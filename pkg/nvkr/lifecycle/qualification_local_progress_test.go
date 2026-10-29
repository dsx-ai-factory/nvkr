// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalQualificationProgressTracksObservedBoundaries(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "failed"}[failed], func(t *testing.T) {
			var events []qualificationprogress.Event
			ctx, progress := withLocalQualificationProgress(t.Context(), func(event qualificationprogress.Event) { events = append(events, event) })
			progress.start(qualificationprogress.StageInputs)
			localProgress(ctx).startItem("cli", 0, 1)
			if failed {
				progress.fail()
			} else {
				progress.finishItem()
				progress.finish()
			}
			states := []string{}
			for _, event := range events {
				if err := event.Validate(); err != nil {
					t.Fatal(err)
				}
				states = append(states, event.Kind+"/"+event.State)
			}
			terminal := qualificationprogress.StateCompleted
			if failed {
				terminal = qualificationprogress.StateFailed
			}
			if !slices.Equal(states, []string{"stage/running", "item/running", "item/" + terminal, "stage/" + terminal}) {
				t.Fatal(states)
			}
			if failed && events[len(events)-1].Completed != 0 {
				t.Fatal("failure counted unfinished item")
			}
		})
	}
}

func TestLocalQualificationProgressObserverPanicDoesNotEscape(t *testing.T) {
	for _, observer := range []func(qualificationprogress.Event){nil, func(qualificationprogress.Event) { panic("presentation failed") }} {
		_, progress := withLocalQualificationProgress(t.Context(), observer)
		progress.start(qualificationprogress.StageSmoke)
		progress.startItem("cli/kubectl", 0, 1)
		progress.finishItem()
		progress.finish()
		progress.fail()
		if progress.completed != 1 {
			t.Fatal("observer interrupted lifecycle bookkeeping")
		}
	}
}

func TestLocalQualificationProgressAdmissionHasNoEvents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options LocalQualificationOptions
		code    e.Code
	}{
		{"invalid inputs", LocalQualificationOptions{}, e.CodeUsage},
		{"unsupported platform", LocalQualificationOptions{Workspace: "/private/workspace", PolicyPath: "/private/policy", BuilderKeyPath: "/private/builder", SmokeKeyPath: "/private/smoke", QualifierKeyPath: "/private/qualifier", CosignExecutable: "/private/cosign", Repository: "localhost:5000/test", Scratch: "/private/scratch", EvidenceOut: "/private/evidence"}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			tc.options.Progress = func(qualificationprogress.Event) { called = true }
			_, err := qualifyLocalRunWith(t.Context(), tc.options, "darwin/arm64", runLocalToolSmoke)
			assertCode(t, err, tc.code)
			if called {
				t.Fatal("admission refusal emitted qualification progress")
			}
		})
	}
}
