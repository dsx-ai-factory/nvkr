// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRetainedProgressRelaysBoundedOrderedPublicFrames(t *testing.T) {
	progress := &retainedProgress{}
	if _, err := progress.Write([]byte("public fixture output\n")); err != nil {
		t.Fatal(err)
	}
	progress.Report(ProgressEvent{Kind: PhaseStarted, Name: "fixture"})
	var out bytes.Buffer
	var cursor uint64
	frames := progress.frames()
	if err := relayRetainedProgress(&out, &cursor, frames); err != nil || cursor != 2 || out.String() != "public fixture output\n" {
		t.Fatal("public frame order was lost", cursor, out.String(), err)
	}
	if err := relayRetainedProgress(&out, &cursor, frames); err != nil || out.String() != "public fixture output\n" {
		t.Fatal("repeated observation duplicated output", err)
	}
	for range 100 {
		_, _ = progress.Write([]byte(strings.Repeat("x", 1024)))
	}
	raw, err := json.Marshal(progress.frames())
	if err != nil || len(raw) > retainedProgressLimit+2 {
		t.Fatal("control progress envelope exceeded its bound", len(raw), err)
	}
	if err := relayRetainedProgress(&out, &cursor, progress.frames()); err == nil {
		t.Fatal("evicted progress became a hidden stall")
	}
	if progress.SudoAuthentication() != nil {
		t.Fatal("unattended lane acquired an authentication bridge")
	}
}

func TestRetainedPrivateRecordsRejectSubstitutionsBeforeWork(t *testing.T) {
	for _, test := range []struct {
		name, body string
		mode       os.FileMode
		symlink    bool
	}{
		{"valid", `{"version":1,"work":"/fixture","socket":"private","token":"private","pid":1}`, 0o600, false},
		{"public", `{}`, 0o644, false},
		{"overflow", strings.Repeat(" ", retainedControlLimit+1), 0o600, false},
		{"unknown", `{"invented":true}`, 0o600, false},
		{"second document", `{} {}`, 0o600, false},
		{"symlink", `{}`, 0o600, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(privateTestDir(t), "record.json")
			if err := os.WriteFile(path, []byte(test.body), test.mode); err != nil {
				t.Fatal(err)
			}
			if test.symlink {
				link := path + "-link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			_, err := readRetainedJSON[retainedControlDescription](t.Context(), path)
			if (err == nil) != (test.name == "valid") {
				t.Fatal("private record admission differed", err)
			}
		})
	}
}

func TestRetainedCleanupRejectsUnboundedContextBeforeRequest(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.WithoutCancel(t.Context())} {
		var coded *nkderr.Error
		if err := CleanupRetained(ctx, "/must-not-read", nil); !errors.As(err, &coded) || coded.Code != nkderr.CodeUsage {
			t.Fatal("unbounded cleanup read or mutated a session", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var session *RetainedSession
	if _, err := session.request(ctx, "GET", "/state"); err == nil {
		t.Fatal("unissued locator supplied a request")
	}
}

func TestRetainedWorkPanicRemainsPrivateAndDoesNotInventCleanup(t *testing.T) {
	root := privateTestDir(t)
	err := retainedWork(t.Context(), root, Config{}, func(context.Context, Config) error { panic("PRIVATE-PANIC-SENTINEL") })
	var coded *nkderr.Error
	if !errors.As(err, &coded) || coded.Code != nkderr.CodeInternal || !errors.Is(err, ErrRetainedControls) || strings.Contains(err.Error(), "PRIVATE-PANIC-SENTINEL") {
		t.Fatal("panic payload escaped or failure lost", err)
	}
	owner, _, _ := retainedTestOwner(t)
	if cleanupErr := owner.requestCleanup(t.Context()); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	owner.recordTeardownReturn(err)
	if !errors.Is(owner.joinedCleanup(), ErrRetainedControls) {
		t.Fatal("panic falsely authorized joined cleanup")
	}
	stack, readErr := os.ReadFile(filepath.Join(root, "owner-panic.log"))
	if readErr != nil || len(stack) == 0 || !bytes.Contains(stack, []byte("retainedWork")) {
		t.Fatal("actual panic stack not retained privately", readErr)
	}
}

func TestRetainedPrimaryFailureIsReadableBeforeCleanup(t *testing.T) {
	for _, mode := range []string{"entry hold", "nested finalizer"} {
		t.Run(mode, func(t *testing.T) {
			owner, work, _ := retainedTestOwner(t)
			root := privateTestDir(t)
			path := filepath.Join(root, "owner-error.log")
			owner.failureRecorder = func(err error) error { return privateWrite(path, []byte(err.Error())) }
			primary := errors.New("private-primary-failure-sentinel")
			joined := make(chan error, 1)
			go func() {
				if mode == "entry hold" {
					failureErr := owner.retainFailure(primary)
					joined <- errors.Join(failureErr, owner.hold())
				} else {
					joined <- cleanupAfterJoinedWork(work, primary, time.Second, func(context.Context) error { return nil })
				}
			}()
			defer func() {
				if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
				select {
				case <-joined:
				case <-time.After(2 * time.Second):
					t.Error("test failure owner not joined")
				}
			}()
			if snapshot := retainedObserve(t, owner); snapshot.state != retainedPartial {
				t.Fatal("failure not retained", snapshot)
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != primary.Error() {
				t.Fatal("primary cause hidden until cleanup", err)
			}
			if err := owner.retainFailure(errors.New("later failure")); err != nil {
				t.Fatal("first private failure overwritten", err)
			}
			select {
			case <-joined:
				t.Fatal("failure recording released owned resources")
			default:
			}
		})
	}
}

func TestRetainedDiagnosticFailureDoesNotSkipExplicitTeardown(t *testing.T) {
	owner, work, _ := retainedTestOwner(t)
	diagnosticErr := errors.New("diagnostic storage unavailable")
	owner.failureRecorder = func(error) error { return diagnosticErr }
	primary := errors.New("operation failed")
	cleaned := make(chan struct{})
	joined := make(chan error, 1)
	go func() {
		joined <- cleanupAfterJoinedWork(work, primary, time.Second, func(context.Context) error { close(cleaned); return nil })
	}()
	defer func() {
		if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()
	if snapshot := retainedObserve(t, owner); snapshot.state != retainedPartial {
		t.Fatal(snapshot)
	}
	if err := owner.requestCleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-joined:
		if !errors.Is(result, primary) || !errors.Is(owner.retainFailure(primary), diagnosticErr) {
			t.Fatal("primary/recording failures were lost", result)
		}
		select {
		case <-cleaned:
		default:
			t.Fatal("diagnostic storage failure skipped natural teardown")
		}
		owner.recordTeardownReturn(result)
		if err := owner.joinedCleanup(); err != nil {
			t.Fatal("actual joined cleanup confused with diagnostic storage failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit teardown did not join")
	}
}
