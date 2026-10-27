// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package progress retains bounded descriptive Run activity. It neither issues
// receipts nor changes execution, artifact, or publication authority.
package progress

import (
	"slices"
	"sync"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MaxActivity bounds retained public activity, independently of the complete
// admitted output and operation tables. Eviction does not shrink membership.
const MaxActivity = 128

// Snapshot is descriptive presentation state. Owners must project safe labels
// before constructing it; this package does not sanitize arbitrary caller text
// or authenticate the represented Run, output bytes, or command result.
type Snapshot struct {
	Attempt, PlanDigest, Mode, Phase, Lane, Status, FailureCode string
	StartedAt                                                   time.Time
	Outputs                                                     []Output
	Operations                                                  []Operation
	Activity                                                    []Activity
	Dropped                                                     uint64
	RecordAvailable, CleanupIncomplete                          bool
	// Command descriptions are detached presentation data, never Run outputs.
	Title, Note string
	Fields      []Field
	Items       []Item
}

// Field is an owner-projected public label/value pair, excluding raw diagnostics
// and private transport or filesystem locations.
type Field struct{ Label, Value string }

// Item describes one result entry. ID is only a navigation key; neither it nor
// State, Fields, or Dependencies can restore an executable or verified receipt.
type Item struct {
	ID, Name, Kind, State string
	// Summary is optional compact public metadata for the list row. Owners
	// must also project its facts through Kind or Fields for narrow terminals.
	Summary string
	// Primary marks an overview entry. All entries remain descriptive and
	// available for search and exact dependency navigation.
	Primary      bool
	Fields       []Field
	Dependencies []string
}

// Output tracks one frozen named output independently of its producer operations.
// An observed state describes an owning update, not a payload-verification receipt.
type Output struct {
	Name, Kind, Platform, State, Stage string
	// RecordedDigest describes an inspected record, never current payload proof.
	RecordedDigest           string
	Dependencies, Operations []string
	UpdatedAt                time.Time
}

// Operation describes an admitted graph node and its exact navigation links.
// Producer completion alone cannot satisfy lifecycle output observation.
type Operation struct {
	ID, Kind, State, Code string
	Outputs, Dependencies []string
	StartedAt, UpdatedAt  time.Time
}

// Activity is a sequenced, closed public summary, never raw console bytes. Its
// timestamp and presence do not establish successful or healthy execution.
type Activity struct {
	Sequence uint64
	At       time.Time
	Text     string
}

// Monitor owns latest state for a fixed admitted graph and a bounded activity
// ring. Updates call no external code or I/O; a stalled renderer cannot hold its
// lock. Wakeups coalesce and must never be counted as completed work. This
// mutex-backed bookkeeping is not a lock-free execution guarantee.
type Monitor struct {
	mu         sync.Mutex
	state      Snapshot
	operations map[string]int
	outputs    map[string]int
	wake       chan struct{}
	now        func() time.Time
	sequence   uint64
	finished   bool
}

// New owns a defensive copy of an already projected description. The caller
// remains responsible for deriving labels from its admitted owning boundary.
// Its activity ring starts empty; only monitor updates populate that history.
// Descriptions, including a caller-created monitor, grant no authority.
func New(initial Snapshot) *Monitor {
	m := &Monitor{state: clone(initial), operations: make(map[string]int),
		outputs: make(map[string]int), wake: make(chan struct{}, 1), now: time.Now}
	m.state.Activity = nil
	for index, operation := range m.state.Operations {
		m.operations[operation.ID] = index
	}
	for index, output := range m.state.Outputs {
		m.outputs[output.Name] = index
	}
	return m
}

// Snapshot returns detached state, including nested field and navigation slices.
// Readers may mutate their copy without changing the monitor or other readers;
// a nil monitor returns an empty description.
func (m *Monitor) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.state)
}

// Updates is a best-effort wakeup, not an event or final-result channel. It is
// coalesced and never closed, avoiding races with late producer updates. Readers
// obtain current state through Snapshot; the session owns stop and joining.
func (m *Monitor) Updates() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.wake
}

// Matches checks a descriptive binding without inspecting or admitting a Plan.
// An empty attempt checks only the digest; a nil monitor never matches.
func (m *Monitor) Matches(attempt, digest string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.PlanDigest == digest && (attempt == "" || m.state.Attempt == attempt)
}

func (m *Monitor) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Monitor) activity(text string, at time.Time) {
	m.sequence++
	if len(m.state.Activity) == MaxActivity {
		copy(m.state.Activity, m.state.Activity[1:])
		m.state.Activity = m.state.Activity[:MaxActivity-1]
		m.state.Dropped++
	}
	m.state.Activity = append(m.state.Activity, Activity{Sequence: m.sequence, At: at, Text: text})
}

// Start timestamps the first local or dry-run admission description. Repeated
// starts, unknown modes, and updates after Finish are ignored; no work is admitted.
func (m *Monitor) Start(mode string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished || (mode != "local" && mode != "dry-run") {
		return
	}
	if !m.state.StartedAt.IsZero() {
		return
	}
	m.state.Mode, m.state.Phase = mode, "admission"
	m.state.StartedAt = m.now()
	m.activity("Run admission started.", m.state.StartedAt)
	m.notify()
}

// SetPhase records a known owner-reported phase. Unknown phases are ignored and
// phase changes neither start operations nor establish their completion.
func (m *Monitor) SetPhase(phase string) {
	if m == nil {
		return
	}
	switch phase {
	case "admission", "execution", "observation", "finalization", "inspection", "plan":
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished || m.state.Phase == phase {
		return
	}
	m.state.Phase = phase
	m.notify()
}

// SetOperation updates an existing node using closed state and error vocabularies.
// Terminal states are retained, unknown IDs count as dropped updates, and no
// update can add graph membership or mark a required output observed.
func (m *Monitor) SetOperation(id, state, code string) {
	if m == nil {
		return
	}
	switch state {
	case "queued", "running", "completed", "cached", "skipped", "failed", "canceled":
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return
	}
	index, ok := m.operations[id]
	if !ok {
		m.state.Dropped++
		m.notify()
		return
	}
	operation := &m.state.Operations[index]
	if operation.State == state {
		return
	}
	switch operation.State {
	case "completed", "cached", "skipped", "failed", "canceled":
		return
	}
	at := m.now()
	operation.State, operation.Code, operation.UpdatedAt = state, safeCode(code), at
	if state == "running" {
		operation.StartedAt = at
	}
	for _, name := range operation.Outputs {
		if outputIndex, known := m.outputs[name]; known {
			output := &m.state.Outputs[outputIndex]
			if output.State == "required" && m.state.Mode != "dry-run" && state != "skipped" && state != "queued" {
				output.Stage, output.UpdatedAt = "execution", at
			}
		}
	}
	// Only closed owner summaries enter the public ring, never a raw error or log.
	m.activity("Operation "+state+".", at)
	m.notify()
}

// SetOutput records a known observation state/stage for a frozen named output.
// Unknown names count as dropped updates; unknown states/stages are ignored.
// Observations remain provisional until Finish reconciles retained membership.
func (m *Monitor) SetOutput(name, state, stage string) {
	if m == nil {
		return
	}
	switch state {
	case "required", "observing", "observed", "failed":
	default:
		return
	}
	switch stage {
	case "", "execution", "observation", "finalization", "inspection":
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return
	}
	index, ok := m.outputs[name]
	if !ok {
		m.state.Dropped++
		m.notify()
		return
	}
	output := &m.state.Outputs[index]
	if output.State == state && output.Stage == stage {
		return
	}
	output.State, output.Stage, output.UpdatedAt = state, stage, m.now()
	m.activity("Output "+state+".", output.UpdatedAt)
	m.notify()
}

// Finish reconciles latest observations from the actual returned Run and error.
// It is independent of wakeup delivery. A complete retained Run and a nonempty
// error code may coexist, including an incomplete disk commit cleanup. The first
// final reconciliation wins and suppresses later telemetry; callers must already
// have joined their operations. These inputs remain descriptions, not receipts.
func (m *Monitor) Finish(status, lane string, observed []string, code string, recordAvailable, cleanupIncomplete bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return
	}
	switch status {
	case "pending", "complete", "failed", "unavailable":
	default:
		status = "failed"
	}
	m.state.Status, m.state.FailureCode = status, safeCode(code)
	if lane != "" {
		m.state.Lane = lane
	}
	m.state.RecordAvailable, m.state.CleanupIncomplete = recordAvailable, cleanupIncomplete
	m.state.Phase = "finalization"
	at := m.now()
	for index := range m.state.Outputs {
		output := &m.state.Outputs[index]
		observationFailed := output.State == "failed"
		output.State, output.Stage, output.UpdatedAt = "required", "finalization", at
		// A failed Run clears retained membership, not every output's own
		// observation outcome. Preserve an actual failed observation; previously
		// observed or never-started siblings have no invented individual failure.
		if status == "failed" && observationFailed {
			output.State = "failed"
		}
	}
	for _, name := range observed {
		if index, ok := m.outputs[name]; ok {
			m.state.Outputs[index].State = "observed"
		} else {
			m.state.Dropped++
		}
	}
	// Operations never started after a failure are still queued descriptions.
	// A running operation at final reconciliation has been joined by its owner.
	for index := range m.state.Operations {
		operation := &m.state.Operations[index]
		if operation.State == "running" {
			operation.State, operation.UpdatedAt = "canceled", at
		}
	}
	m.finished = true
	m.activity("Run outcome received.", at)
	m.notify()
}

func safeCode(code string) string {
	if code == "" {
		return ""
	}
	for _, known := range e.AllCodes {
		if code == string(known) {
			return code
		}
	}
	return string(e.CodeInternal)
}

// clone detaches every nested collection used by presentation and navigation.
func clone(source Snapshot) Snapshot {
	result := source
	result.Fields = slices.Clone(source.Fields)
	result.Items = slices.Clone(source.Items)
	for index := range result.Items {
		result.Items[index].Fields = slices.Clone(source.Items[index].Fields)
		result.Items[index].Dependencies = slices.Clone(source.Items[index].Dependencies)
	}
	result.Outputs = slices.Clone(source.Outputs)
	for index := range result.Outputs {
		result.Outputs[index].Dependencies = slices.Clone(source.Outputs[index].Dependencies)
		result.Outputs[index].Operations = slices.Clone(source.Outputs[index].Operations)
	}
	result.Operations = slices.Clone(source.Operations)
	for index := range result.Operations {
		result.Operations[index].Outputs = slices.Clone(source.Operations[index].Outputs)
		result.Operations[index].Dependencies = slices.Clone(source.Operations[index].Dependencies)
	}
	result.Activity = slices.Clone(source.Activity)
	return result
}
