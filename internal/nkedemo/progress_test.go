// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type recordedProgress struct {
	bytes.Buffer
	events      []ProgressEvent
	panicReport bool
}

func (writer *recordedProgress) Report(event ProgressEvent) {
	writer.events = append(writer.events, event)
	if writer.panicReport {
		panic("presenter failure")
	}
}

func phaseEvents(name, last string) []ProgressEvent {
	status := "finished"
	if last == PhaseFailed {
		status = "failed"
	}
	return []ProgressEvent{{Kind: PhaseStarted, Name: name, Status: "started"}, {Kind: last, Name: name, Status: status}}
}

func TestProgressRunnerObservedBoundaries(t *testing.T) {
	for _, name := range []string{"success", "exit failure", "lookup failure", "exec failure", "timeout", "cancellation", "canceled before start", "invalid phase", "invalid timeout", "storage failure"} {
		t.Run(name, func(t *testing.T) {
			writer := &recordedProgress{}
			work := privateTestDir(t)
			runner := Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "PRIVATE_ENV=secret-progress-sentinel"}, Progress: writer}
			args := []string{"/bin/sh", "-c", "printf secret-progress-sentinel; printf secret-progress-sentinel >&2"}
			ctx := t.Context()
			timeout := 5 * time.Second
			phase := "public-phase"
			wantLast := PhaseFailed
			switch name {
			case "success":
				wantLast = PhaseFinished
			case "exit failure":
				args[2] += "; exit 7"
			case "lookup failure":
				args = []string{"absent-secret-progress-sentinel"}
			case "exec failure":
				path := filepath.Join(work, "executable")
				if err := os.WriteFile(path, []byte("#!/absent/secret-progress-sentinel\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				args = []string{path}
			case "timeout", "cancellation":
				args[2] += "; exec sleep 60"
				if name == "timeout" {
					timeout = 100 * time.Millisecond
				} else {
					cancelCtx, cancel := context.WithCancel(ctx)
					ctx = cancelCtx
					defer cancel()
					timer := time.AfterFunc(100*time.Millisecond, cancel)
					defer timer.Stop()
				}
			case "canceled before start":
				cancelCtx, cancel := context.WithCancel(ctx)
				ctx = cancelCtx
				cancel()
			case "invalid phase":
				phase = "../secret-progress-sentinel"
			case "invalid timeout":
				timeout = 0
			case "storage failure":
				if err := os.Chmod(work, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			_, err := runner.Capture(ctx, args, phase, timeout)
			if (err == nil) != (name == "success") {
				t.Fatalf("unexpected result: %v", err)
			}
			if name == "invalid phase" {
				phase = "command"
			}
			if !reflect.DeepEqual(writer.events, phaseEvents(phase, wantLast)) {
				t.Fatalf("events=%+v", writer.events)
			}
			for _, event := range writer.events {
				if strings.Contains(event.Kind+event.Name+event.Status, "secret-progress-sentinel") {
					t.Fatal("events exposed private command data")
				}
			}
			if strings.Contains(writer.String(), "secret-progress-sentinel") {
				t.Fatal("private output reached progress writer")
			}
			if errors.Is(err, ErrUnjoined) {
				t.Fatal("fixture producer did not join", err)
			}
		})
	}
}

func TestProgressOwnedStartStopObservedSynchronously(t *testing.T) {
	for _, name := range []string{"exit success", "exit failure", "stop first exit success", "stop first exit failure", "stop joined", "start failure", "stop unjoined"} {
		t.Run(name, func(t *testing.T) {
			writer := &recordedProgress{}
			var process *OwnedProcess
			if name == "stop unjoined" {
				process = &OwnedProcess{cmd: &exec.Cmd{Process: &os.Process{Pid: 123}}, phase: "daemon", done: make(chan struct{}), groupSignal: func(int, unix.Signal) error { return unix.EPERM }, progress: beginProgress(writer, "daemon", PhaseStarted, PhaseFinished, PhaseFailed)}
			} else {
				command := "exit 0"
				if name == "exit failure" || name == "stop first exit failure" {
					command = "exit 7"
				}
				if name == "stop joined" {
					command = "exec sleep 60"
				}
				args := []string{"/bin/sh", "-c", command}
				if name == "start failure" {
					args = []string{"/absent/executable"}
				}
				var err error
				process, err = (Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: writer}).Start(t.Context(), args, "daemon")
				if name == "start failure" {
					if err == nil || !reflect.DeepEqual(writer.events, phaseEvents("daemon", PhaseFailed)) {
						t.Fatalf("events=%+v error=%v", writer.events, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(writer.events) != 1 {
				t.Fatal("terminal event before synchronous observation")
			}
			last := PhaseFinished
			if strings.HasPrefix(name, "stop first") {
				select {
				case <-process.done:
				case <-time.After(5 * time.Second):
					t.Fatal("process did not join")
				}
				if len(writer.events) != 1 {
					t.Fatal("Wait goroutine reported a presentation event")
				}
				if err := process.Stop(t.Context()); err != nil {
					t.Fatal("joined failed child must not change cleanup return", err)
				}
				if name == "stop first exit failure" {
					last = PhaseFailed
				}
				_, _ = process.Exited()
				_ = process.Stop(t.Context())
			} else if name == "stop joined" || name == "stop unjoined" {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				err := process.Stop(ctx)
				if name == "stop unjoined" {
					last = PhaseFailed
					if !errors.Is(err, ErrUnjoined) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				_ = process.Stop(ctx)
			} else {
				select {
				case <-process.done:
				case <-time.After(5 * time.Second):
					t.Fatal("process did not join")
				}
				if len(writer.events) != 1 {
					t.Fatal("Wait goroutine reported a presentation event")
				}
				exited, err := process.Exited()
				if !exited || (err != nil) != (name == "exit failure") {
					t.Fatalf("exited=%t error=%v", exited, err)
				}
				if name == "exit failure" {
					last = PhaseFailed
				}
				_, _ = process.Exited()
			}
			if !reflect.DeepEqual(writer.events, phaseEvents("daemon", last)) {
				t.Fatalf("events=%+v", writer.events)
			}
		})
	}
}

func TestProgressCombinedBuildOrdering(t *testing.T) {
	for _, failLane := range []string{"", "all"} {
		t.Run("failure-"+failLane, func(t *testing.T) {
			writer := &recordedProgress{}
			script := filepath.Join(privateTestDir(t), "launcher.sh")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nset -eu\n[ \"$3\" != \""+failLane+"\" ] || exit 7\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: writer}
			err := buildLocalArtifacts(t.Context(), runner, script, BuildConfig{AttemptPrefix: "fixture", Jobs: 4})
			phaseLast, laneLast, status := PhaseFinished, LaneFinished, "finished"
			if failLane != "" {
				phaseLast, laneLast, status = PhaseFailed, LaneFailed, "failed"
				if CommandExitCode(err) != 7 {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := []ProgressEvent{{Kind: LaneStarted, Name: "artifacts", Status: "started"}}
			want = append(want, phaseEvents("build-all", phaseLast)...)
			want = append(want, ProgressEvent{Kind: laneLast, Name: "artifacts", Status: status})
			if !reflect.DeepEqual(writer.events, want) {
				t.Fatalf("events=%+v want=%+v", writer.events, want)
			}
		})
	}
}

func TestProgressPlainWriterAndPresenterFailure(t *testing.T) {
	for _, panicReport := range []bool{false, true} {
		writer := &recordedProgress{panicReport: panicReport}
		var plain bytes.Buffer
		for _, progress := range []io.Writer{&plain, writer} {
			if err := (Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: progress}).Run(t.Context(), []string{"/bin/sh", "-c", "exit 0"}, "preflight-os", time.Second); err != nil {
				t.Fatal(err)
			}
		}
		if plain.String() != "Demo phase: preflight-os\n" || writer.String() != plain.String() {
			t.Fatal("plain progress output changed")
		}
		if !reflect.DeepEqual(writer.events, phaseEvents("preflight-os", PhaseFinished)) {
			t.Fatal(writer.events)
		}
	}
}

func TestProgressDeploymentFailureAndCleanupOrdering(t *testing.T) {
	writer := &recordedProgress{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Run(ctx, Config{Progress: writer}); err == nil {
		t.Fatal("invalid deployment succeeded")
	}
	want := []ProgressEvent{{Kind: DeploymentStarted, Name: "local-demo", Status: "started"}, {Kind: DeploymentFailed, Name: "local-demo", Status: "failed"}}
	if !reflect.DeepEqual(writer.events, want) {
		t.Fatal(writer.events)
	}
	// Model the wrapper's deferred boundary around an operation with deferred
	// cleanup. Success presentation cannot run until cleanup has returned.
	writer = &recordedProgress{}
	cleaned := false
	operation := func() (result error) {
		boundary := beginProgress(writer, "local-demo", DeploymentStarted, DeploymentFinished, DeploymentFailed)
		defer func() { boundary.finish(result) }()
		defer func() { cleaned = true; result = errors.New("cleanup failed") }()
		if len(writer.events) != 1 {
			t.Fatal("premature deployment terminal event")
		}
		return nil
	}
	if operation() == nil || !cleaned || !reflect.DeepEqual(writer.events, want) {
		t.Fatal("cleanup failure reported success")
	}
}

func TestTenantProgressReportsTransitionsAndBoundedHeartbeat(t *testing.T) {
	writer := &recordedProgress{}
	start := time.Unix(1000, 0)
	p := tenantProgress{writer: writer, started: start, deadline: start.Add(time.Minute)}
	p.update(start, "tenant-kubeconfig", nil)
	p.update(start.Add(10*time.Second), "tenant-kubeconfig", nil)
	p.update(start.Add(30*time.Second), "tenant-kubeconfig", nil)
	nodes := readyTenantFixture()
	nodes[0].Status.Conditions[0].Status = "False"
	p.update(start.Add(31*time.Second), "tenant-nodes", nodes[:1])
	p.update(start.Add(32*time.Second), "tenant-nodes", nodes)
	p.update(start.Add(33*time.Second), "tenant-nodes", nodes)
	if len(writer.events) != 4 {
		t.Fatalf("expected initial wait, heartbeat and two transitions: %+v", writer.events)
	}
	for i, event := range writer.events {
		wantRegistered, wantReady := 0, 0
		if i == 2 {
			wantRegistered = 1
		}
		if i == 3 {
			wantRegistered, wantReady = 2, 1
		}
		if event.Kind != TenantWaiting || event.RegisteredNodes != wantRegistered || event.ReadyNodes != wantReady || event.Elapsed+event.Remaining != time.Minute {
			t.Fatalf("wrong interpreted observation: %+v", event)
		}
	}
}

func TestTenantPollingKeepsPrivateLogWithoutSuccessSpam(t *testing.T) {
	writer := &recordedProgress{}
	work := privateTestDir(t)
	tenant := tenant{session: ManagementSession{Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}, Progress: writer}}}
	raw, err := tenant.capture(t.Context(), []string{"/bin/sh", "-c", "printf private-response; exit 1"}, "kubeconfig-secret", time.Second)
	if CommandExitCode(err) != 1 || string(raw) != "private-response" || len(writer.events) != 0 || writer.Len() != 0 {
		t.Fatalf("polling output escaped interpretation: events=%+v error=%v", writer.events, err)
	}
	if _, err := os.Stat(filepath.Join(work, "tenant-001-kubeconfig-secret.log")); err != nil {
		t.Fatal("private command diagnostics lost", err)
	}
}
