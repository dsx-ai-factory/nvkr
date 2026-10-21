// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package siteidentity

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/dsx-ai-factory/nvke/agent/pkg/agent/proto"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"google.golang.org/grpc"
)

func TestDecodeCompleteBoundedRequest(t *testing.T) {
	raw, err := canonical.Marshal(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		raw       []byte
		wantError bool
	}{
		{"valid", raw, false},
		{"oversized valid prefix", append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), MaxRequestBytes)...), true},
		{"second document", append(append([]byte{}, raw...), []byte(" {}")...), true},
		{"unknown field", []byte(`{"extra":1}`), true},
		{"duplicate operation", []byte(`{"operation":"probe","operation":"handoff"}`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeRequest(context.Background(), tc.raw)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v wantError=%v", err, tc.wantError)
			}
			if err != nil && e.ExitCode(err) != e.ExitCode(e.New(e.CodeUsage, "synthetic")) {
				t.Fatalf("uncoded input refusal: %v", err)
			}
		})
	}
}

func TestExecuteFixedCompletionStates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		response  *pb.ExecuteScriptResponse
		transport bool
		panic     bool
		want      Completion
		wantCode  e.Code
	}{
		{"success awaits fresh identity", &pb.ExecuteScriptResponse{Status: "success"}, false, false, JoinedFailure, ""},
		{"joined guest failure", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 1, ErrorMessage: "synthetic private detail"}, false, false, JoinedFailure, e.CodePreflightFailed},
		{"generic manager failure marked unknown", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 1, ErrorMessage: "synthetic private detail", Output: unknownCompletionSentinel}, false, false, Unknown, e.CodeVerifyFailed},
		{"reserved manager failure", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 125, ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"command timeout", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 124, ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"not executable", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 126, ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"command missing", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 127, ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"signal exit", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 143, ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"negative signal exit", &pb.ExecuteScriptResponse{Status: "error", ExitCode: -1, ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"error with zero exit", &pb.ExecuteScriptResponse{Status: "error", ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"error without message", &pb.ExecuteScriptResponse{Status: "error", ExitCode: 1}, false, false, Unknown, e.CodeVerifyFailed},
		{"success with nonzero exit", &pb.ExecuteScriptResponse{Status: "success", ExitCode: 1}, false, false, Unknown, e.CodeVerifyFailed},
		{"success with error message", &pb.ExecuteScriptResponse{Status: "success", ErrorMessage: "synthetic private detail"}, false, false, Unknown, e.CodeVerifyFailed},
		{"success with unknown marker", &pb.ExecuteScriptResponse{Status: "success", Output: unknownCompletionSentinel}, false, false, Unknown, e.CodeVerifyFailed},
		{"lost response", nil, true, false, Unknown, e.CodePreflightFailed},
		{"unrecognized response", &pb.ExecuteScriptResponse{Status: "other"}, false, false, Unknown, e.CodeVerifyFailed},
		{"nil response", nil, false, false, Unknown, e.CodeVerifyFailed},
		{"client panic", nil, false, true, Unknown, e.CodePreflightFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completion, _, err := executeFixed(context.Background(), validRequest(), func(_ context.Context, r *pb.ExecuteScriptRequest, _ ...grpc.CallOption) (*pb.ExecuteScriptResponse, error) {
				if r.Async || r.ScriptContents != handoffScript || len(r.Args) != 7 {
					t.Fatal("arbitrary or async operation dispatched")
				}
				if tc.panic {
					panic("synthetic private detail")
				}
				if tc.transport {
					return nil, errors.New("synthetic private detail")
				}
				return tc.response, nil
			}, time.Second)
			if completion != tc.want {
				t.Fatalf("completion=%s want=%s err=%v", completion, tc.want, err)
			}
			if tc.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.wantCode {
					t.Fatalf("coded boundary differs: %v", err)
				}
				if strings.Contains(err.Error(), "private detail") {
					t.Fatal("private RPC diagnostics escaped")
				}
			}
			if (completion == Unknown) != errors.Is(err, ErrRemoteCompletionUnknown) {
				t.Fatalf("unknown lifecycle lost: %v", err)
			}
		})
	}
}

func TestCanceledBeforeDispatchDoesNoRemoteWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	completion, _, err := executeFixed(ctx, validRequest(), func(context.Context, *pb.ExecuteScriptRequest, ...grpc.CallOption) (*pb.ExecuteScriptResponse, error) {
		t.Fatal("canceled operation dispatched")
		return nil, nil
	}, time.Second)
	if completion != NotStarted || !errors.Is(err, context.Canceled) {
		t.Fatalf("completion=%s err=%v", completion, err)
	}
}

func TestCancellationDrainsAcceptedSynchronousCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	joined := make(chan struct{})
	type outcome struct {
		completion Completion
		err        error
	}
	done := make(chan outcome, 1)
	go func() {
		defer close(joined)
		c, _, err := executeFixed(ctx, validRequest(), func(critical context.Context, _ *pb.ExecuteScriptRequest, _ ...grpc.CallOption) (*pb.ExecuteScriptResponse, error) {
			entered <- critical
			select {
			case <-release:
				return &pb.ExecuteScriptResponse{Status: "success"}, nil
			case <-critical.Done():
				return nil, critical.Err()
			}
		}, time.Second)
		done <- outcome{c, err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("critical call not joined")
		}
	})
	var critical context.Context
	select {
	case critical = <-entered:
	case <-time.After(time.Second):
		t.Fatal("did not dispatch")
	}
	cancel()
	if critical.Err() != nil {
		t.Fatal("parent cancellation killed the accepted operation")
	}
	select {
	case <-joined:
		t.Fatal("returned before accepted synchronous call joined")
	default:
	}
	close(release)
	select {
	case got := <-done:
		if got.completion != JoinedFailure || got.err != nil {
			t.Fatalf("unconfirmed remote join: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not join completed synchronous call")
	}
}

func TestCriticalDeadlineRemainsUnknown(t *testing.T) {
	completion, _, err := executeFixed(context.Background(), validRequest(), func(ctx context.Context, _ *pb.ExecuteScriptRequest, _ ...grpc.CallOption) (*pb.ExecuteScriptResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, 10*time.Millisecond)
	if completion != Unknown || !errors.Is(err, ErrRemoteCompletionUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("completion=%s err=%v", completion, err)
	}
}

func TestInputCancellationJoinsDeadlineCallback(t *testing.T) {
	in, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer write.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() { defer close(joined); _, err := readInput(ctx, in); done <- err }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("input callback/read did not join")
		}
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input read did not cancel")
	}
}

func TestOutputAdmissionPrecedesInputOrIdentity(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "unpollable-output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, out := range []*os.File{nil, file} {
		// A nil input would otherwise fail, and no request/credential has been read.
		result, err := RunHelper(context.Background(), nil, out)
		if result.Completion != NotStarted || err == nil || e.ExitCode(err) != e.ExitCode(e.New(e.CodeEngineCapability, "synthetic")) {
			t.Fatalf("output refusal did not remain not-started: %+v %v", result, err)
		}
	}
}
