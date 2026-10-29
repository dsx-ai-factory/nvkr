// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationprogress

import (
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestEventAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event Event
		valid bool
	}{
		{"stage", Event{Kind: KindStage, Stage: StageInputs, State: StateRunning}, true},
		{"item", Event{Kind: KindItem, Stage: StageSmoke, State: StateCompleted, Item: "tools/kubectl", Completed: 1, Total: 10}, true},
		{"unknown stage", Event{Kind: KindStage, Stage: "deploy", State: StateRunning}, false},
		{"unknown state", Event{Kind: KindStage, Stage: StageInputs, State: "passed"}, false},
		{"unknown kind", Event{Kind: "log", Stage: StageInputs, State: StateRunning}, false},
		{"stage item", Event{Kind: KindStage, Stage: StageInputs, State: StateRunning, Item: "private"}, false},
		{"path", Event{Kind: KindItem, Stage: StageInputs, State: StateRunning, Item: "/tmp/private", Total: 1}, false},
		{"traversal", Event{Kind: KindItem, Stage: StageInputs, State: StateRunning, Item: "tools/../private", Total: 1}, false},
		{"terminal control", Event{Kind: KindItem, Stage: StageInputs, State: StateRunning, Item: "tool\x1b[31m", Total: 1}, false},
		{"markup", Event{Kind: KindItem, Stage: StageInputs, State: StateRunning, Item: "[green]tool", Total: 1}, false},
		{"unknown count", Event{Kind: KindItem, Stage: StageInputs, State: StateRunning, Item: "tool"}, false},
		{"excess count", Event{Kind: KindItem, Stage: StageInputs, State: StateCompleted, Item: "tool", Completed: 2, Total: 1}, false},
		{"negative count", Event{Kind: KindStage, Stage: StageInputs, State: StateRunning, Completed: -1}, false},
		{"unbounded count", Event{Kind: KindStage, Stage: StageInputs, State: StateRunning, Total: MaxItems + 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.event.Validate()
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				raw, err := canonical.Marshal(tc.event)
				if err != nil {
					t.Fatal(err)
				}
				got, err := Decode(raw)
				if err != nil || got != tc.event {
					t.Fatalf("round trip: %+v %v", got, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("want usage, got %v", err)
			}
		})
	}
}

func TestDecodeRejectsNoncanonicalOrUnboundedEvents(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"stage","stage":"inputs","state":"running","secret":"private"}`,
		`{"kind":"stage","stage":"inputs","stage":"builder","state":"running"}`,
		`{"kind":"stage", "stage":"inputs","state":"running"}`,
		strings.Repeat("x", MaxRecordBytes),
		"",
	} {
		_, err := Decode([]byte(raw))
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
			t.Fatalf("want usage, got %v", err)
		}
	}
}
