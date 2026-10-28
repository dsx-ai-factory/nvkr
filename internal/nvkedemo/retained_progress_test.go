// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type retainedTypedStatusBuffer struct {
	bytes.Buffer
	statuses    int
	walkthrough bool
}

func (out *retainedTypedStatusBuffer) WriteOperatorStatus(status OperatorStatus) error {
	out.statuses++
	out.walkthrough = status.Walkthrough()
	_, err := out.WriteString(status.Text())
	return err
}

func TestRetainedOperatorStatusPreservesTypedPublicObservation(t *testing.T) {
	access, runner := operatorStatusFixture(t)
	progress := &retainedProgress{}
	if err := access.WriteLiveStatus(t.Context(), runner, progress); err != nil {
		t.Fatal(err)
	}
	// Exercise the existing JSON wire codec, not an in-process opaque value.
	raw, err := json.Marshal(progress.frames())
	if err != nil {
		t.Fatal(err)
	}
	var frames []retainedProgressFrame
	if err := json.Unmarshal(raw, &frames); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Text != "" || frames[0].Event != nil {
		t.Fatal("owner result was fragmented or confused with generic logs")
	}
	var typed retainedTypedStatusBuffer
	var cursor uint64
	if err := relayRetainedProgress(&typed, &cursor, frames); err != nil || typed.statuses != 1 {
		t.Fatal("typed cluster presenter was bypassed", err, typed.statuses)
	}
	for _, wanted := range []string{"Live Kubernetes checks", "Site / Kind management", "Tenant / QCOW2", "Nodes: 2/2 Ready", "control-plane", "worker", "arm64"} {
		if !strings.Contains(typed.String(), wanted) {
			t.Fatal("retained public observation omitted", wanted)
		}
	}
	if strings.Contains(typed.String(), "PRIVATE") || strings.Contains(typed.String(), "private-") || strings.ContainsRune(typed.String(), '\x1b') {
		t.Fatal("retained public observation exposed private responses or terminal controls")
	}
	if err := relayRetainedProgress(&typed, &cursor, frames); err != nil || typed.statuses != 1 {
		t.Fatal("repeat control snapshot duplicated the cluster observation", err)
	}
	var plain bytes.Buffer
	cursor = 0
	if err := relayRetainedProgress(&plain, &cursor, frames); err != nil || plain.String() != typed.String() {
		t.Fatal("ordinary output lost the same public observation", err)
	}
	cursor = 0
	err = relayRetainedProgress(operatorShortWriter{}, &cursor, frames)
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || !errors.Is(err, io.ErrShortWrite) || cursor != 0 {
		t.Fatal("short output was accepted or advanced the cursor", err, cursor)
	}
}

func TestRetainedOperatorStatusRejectsInvalidOrOversizeEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
	}{
		{"unissued", ""},
		{"oversize", strings.Repeat("x", retainedProgressLimit)},
		{"encoded oversize", strings.Repeat("\n", retainedProgressLimit/2)},
	} {
		t.Run(test.name, func(t *testing.T) {
			progress := &retainedProgress{}
			var coded *nvkrerr.Error
			if err := progress.WriteOperatorStatus(OperatorStatus{text: test.text}); !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || len(progress.frames()) != 0 {
				t.Fatal("unissued or oversize cluster envelope was accepted", err)
			}
			if test.text != "" {
				progress.Report(ProgressEvent{Kind: PhaseStarted, Name: "after refused observation"})
				var cursor uint64
				var notices bytes.Buffer
				if err := relayRetainedProgress(&notices, &cursor, progress.frames()); err != nil || cursor != 2 || strings.Count(notices.String(), "Warning: foreground missed") != 1 {
					t.Fatal("refused result was not reported and resynchronized", err)
				}
			}
		})
	}
	for _, frame := range []retainedProgressFrame{
		{Sequence: 1, Walkthrough: true},
		{Sequence: 1, Status: "public fixture", Text: "ambiguous log"},
		{Sequence: 1, Status: "public fixture", Event: &ProgressEvent{}},
		{Sequence: 1, Status: strings.Repeat("x", retainedProgressLimit+1)},
	} {
		var out retainedTypedStatusBuffer
		var cursor uint64
		var coded *nvkrerr.Error
		if err := relayRetainedProgress(&out, &cursor, []retainedProgressFrame{frame}); !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || out.Len() != 0 || cursor != 0 {
			t.Fatal("invalid envelope reached a presenter", err)
		}
	}
}

func TestRetainedWalkthroughKindSurvivesWireCodec(t *testing.T) {
	progress := &retainedProgress{}
	if err := progress.WriteOperatorStatus(OperatorStatus{text: "get pods -A\n", walkthrough: true}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(progress.frames())
	if err != nil {
		t.Fatal(err)
	}
	var frames []retainedProgressFrame
	if err := json.Unmarshal(raw, &frames); err != nil {
		t.Fatal(err)
	}
	var out retainedTypedStatusBuffer
	var cursor uint64
	if err := relayRetainedProgress(&out, &cursor, frames); err != nil || !out.walkthrough || out.String() != "get pods -A\n" {
		t.Fatal("relay erased walkthrough presentation kind", err)
	}
}

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
	if err := relayRetainedProgress(&out, &cursor, progress.frames()); err != nil || strings.Count(out.String(), "Warning: foreground missed") != 1 || cursor != 102 {
		t.Fatal("evicted progress did not visibly resynchronize", err, cursor)
	}
	if err := relayRetainedProgress(&out, &cursor, progress.frames()); err != nil || strings.Count(out.String(), "Warning: foreground missed") != 1 {
		t.Fatal("repeated snapshot duplicated the gap warning", err)
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
		var coded *nvkrerr.Error
		if err := CleanupRetained(ctx, "/must-not-read", nil); !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
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
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeInternal || !errors.Is(err, ErrRetainedControls) || strings.Contains(err.Error(), "PRIVATE-PANIC-SENTINEL") {
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
	if readErr != nil || len(stack) == 0 || !bytes.Contains(stack, []byte("retainedWork")) || !bytes.Contains(stack, []byte("PRIVATE-PANIC-SENTINEL")) {
		t.Fatal("actual panic value and stack not retained privately", readErr)
	}
}

func TestRetainedPrimaryFailureIsReadableBeforeCleanup(t *testing.T) {
	for _, mode := range []string{"entry hold", "native finalizer"} {
		t.Run(mode, func(t *testing.T) {
			owner, work, _ := retainedTestOwner(t)
			root := privateTestDir(t)
			path := filepath.Join(root, "owner-error.log")
			owner.failureRecorder = func(err error) error { return privateWrite(path, []byte(err.Error())) }
			primary := errors.New("private-primary-failure-sentinel")
			joined := make(chan error, 1)
			var scope *nativeSiteScope
			go func() {
				if mode == "entry hold" {
					failureErr := owner.retainFailure(primary)
					joined <- errors.Join(failureErr, owner.hold())
				} else {
					joined <- withNativeSiteScope(work, &nativeSiteAttempt{}, Runner{}, "", "", func(_ context.Context, site *NativeSite) error {
						scope = site.scope
						return primary
					})
				}
			}()
			defer func() {
				if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
				select {
				case result := <-joined:
					if mode == "native finalizer" && (!errors.Is(result, primary) || scope == nil || !scope.closed) {
						t.Error("native finalizer lost its primary failure or scope invalidation", result)
					}
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
			if raw, err := os.ReadFile(path); err != nil || string(raw) != primary.Error() {
				t.Fatal("later failure replaced the original private cause", err)
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
	joined := make(chan error, 1)
	done := make(chan struct{})
	var scope *nativeSiteScope
	go func() {
		defer close(done)
		joined <- withNativeSiteScope(work, &nativeSiteAttempt{}, Runner{}, "", "", func(_ context.Context, site *NativeSite) error {
			scope = site.scope
			return primary
		})
	}()
	defer func() {
		if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("native diagnostic-failure finalizer did not join")
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
		if scope == nil || !scope.closed {
			t.Fatal("diagnostic storage failure skipped native scope invalidation")
		}
		owner.recordTeardownReturn(result)
		if err := owner.joinedCleanup(); err != nil {
			t.Fatal("actual joined cleanup confused with diagnostic storage failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit teardown did not join")
	}
}
