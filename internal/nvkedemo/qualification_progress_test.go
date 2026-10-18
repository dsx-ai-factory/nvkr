// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
)

func TestRunnerQualificationProgressIsPrivateAndJoined(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "failed", "invalid", "no opt in", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			presenter := &buildActivityPresenter{}
			runner := Runner{Env: []string{"NVKR_QUALIFICATION_PROGRESS_HELPER=" + mode}, Progress: presenter}
			args := []string{program, "-test.run=TestQualificationProgressHelper", "--"}
			var raw []byte
			var err error
			if mode == "no opt in" {
				raw, err = runner.Capture(t.Context(), args, "qualify-local", 5*time.Second)
			} else {
				timeout := 5 * time.Second
				if mode == "cancel" {
					timeout = 300 * time.Millisecond
				}
				raw, err = runner.CaptureQualification(t.Context(), args, timeout)
			}
			if mode == "failed" {
				if CommandExitCode(err) != 17 {
					t.Fatalf("telemetry overrode failed child: %v", err)
				}
			} else if mode == "cancel" {
				if err == nil || CommandExitCode(err) != -1 {
					t.Fatalf("cancellation lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if string(raw) != "{\"result\":\"canonical\"}\n" {
				t.Fatalf("telemetry contaminated result: %q", raw)
			}
			presenter.mu.Lock()
			defer presenter.mu.Unlock()
			var progress []qualificationprogress.Event
			for _, event := range presenter.events {
				if event.Kind == QualificationProgress {
					progress = append(progress, event.Qualification)
				}
				raw, marshalErr := json.Marshal(event)
				if marshalErr != nil || strings.Contains(string(raw), "private-sentinel") {
					t.Fatalf("private diagnostics exposed: %s %v", raw, marshalErr)
				}
			}
			if mode == "invalid" || mode == "no opt in" {
				if len(progress) != 0 {
					t.Fatalf("untrusted stream forged typed progress: %+v", progress)
				}
			} else if len(progress) != 2 || progress[0].State != qualificationprogress.StateRunning || progress[1].State != qualificationprogress.StateCompleted {
				t.Fatalf("ordered final progress lost: %+v", progress)
			}
			last := presenter.events[len(presenter.events)-1]
			wantKind := PhaseFinished
			if mode == "failed" || mode == "cancel" {
				wantKind = PhaseFailed
			}
			if last.Kind != wantKind {
				t.Fatalf("child boundary lost: %+v", last)
			}
		})
	}
}

func TestQualificationProgressHelper(t *testing.T) {
	mode := os.Getenv("NVKR_QUALIFICATION_PROGRESS_HELPER")
	if mode == "" {
		return
	}
	_, _ = os.Stdout.WriteString("{\"result\":\"canonical\"}\n")
	event := qualificationprogress.Event{Kind: qualificationprogress.KindStage, Stage: qualificationprogress.StageReadback, State: qualificationprogress.StateCompleted}
	raw, err := canonical.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	// Even valid-looking JSON in stderr must not become a typed observation.
	_, _ = os.Stderr.Write(append(raw, '\n'))
	_, _ = os.Stderr.WriteString("private-sentinel\x1b[2J\n")
	if mode == "no opt in" {
		os.Exit(0)
	}
	if mode == "invalid" {
		pipe := os.NewFile(3, "private-metadata")
		_, _ = pipe.WriteString(strings.Repeat("x", qualificationprogress.MaxRecordBytes*2) + "\n")
		_, _ = pipe.WriteString(strings.TrimSuffix(string(raw), "}") + `,"private":"private-sentinel"}` + "\n")
		_, _ = pipe.WriteString("private-sentinel\x1b[2J\n")
		_ = pipe.Close()
		os.Exit(0)
	}
	sink, err := qualificationprogress.AdoptFD(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	event.State = qualificationprogress.StateRunning
	sink.Offer(event)
	event.State = qualificationprogress.StateCompleted
	sink.Offer(event)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if mode == "cancel" {
		time.Sleep(time.Hour)
	}
	if mode == "failed" {
		os.Exit(17)
	}
	os.Exit(0)
}
