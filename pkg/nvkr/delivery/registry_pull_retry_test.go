// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"golang.org/x/net/http2"
)

func TestPullPeerResetAdmission(t *testing.T) {
	peer := http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"peer protocol reset", peer, true},
		{"wrapped peer reset", fmt.Errorf("layer read: %w", peer), true},
		{"string imitation", errors.New(peer.Error()), false},
		{"local protocol failure", http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("protocol error: headers after END_STREAM")}, false},
		{"missing peer cause", http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol}, false},
		{"zero stream", http2.StreamError{Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}, false},
		{"different reset", http2.StreamError{StreamID: 3, Code: http2.ErrCodeCancel, Cause: errors.New("received from peer")}, false},
		{"joined integrity failure", errors.Join(peer, e.New(e.CodeVerifyFailed, "checksum mismatch")), false},
		{"coded integrity failure", e.Wrap(e.CodeVerifyFailed, peer, "rejected bytes"), false},
		{"coded outage", e.Wrap(e.CodeUnavailable, peer, "not a raw body error"), false},
		{"TLS wrapper", &tls.CertificateVerificationError{Err: peer}, false},
		{"filesystem wrapper", &os.PathError{Op: "write", Path: "payload", Err: peer}, false},
		{"connection error", http2.ConnectionError(http2.ErrCodeProtocol), false},
		{"unexpected EOF", io.ErrUnexpectedEOF, false},
		{"cancellation", context.Canceled, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isPeerProtocolReset(test.err); got != test.want {
				t.Fatalf("peer reset admitted=%v, want %v", got, test.want)
			}
		})
	}
}

func TestPullReadRecoveryOwnsStagesAndBounds(t *testing.T) {
	interrupted := e.Wrap(e.CodeUnavailable, &interruptedPullBody{}, "materializing disk")
	for _, test := range []struct {
		name        string
		cause       error
		cleanupFail bool
		cancelWait  bool
		attempts    int
		code        e.Code
	}{
		{"bounded peer resets", interrupted, false, false, 3, e.CodeUnavailable},
		{"integrity failure", e.New(e.CodeVerifyFailed, "checksum mismatch"), false, false, 1, e.CodeVerifyFailed},
		{"joined independent failure", e.Wrap(e.CodeVerifyFailed, errors.Join(interrupted, errors.New("disk full")), "failed"), false, false, 1, e.CodeVerifyFailed},
		{"cleanup failure", interrupted, true, false, 1, e.CodeImmutableViolation},
		{"cancel backoff", interrupted, false, true, 1, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			deadline, _ := ctx.Deadline()
			out := filepath.Join(t.TempDir(), "release")
			var stages []string
			var delays []time.Duration
			acquire := func(attemptCtx context.Context, stage string) (Inspection, error) {
				if got, _ := attemptCtx.Deadline(); !got.Equal(deadline) {
					t.Fatal("retry changed the caller's deadline")
				}
				for _, previous := range stages {
					if _, err := os.Stat(previous); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("previous payload staging survived into another attempt")
					}
					if previous == stage {
						t.Fatal("retry reused staging")
					}
				}
				stages = append(stages, stage)
				if err := os.WriteFile(filepath.Join(stage, "partial"), []byte("partial payload"), 0o600); err != nil {
					t.Fatal(err)
				}
				return Inspection{}, test.cause
			}
			remove := func(stage string) error {
				if test.cleanupFail {
					return errors.New("cleanup denied")
				}
				return os.RemoveAll(stage)
			}
			wait := func(waitCtx context.Context, delay time.Duration) error {
				delays = append(delays, delay)
				if test.cancelWait {
					cancel()
				}
				return waitCtx.Err()
			}
			_, err := pullWithRecoveryUsing(ctx, out, acquire, remove, wait)
			assertRegistryCode(t, err, test.code)
			if len(stages) != test.attempts {
				t.Fatalf("attempts=%d, want %d", len(stages), test.attempts)
			}
			if test.attempts == 3 && !reflect.DeepEqual(delays, []time.Duration{time.Second, 2 * time.Second}) {
				t.Fatalf("unexpected retry delays: %v", delays)
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed acquisition exposed a destination")
			}
		})
	}
}

func TestPullRecoveryRetainsFullFinalInspection(t *testing.T) {
	root, expected := registryPublicationFixture(t)
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "release")
			attempts := 0
			acquire := func(ctx context.Context, stage string) (Inspection, error) {
				attempts++
				if attempts == 1 {
					return Inspection{}, e.Wrap(e.CodeUnavailable, &interruptedPullBody{}, "disk interrupted")
				}
				if err := os.CopyFS(stage, os.DirFS(root)); err != nil {
					return Inspection{}, err
				}
				if !valid {
					if err := os.WriteFile(filepath.Join(stage, "index.json"), []byte("invalid"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				// A descriptive acquisition result cannot bypass the final owner.
				return Inspection{RootDigest: expected.RootDigest}, nil
			}
			got, err := pullWithRecoveryUsing(t.Context(), out, acquire, os.RemoveAll, func(context.Context, time.Duration) error { return nil })
			if valid {
				if err != nil || got.RootDigest != expected.RootDigest {
					t.Fatalf("recovered publication did not authenticate: %v", err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed final inspection exposed output")
				}
			}
			if attempts != 2 {
				t.Fatalf("post-commit result authorized another attempt: %d", attempts)
			}
		})
	}
}

type failedPullLayer struct {
	v1.Layer
	cause, closeErr error
	closed          *bool
}

func (layer failedPullLayer) Compressed() (io.ReadCloser, error) {
	return failedPullBody{cause: layer.cause, closeErr: layer.closeErr, closed: layer.closed}, nil
}

type failedPullBody struct {
	cause, closeErr error
	closed          *bool
}

func (body failedPullBody) Read([]byte) (int, error) { return 0, body.cause }
func (body failedPullBody) Close() error {
	*body.closed = true
	return body.closeErr
}

func TestPullLayerMarkerRequiresSuccessfulClose(t *testing.T) {
	root, object, _ := registryDiskFixture(t, "arm64")
	image, owner, err := registryDiskImage(t.Context(), root, object)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, closeFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(closeFailure), func(t *testing.T) {
			closed := false
			layer := failedPullLayer{Layer: layers[0], closed: &closed,
				cause: http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}}
			if closeFailure {
				layer.closeErr = errors.New("sensitive close detail must not escape")
			}
			wrapped := pullObservedImage{Image: image, layer: layer}
			err := writePulledImage(t.Context(), t.TempDir(), object, wrapped)
			if err == nil || !closed || isInterruptedPullBody(err) == closeFailure {
				t.Fatalf("unexpected body lifecycle: closed=%v retry=%v err=%v", closed, isInterruptedPullBody(err), err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatal("close diagnostic retained provider data")
			}
			if closeFailure {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if e.Retryable(err) {
					t.Fatal("failed response close remained publicly retryable")
				}
			}
		})
	}
}

type resetWithData struct{ delivered int }

func (body resetWithData) Read(buffer []byte) (int, error) {
	n := min(body.delivered, len(buffer))
	clear(buffer[:n])
	return n, http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}
}

func (resetWithData) Close() error { return nil }

func TestPullResetAfterDeclaredSizeCannotHideIntegrity(t *testing.T) {
	for _, delivered := range []int{1, 2} {
		t.Run(fmt.Sprint(delivered), func(t *testing.T) {
			body := &pullObservedBody{ReadCloser: resetWithData{delivered: delivered}, state: &pullBodyState{}, remaining: 2}
			n, err := body.Read(make([]byte, 4))
			if n != delivered {
				t.Fatal("body changed returned byte count")
			}
			if delivered == 1 {
				if !isInterruptedPullBody(err) {
					t.Fatal("partial typed reset lost its recovery marker")
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if isInterruptedPullBody(err) || e.Retryable(err) {
					t.Fatal("complete but unverified body authorized retry")
				}
			}
		})
	}
}

func TestPullRecoveryCancellationAndStagingAdmission(t *testing.T) {
	for _, test := range []string{"canceled", "parent file"} {
		t.Run(test, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			parent := filepath.Join(t.TempDir(), "parent")
			want := e.CodeTimeout
			if test == "canceled" {
				cancel()
			} else {
				want = e.CodeImmutableViolation
				if err := os.WriteFile(parent, []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := pullWithRecovery(ctx, filepath.Join(parent, "release"), func(context.Context, string) (Inspection, error) {
				t.Fatal("acquisition started before staging/context admission")
				return Inspection{}, nil
			})
			assertRegistryCode(t, err, want)
		})
	}
}
