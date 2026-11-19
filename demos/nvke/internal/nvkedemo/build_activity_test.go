// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
)

type buildActivityPresenter struct {
	mu     sync.Mutex
	events []ProgressEvent
}

func (*buildActivityPresenter) Write(raw []byte) (int, error) { return len(raw), nil }
func (p *buildActivityPresenter) Report(event ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func TestRunnerOwnsBuildActivityPipe(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, mode string
		opted      bool
		timeout    time.Duration
	}{
		{"opted in", "valid", true, 5 * time.Second}, {"no opt in", "none", false, 5 * time.Second},
		{"malformed private stream", "invalid", true, 5 * time.Second}, {"canceled joins pipe", "cancel", true, time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			presenter := &buildActivityPresenter{}
			runner := Runner{Env: []string{"NVKR_BUILD_ACTIVITY_HELPER=" + test.mode}, Progress: presenter, buildActivity: test.opted}
			err := runner.Run(t.Context(), []string{program, "-test.run=TestRunnerActivityHelper", "--"}, "build-all", test.timeout)
			if test.mode == "cancel" {
				if err == nil || CommandExitCode(err) != -1 {
					t.Fatalf("cancel lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			presenter.mu.Lock()
			defer presenter.mu.Unlock()
			found := false
			for _, event := range presenter.events {
				if event.Kind == OSBuildLogs && event.BuildLogs.Available {
					found = true
					if event.BuildLogs.Operation != "os/build-node" || event.BuildLogs.Bytes != 2048 {
						t.Fatalf("metadata changed: %+v", event)
					}
				}
				if strings.Contains(event.Name, "private-sentinel") {
					t.Fatal("private pipe content leaked")
				}
			}
			if found != (test.mode == "valid" || test.mode == "cancel") {
				t.Fatalf("events=%+v", presenter.events)
			}
		})
	}
}

func TestRunnerActivityHelper(t *testing.T) {
	mode := os.Getenv("NVKR_BUILD_ACTIVITY_HELPER")
	if mode == "" {
		return
	}
	if mode == "none" {
		return
	}
	sink, err := buildactivity.AdoptFD(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if mode == "invalid" {
		// Arbitrary stdout markers cannot impersonate a typed pipe record.
		_, _ = os.Stdout.WriteString("OSBuildLogs private-sentinel bytes=9000\n")
		sink.Offer(buildactivity.Record{Version: 1, Operation: "private-sentinel\x1b", Available: true, Bytes: 9000})
		return
	}
	sink.Offer(buildactivity.Record{Version: 1, Operation: "os/build-node", Available: true, Files: 1, Bytes: 2048})
	if mode == "cancel" {
		time.Sleep(time.Hour)
	} else {
		time.Sleep(500 * time.Millisecond)
	}
}
