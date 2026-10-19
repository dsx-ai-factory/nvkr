// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package progress

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCommandResultSnapshotsDetachAllCollections(t *testing.T) {
	input := Snapshot{Mode: "command", Fields: []Field{{Label: "digest", Value: "original"}}, Items: []Item{{ID: "item", Fields: []Field{{Label: "name", Value: "original"}}, Dependencies: []string{"parent"}}}}
	monitor := New(input)
	input.Fields[0].Value = "changed"
	input.Items[0].Fields[0].Value = "changed"
	input.Items[0].Dependencies[0] = "changed"
	first := monitor.Snapshot()
	if first.Fields[0].Value != "original" || first.Items[0].Fields[0].Value != "original" || first.Items[0].Dependencies[0] != "parent" {
		t.Fatal("monitor retained caller collections")
	}
	first.Fields[0].Value = "changed"
	first.Items[0].Fields[0].Value = "changed"
	first.Items[0].Dependencies[0] = "changed"
	next := monitor.Snapshot()
	if next.Fields[0].Value != "original" || next.Items[0].Fields[0].Value != "original" || next.Items[0].Dependencies[0] != "parent" {
		t.Fatal("readers share mutable command collections")
	}
}

func TestMonitorConcurrentCoalescingAndDefensiveSnapshots(t *testing.T) {
	const count = 700 // a display cap must not silently omit admitted operations
	initial := Snapshot{Attempt: "attempt", PlanDigest: "plan", Status: "pending"}
	for index := range count {
		initial.Operations = append(initial.Operations, Operation{ID: fmt.Sprint(index), Kind: "build", State: "queued", Outputs: []string{"first"}, Dependencies: []string{"shared"}})
	}
	initial.Outputs = []Output{{Name: "first", State: "required", Dependencies: []string{"second"}, Operations: []string{"0"}}, {Name: "second", State: "required"}}
	m := New(initial)
	initial.Operations[0].Outputs[0] = "substitution"
	m.Start("local")
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for range 100 {
			_ = m.Snapshot()
		}
	}()
	var workers sync.WaitGroup
	for index := range count {
		workers.Go(func() {
			id := fmt.Sprint(index)
			m.SetOperation(id, "running", "")
			m.SetOperation(id, "completed", "")
		})
	}
	workers.Wait() // no wakeup receiver or renderer has run
	<-readerDone
	snapshot := m.Snapshot()
	if len(snapshot.Operations) != count || len(snapshot.Activity) != MaxActivity || snapshot.Dropped == 0 {
		t.Fatalf("unbounded/lost state: operations=%d, activity=%d dropped=%d", len(snapshot.Operations), len(snapshot.Activity), snapshot.Dropped)
	}
	for _, operation := range snapshot.Operations {
		if operation.State != "completed" || operation.StartedAt.IsZero() {
			t.Fatalf("operation not complete: %+v", operation)
		}
	}
	snapshot.Operations[0].Outputs[0] = "changed"
	snapshot.Operations[0].Dependencies[0] = "changed"
	snapshot.Outputs[0].Operations[0] = "changed"
	snapshot.Outputs[0].Dependencies[0] = "changed"
	snapshot.Activity[0].Text = "changed"
	next := m.Snapshot()
	if next.Operations[0].Outputs[0] != "first" || next.Operations[0].Dependencies[0] != "shared" || next.Outputs[0].Operations[0] != "0" || next.Outputs[0].Dependencies[0] != "second" || next.Activity[0].Text == "changed" {
		t.Fatal("snapshot aliases monitor or input")
	}
	select {
	case <-m.Updates():
	default:
		t.Fatal("missing coalesced wakeup")
	}
}

func TestMonitorFinalResultOverridesPartialTelemetry(t *testing.T) {
	for _, test := range []struct {
		name, status    string
		observed        []string
		code            string
		record, cleanup bool
	}{
		{name: "failed Run clears earlier observation", status: "failed", code: string(e.CodeVerifyFailed), record: true},
		{name: "retained complete with cleanup error", status: "complete", observed: []string{"first", "second"}, code: string(e.CodeImmutableViolation), record: true, cleanup: true},
		{name: "dry run", status: "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := New(Snapshot{Outputs: []Output{{Name: "first", State: "required"}, {Name: "second", State: "required"}}})
			m.SetOutput("first", "observed", "observation")
			m.Finish(test.status, "static-unqualified", test.observed, test.code, test.record, test.cleanup)
			m.SetOutput("second", "observed", "observation")
			got := m.Snapshot()
			if got.Status != test.status || got.FailureCode != test.code || got.RecordAvailable != test.record || got.CleanupIncomplete != test.cleanup {
				t.Fatalf("final pair lost: %+v", got)
			}
			for _, output := range got.Outputs {
				want := "required"
				if len(test.observed) > 0 {
					want = "observed"
				}
				if output.State != want {
					t.Fatalf("latest telemetry replaced final result: %+v", output)
				}
			}
		})
	}
}

func TestMonitorFailedRunDoesNotInventSiblingObservationFailures(t *testing.T) {
	m := New(Snapshot{Outputs: []Output{
		{Name: "bad", State: "required"},
		{Name: "previous", State: "required"},
		{Name: "queued", State: "required"},
	}, Operations: []Operation{
		{ID: "causal", State: "queued", Outputs: []string{"bad"}},
		{ID: "sibling", State: "queued", Outputs: []string{"previous"}},
	}})
	m.SetOutput("previous", "observed", "observation")
	m.SetOutput("bad", "failed", "observation")
	m.SetOperation("causal", "failed", string(e.CodeVerifyFailed))
	m.SetOperation("sibling", "canceled", string(e.CodeTimeout))
	m.Finish("failed", "static-unqualified", nil, string(e.CodeVerifyFailed), true, false)
	got := m.Snapshot()
	want := []string{"failed", "required", "required"}
	if len(got.Outputs) != len(want) {
		t.Fatal("failure changed frozen denominator")
	}
	for index, output := range got.Outputs {
		if output.State != want[index] {
			t.Fatalf("output %q final state=%q want=%q", output.Name, output.State, want[index])
		}
	}
	if got.Operations[0].State != "failed" || got.Operations[1].State != "canceled" {
		t.Fatal("final reconciliation lost causal operation or canceled sibling")
	}
}

func TestMonitorClosedVocabularyAndClock(t *testing.T) {
	m := New(Snapshot{Operations: []Operation{{ID: "known", State: "queued"}}, Outputs: []Output{{Name: "out", State: "required"}}})
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	m.Start("local")
	m.SetOperation("known", "running", "")
	at = at.Add(time.Second)
	m.SetOperation("known", "failed", "secret-provider-message")
	m.SetOperation("secret-provider-message", "running", "")
	m.SetOutput("out", "secret-provider-message", "observation")
	m.SetPhase("secret-provider-message")
	got := m.Snapshot()
	if got.Operations[0].Code != string(e.CodeInternal) || got.Operations[0].UpdatedAt.Sub(got.StartedAt) != time.Second || got.Outputs[0].State != "required" || got.Dropped != 1 {
		t.Fatalf("unsafe/incorrect observation: %+v", got)
	}
	for _, activity := range got.Activity {
		if strings.Contains(activity.Text, "secret") {
			t.Fatal("unsafe activity")
		}
	}
	var absent *Monitor
	absent.Start("local")
	absent.SetPhase("execution")
	absent.SetOperation("x", "running", "")
	absent.SetOutput("x", "observed", "observation")
	absent.Finish("failed", "", nil, "", false, false)
	if absent.Updates() != nil || absent.Matches("", "") {
		t.Fatal("nil monitor is not a no-op")
	}
}
