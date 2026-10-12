// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Only the fake observer clients run concurrently. The existing complete-run
// fixture remains serialized, so its recorded commands are race-safe.
type observerCleanupTestRuntime struct {
	*recordingPackerRuntime
	mu                         sync.Mutex
	started, attached, removed chan struct{}
	release                    chan struct{}
	blocked                    string
	persistent, retained       bool
	removals                   int
	buildFailure               error
	diagnosticRoot             string
	recoveryConflict           bool
}

func (runtime *observerCleanupTestRuntime) retainUnjoinedPackerActivity() {
	runtime.mu.Lock()
	runtime.retained = true
	runtime.mu.Unlock()
}

func (runtime *observerCleanupTestRuntime) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if len(args) > 2 && args[0] == "start" && strings.HasSuffix(args[2], "-activity") {
		close(runtime.started)
		defer close(runtime.attached)
		if runtime.blocked == "attach" {
			<-runtime.release
		} else {
			<-ctx.Done()
		}
		return ctx.Err()
	}
	if len(args) > 2 && args[0] == "start" && strings.HasSuffix(args[2], "-build") {
		select {
		case <-runtime.started:
		case <-ctx.Done():
			return ctx.Err()
		}
		if runtime.recoveryConflict {
			routes, err := filepath.Glob(filepath.Join(runtime.diagnosticRoot, packerDiagnosticDirectory, packerDiagnosticProducerDir, "run-*"))
			if err != nil || len(routes) != 1 {
				return errors.New("test recovery target unavailable")
			}
			if err := os.Mkdir(filepath.Join(routes[0], packerRetainedObserverName), 0o700); err != nil {
				return err
			}
		}
		if runtime.buildFailure != nil {
			return runtime.buildFailure
		}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.recordingPackerRuntime.Run(ctx, stdin, stdout, stderr, args...)
}

func (runtime *observerCleanupTestRuntime) Cleanup(ctx context.Context, grace time.Duration, stdout, stderr io.Writer, args ...string) error {
	runtime.mu.Lock()
	observer := len(args) > 2 && args[0] == "rm" && strings.HasSuffix(args[2], "-activity")
	if observer {
		runtime.removals++
		first := runtime.removals == 1
		runtime.cleanups = append(runtime.cleanups, append([]string(nil), args...))
		runtime.mu.Unlock()
		if first {
			defer close(runtime.removed)
			if runtime.blocked == "removal" {
				<-runtime.release
			}
		}
		if runtime.blocked == "" && (first || runtime.persistent) {
			return errors.New("private observer removal diagnostic")
		}
		return nil
	}
	defer runtime.mu.Unlock()
	return runtime.recordingPackerRuntime.Cleanup(ctx, grace, stdout, stderr, args...)
}

func TestPackerObserverStopDoesNotSuppressBuildResultsOrDiagnostics(t *testing.T) {
	buildFailure := errors.New("primary build failure")
	for _, test := range []struct {
		name, blocked    string
		persistent       bool
		recoveryConflict bool
		buildErr         error
		wantCode         e.Code
	}{
		{name: "joined removal retry succeeds"},
		{name: "joined removal retry fails", persistent: true, wantCode: e.CodeInternal},
		{name: "failed build unjoined attachment", blocked: "attach", buildErr: buildFailure, wantCode: e.CodeBuildFailed},
		{name: "failed build unjoined removal", blocked: "removal", buildErr: buildFailure, wantCode: e.CodeBuildFailed},
		{name: "successful build unjoined attachment", blocked: "attach", wantCode: e.CodeTimeout},
		{name: "failed build unjoined attachment and recovery conflict", blocked: "attach", buildErr: buildFailure, recoveryConflict: true, wantCode: e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, recorded, _ := newPackerContainerTestRun(t, "Packer v1.14.2\n", "The configuration is valid.\n", "", nil)
			runtime := &observerCleanupTestRuntime{recordingPackerRuntime: recorded, blocked: test.blocked,
				persistent: test.persistent, buildFailure: test.buildErr, started: make(chan struct{}),
				attached: make(chan struct{}), removed: make(chan struct{}), release: make(chan struct{}),
				diagnosticRoot: request.workspace.Name(), recoveryConflict: test.recoveryConflict}
			sink, _ := activityTestSink(t)
			request.runtime, request.activity, request.activityOperation = runtime, sink, request.node.ID
			request.bindings.buildLogDirectoryVariable = "build_logs"
			t.Cleanup(func() {
				close(runtime.release)
				for _, done := range []chan struct{}{runtime.attached, runtime.removed} {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("test-owned observer client did not join")
					}
				}
				// Production retains this exact workspace on an unjoined stop.
				// Only the test owner, after releasing and joining both fake
				// clients, removes its own scratch fixture.
				entries, _ := filepath.Glob(filepath.Join(request.workspace.Name(), ".nvkr-packer-*"))
				for _, entry := range entries {
					if err := removePrivateTree(entry); err != nil {
						t.Error(err)
					}
				}
			})
			// Only the deliberately blocked observer needs the short join bound.
			// Normal diagnostic retention includes archive/file/directory fsyncs;
			// it is not a 100 ms filesystem latency assertion. DiagnosticFailures
			// separately injects and asserts the actual diagnostic deadline path.
			artifact, err := runPackerContainerWithTimeouts(testContext(t), request, packerContainerCleanupTimeout, 100*time.Millisecond)
			if code(err) != test.wantCode || (artifact != nil) != (test.wantCode == "") {
				t.Fatalf("artifact=%v error=%v, want code %s", artifact, err, test.wantCode)
			}
			if artifact != nil {
				defer artifact.Close()
			}
			if test.buildErr != nil && !errors.Is(err, test.buildErr) {
				t.Fatalf("observer stop lost primary build failure: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private observer removal diagnostic") {
				t.Fatal("private observer diagnostic became public")
			}
			recovery, globErr := filepath.Glob(filepath.Join(request.workspace.Name(), packerDiagnosticDirectory,
				packerDiagnosticProducerDir, "run-*", packerRetainedObserverName))
			if globErr != nil || (len(recovery) == 1) != (test.blocked != "") {
				t.Fatal("observer recovery metadata did not follow issued retention", globErr)
			}
			if test.recoveryConflict {
				if !errors.Is(err, os.ErrExist) || !strings.Contains(err.Error(), "creating no-replace") {
					t.Fatal("recovery persistence failure was hidden", err)
				}
			} else if test.blocked != "" {
				raw, readErr := os.ReadFile(recovery[0])
				var record struct{ ObserverContainer, WorkVolume string }
				if readErr != nil || json.Unmarshal(raw, &record) != nil {
					t.Fatal("recovery metadata unavailable", readErr)
				}
				observerCreated, volumeCreated := false, false
				for _, call := range recorded.calls {
					if len(call) > 2 && call[0] == "create" {
						for i := 1; i+1 < len(call); i++ {
							observerCreated = observerCreated || call[i] == "--name" && call[i+1] == record.ObserverContainer
						}
					}
					volumeCreated = volumeCreated || len(call) > 2 && call[0] == "volume" && call[1] == "create" && call[len(call)-1] == record.WorkVolume
				}
				if !observerCreated || !volumeCreated || strings.Contains(err.Error(), record.ObserverContainer) || strings.Contains(err.Error(), record.WorkVolume) {
					t.Fatal("recovery locators were not exact private created objects")
				}
			}
			logs, globErr := filepath.Glob(filepath.Join(request.workspace.Name(), packerDiagnosticDirectory,
				packerDiagnosticProducerDir, "run-*", packerDiagnosticLogsName, "serial.log"))
			if globErr != nil || len(logs) != 1 {
				t.Fatalf("build logs suppressed by observer stop: %v, %v", logs, globErr)
			}
			if content, readErr := os.ReadFile(logs[0]); readErr != nil || string(content) != "failed build serial\n" {
				t.Fatal("build diagnostics not retained", readErr)
			}
			collected, buildRemoved, inputRemoved, workRemoved := false, false, false, false
			for _, call := range recorded.calls {
				if len(call) > 2 && call[0] == "cp" && strings.Contains(call[1], packerContainerOutputPath) {
					collected = true
				}
			}
			for _, call := range recorded.cleanups {
				if len(call) > 2 && call[0] == "rm" && strings.HasSuffix(call[2], "-build") {
					buildRemoved = true
				}
				if len(call) > 3 && call[0] == "volume" {
					inputRemoved = inputRemoved || strings.HasSuffix(call[3], "-input")
					workRemoved = workRemoved || strings.HasSuffix(call[3], "-work")
				}
			}
			if !buildRemoved || !inputRemoved || workRemoved != (test.blocked == "") || collected != (test.blocked == "" && test.buildErr == nil) {
				t.Fatalf("collection/reaping mismatch: artifact=%t build=%t input=%t work=%t", collected, buildRemoved, inputRemoved, workRemoved)
			}
			if runtime.retained != (test.blocked != "") || runtime.removals != map[bool]int{true: 1, false: 2}[test.blocked != ""] {
				t.Fatal("observer runtime retention or retry did not follow joined state")
			}
		})
	}
}
