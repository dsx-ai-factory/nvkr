// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// partitionedRun is one complete partitioned Plan with its admitted execution
// model. Provider behavior is simulated; these tests do not exercise GitHub.
type partitionedRun struct {
	plan       *Plan
	projection *githubNeedsProjection
	nodeJobs   map[string]string
	model      *executionGraph
	partitions []executionPartition
	jobs       []string
}

func partitionedRecoveryRun(t *testing.T) partitionedRun {
	t.Helper()
	plan := recompileGitHubStabilityPlan(t, referenceTestPlan(t), func(inputs *githubStabilityInputs) {
		inputs.environment.GitHubExecutionPartitions = true
		for index := len(inputs.definition.Outputs); index < 6; index++ {
			output := inputs.definition.Outputs[0]
			output.Name = fmt.Sprintf("worker-z%02d", index)
			inputs.definition.Outputs = append(inputs.definition.Outputs, output)
		}
	})
	if !PerOutputPublication(plan) {
		t.Fatal("fixture requires complete provider closure")
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	workers := 0
	for name := range bundle.Files {
		if githubPartitionWorkflowSuffix.MatchString(name) {
			workers++
		}
	}
	if workers < 2 {
		t.Fatalf("fixture requires several execution partitions, got %d", workers)
	}
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := githubExecutionModel(t.Context(), plan, projection)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := model.dependencies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	partitions, err := partitionGitHubJobs(t.Context(), dependencies, githubMaximumResultInputs)
	if err != nil {
		t.Fatal(err)
	}
	_, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := githubFinalizerJobs(plan, nodeJobs)
	if err != nil {
		t.Fatal(err)
	}
	return partitionedRun{plan, projection, nodeJobs, model, partitions, jobs}
}

// flat applies provider semantics over the complete original ancestry: an
// ordinary operation runs only when every transitive ancestor succeeded.
func (run partitionedRun) flat(injected map[string]executionOutcome, canceled map[string]bool) map[string]executionOutcome {
	outcomes := map[string]executionOutcome{}
	var ancestorsSucceeded func(string) bool
	ancestorsSucceeded = func(id string) bool {
		for _, parent := range run.model.operations[id].Needs {
			if outcomes[parent] != executionSucceeded || !ancestorsSucceeded(parent) {
				return false
			}
		}
		return true
	}
	for _, partition := range run.partitions {
		for _, id := range partition.Jobs {
			outcome := injected[id]
			if outcome == 0 {
				outcome = executionSucceeded
			}
			if run.model.operations[id].Condition == executionAfterSuccess {
				if canceled[id] {
					outcome = executionCanceled
				} else if !ancestorsSucceeded(id) {
					outcome = executionSkipped
				}
			}
			outcomes[id] = outcome
		}
	}
	return outcomes
}

// partitioned evaluates each operation from transported prerequisite results
// only, in partition order, as the generated worker guards do.
func (run partitionedRun) partitioned(t *testing.T, injected map[string]executionOutcome, canceled map[string]bool) map[string]executionOutcome {
	t.Helper()
	outcomes := map[string]executionOutcome{}
	for _, partition := range run.partitions {
		for _, id := range partition.Jobs {
			outcome := injected[id]
			if outcome == 0 {
				outcome = executionSucceeded
			}
			if run.model.operations[id].Condition == executionAfterSuccess {
				parents, err := run.model.prerequisiteResults(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				if canceled[id] {
					outcome = executionCanceled
				}
				for _, parent := range parents {
					if outcome != executionCanceled && outcomes[parent] != executionSucceeded {
						outcome = executionSkipped
					}
				}
			}
			outcomes[id] = outcome
		}
	}
	return outcomes
}

var githubOutcomeNames = map[executionOutcome]string{
	executionSucceeded: "success", executionFailed: "failure",
	executionCanceled: "cancelled", executionSkipped: "skipped", //nolint:misspell // GitHub result enum spelling.
}

// providerResults encodes the finalizer's provider description in the exact
// collector shape the partition caller forwards.
func (run partitionedRun) providerResults(t *testing.T, outcomes map[string]executionOutcome) string {
	t.Helper()
	overrides := map[string]string{}
	for id, outcome := range outcomes {
		overrides[id] = githubOutcomeNames[outcome]
	}
	if len(run.projection.Collectors) == 0 {
		leaves := map[string]any{}
		for _, id := range run.jobs {
			leaves[id] = map[string]string{"result": overrides[id]}
		}
		return carrierTestJSON(t, leaves)
	}
	return carrierTestJSON(t, collectorTestReports(t, run.projection.Collectors, overrides))
}

func TestGitHubPartitionedRunPreservesOperationOutcomesThroughFinalization(t *testing.T) {
	run := partitionedRecoveryRun(t)
	var producer, recovery, recoveredAncestor string
	for _, artifact := range run.plan.Graph.Artifacts {
		if finalizerArtifact(run.plan, artifact) && producer == "" {
			producer = run.nodeJobs[artifact.Producer]
		}
	}
	for _, id := range sortedKeys(run.model.operations) {
		operation := run.model.operations[id]
		if operation.Condition == executionAfterCompletion && len(operation.Needs) != 0 && recovery == "" {
			recovery, recoveredAncestor = id, operation.Needs[0]
		}
	}
	// An operation that restores no artifact. Reported verbatim with every
	// restoring producer successful, only the provider closure can refuse it.
	restoring := map[string]bool{}
	for _, artifact := range run.plan.Graph.Artifacts {
		if finalizerArtifact(run.plan, artifact) {
			restoring[run.nodeJobs[artifact.Producer]] = true
		}
	}
	var unrestored string
	for _, id := range run.jobs {
		if !restoring[id] && unrestored == "" {
			unrestored = id
		}
	}
	if producer == "" || recovery == "" || unrestored == "" {
		t.Fatalf("fixture requires a restored producer, a recovery and an unrestored operation: %q %q %q", producer, recovery, unrestored)
	}
	// Cancellation arrives after the first partition finished.
	canceledLater := map[string]bool{}
	for _, partition := range run.partitions[1:] {
		for _, id := range partition.Jobs {
			canceledLater[id] = true
		}
	}
	for _, test := range []struct {
		name     string
		injected map[string]executionOutcome
		canceled map[string]bool
		verbatim bool
	}{
		{name: "complete success"},
		{name: "producer failure", injected: map[string]executionOutcome{producer: executionFailed}},
		{name: "cancellation during execution", canceled: canceledLater},
		{name: "recovery succeeds after ancestor failure", injected: map[string]executionOutcome{recoveredAncestor: executionFailed}},
		{name: "recovery fails after ancestor failure", injected: map[string]executionOutcome{recoveredAncestor: executionFailed, recovery: executionFailed}},
		{name: "skipped descendants", injected: map[string]executionOutcome{run.partitions[0].Jobs[0]: executionSkipped}},
		{name: "inconsistent unrestored failure", injected: map[string]executionOutcome{unrestored: executionFailed}, verbatim: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := run.flat(test.injected, test.canceled)
			got := run.partitioned(t, test.injected, test.canceled)
			for _, id := range run.jobs {
				if test.verbatim {
					got[id] = max(executionSucceeded, test.injected[id])
				} else if got[id] == 0 || got[id] != want[id] {
					t.Fatalf("operation %s: partitioned=%v flat=%v", id, got[id], want[id])
				}
			}
			for id, outcome := range test.injected {
				if outcome == executionFailed {
					for descendant, observed := range want {
						if run.model.operations[descendant].Condition == executionAfterSuccess && run.hasAncestor(descendant, id) && observed != executionSkipped && observed != executionCanceled {
							t.Fatalf("descendant %s of failed %s did not stop: %v", descendant, id, observed)
						}
					}
				}
			}
			t.Chdir(t.TempDir())
			err := recoverGitHubArtifacts(t.Context(), run.plan, run.providerResults(t, got), GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard},
				func(context.Context, Artifact) (string, error) { return "restored:success", nil }, writeGitHubRestoreOutcome)
			if err != nil {
				t.Fatal(err)
			}
			complete := true
			for _, id := range run.jobs {
				complete = complete && got[id] == executionSucceeded
			}
			assertRestoreOutcome(t, "all-provider-jobs", map[bool]string{true: "restored:success", false: "job-failed"}[complete])
			for _, artifact := range run.plan.Graph.Artifacts {
				if finalizerArtifact(run.plan, artifact) {
					succeeded := got[run.nodeJobs[artifact.Producer]] == executionSucceeded
					assertRestoreOutcome(t, artifact.ID, map[bool]string{true: "restored:success", false: "job-failed"}[succeeded])
				}
			}
			err = ValidateHostedRestores(t.Context(), ".nvkr/factory", run.plan)
			if complete {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
		})
	}
}

func (run partitionedRun) hasAncestor(id, ancestor string) bool {
	for _, parent := range run.model.operations[id].Needs {
		if parent == ancestor || run.hasAncestor(parent, ancestor) {
			return true
		}
	}
	return false
}

func assertRestoreOutcome(t *testing.T, id, want string) {
	t.Helper()
	raw, err := os.ReadFile(HostedRestoreOutcomePath(".nvkr/factory", id))
	if err != nil || string(raw) != want+"\n" {
		t.Fatalf("outcome %s=%q want %q: %v", id, raw, want, err)
	}
}

// A worker that never reports yields JSON null for each member through the
// caller's fromJSON(... || '{}') projection, and an empty status entry.
func TestGitHubPartitionedRunRefusesIncompleteReportsBeforeRecovery(t *testing.T) {
	run := partitionedRecoveryRun(t)
	if len(run.projection.Collectors) == 0 {
		t.Fatal("fixture requires result collectors")
	}
	collector := sortedKeys(run.projection.Collectors)[0]
	member := run.projection.Collectors[collector][0]
	valid := func(t *testing.T) map[string]any { return collectorTestReports(t, run.projection.Collectors, nil) }
	setLeaves := func(reports map[string]any, leaves, statuses string) {
		outputs := reports[collector].(map[string]any)["outputs"].(map[string]string)
		outputs["results"] = leaves
		if statuses != "" {
			outputs["statuses"] = statuses
		}
	}
	leafReport := func(t *testing.T, mutate func(map[string]any)) string {
		leaves := map[string]any{}
		for _, id := range run.projection.Collectors[collector] {
			leaves[id] = map[string]string{"result": "success"}
		}
		mutate(leaves)
		return carrierTestJSON(t, leaves)
	}
	successes := strings.TrimSpace(strings.Repeat("success ", len(run.projection.Collectors[collector])))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		raw  func(*testing.T) string
		code e.Code
	}{
		{"partition never reported", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			setLeaves(reports, leafReport(t, func(leaves map[string]any) { leaves[member] = nil }), successes[:strings.LastIndexByte(successes, ' ')+1])
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"missing member", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			// Consistent statuses leave exact operation accounting as the only guard.
			setLeaves(reports, leafReport(t, func(leaves map[string]any) { delete(leaves, member) }), successes[:strings.LastIndexByte(successes, ' ')])
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"missing collector", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			delete(reports, collector)
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"duplicate member", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			leaves := leafReport(t, func(map[string]any) {})
			setLeaves(reports, strings.TrimSuffix(leaves, "}")+`,"`+member+`":{"result":"success"}}`, "")
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"member reported by foreign worker", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			other := sortedKeys(run.projection.Collectors)[len(run.projection.Collectors)-1]
			if other == collector {
				t.Skip("fixture has one collector")
			}
			outputs := reports[other].(map[string]any)["outputs"].(map[string]string)
			outputs["results"] = strings.TrimSuffix(outputs["results"], "}") + `,"` + member + `":{"result":"success"}}`
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"malformed member", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			setLeaves(reports, leafReport(t, func(leaves map[string]any) { leaves[member] = "success" }), "")
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"unsupported member result", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			setLeaves(reports, leafReport(t, func(leaves map[string]any) { leaves[member] = map[string]string{"result": "neutral"} }), "")
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"incomplete statuses", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			setLeaves(reports, leafReport(t, func(map[string]any) {}), successes+" ")
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"truncated description", t.Context(), func(t *testing.T) string {
			raw := carrierTestJSON(t, valid(t))
			return raw[:len(raw)/2]
		}, e.CodeInvalidCatalog},
		{"collector canceled", t.Context(), func(t *testing.T) string {
			reports := valid(t)
			reports[collector].(map[string]any)["result"] = "cancelled" //nolint:misspell // GitHub result enum spelling.
			return carrierTestJSON(t, reports)
		}, e.CodeInvalidCatalog},
		{"finalizer canceled", canceled, func(t *testing.T) string { return carrierTestJSON(t, valid(t)) }, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := test.raw(t)
			t.Chdir(t.TempDir())
			calls := 0
			err := recoverGitHubArtifacts(test.ctx, run.plan, raw, GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard},
				func(context.Context, Artifact) (string, error) {
					calls++
					return "restored:success", nil
				}, writeGitHubRestoreOutcome)
			assertFactoryErrorCode(t, err, test.code)
			if entries, readErr := os.ReadDir("."); readErr != nil || len(entries) != 0 || calls != 0 {
				t.Fatalf("incomplete report started recovery: %v / %v / %d", entries, readErr, calls)
			}
			assertFactoryErrorCode(t, ValidateHostedRestores(t.Context(), ".nvkr/factory", run.plan), e.CodeProvenanceMissing)
		})
	}
}
