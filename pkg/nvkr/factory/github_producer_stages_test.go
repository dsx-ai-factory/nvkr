// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestGitHubProducerStagesKeepOnlyClosedOfflineConsumers(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]Node)
		joined bool
	}{
		{name: "different isolated utility image", joined: true},
		{name: "acquired producer", mutate: func(nodes []Node) { nodes[0].Kind = NodeAcquire }, joined: true},
		{name: "full producer timeout", mutate: func(nodes []Node) {
			nodes[0].Requirements = &execution.Requirements{TimeoutSeconds: 360 * 60}
		}},
		{name: "different runner", mutate: func(nodes []Node) { nodes[2].RunsOn = "ubuntu-24.04" }},
		{name: "different platform", mutate: func(nodes []Node) { nodes[2].Platform = "linux/arm64" }},
		{name: "different named output", mutate: func(nodes []Node) { nodes[2].BuildTarget = "other" }},
		{name: "cross-output join", mutate: func(nodes []Node) { nodes[2].Inputs = append(nodes[2].Inputs, "other-result") }},
		{name: "network-enabled consumer", mutate: func(nodes []Node) { nodes[2].Network = ir.NetworkDefault }},
		{name: "source credential boundary", mutate: func(nodes []Node) { nodes[2].Kind = NodeFetch }},
		{name: "publication credential boundary", mutate: func(nodes []Node) { nodes[2].Kind = NodePublish }},
		{name: "signer OIDC boundary", mutate: func(nodes []Node) { nodes[2].Kind = NodeSign }},
		{name: "SBOM attestation OIDC boundary", mutate: func(nodes []Node) { nodes[2].Kind = NodeAttestEvidence }},
		{name: "publication verifier boundary", mutate: func(nodes []Node) { nodes[2].Kind = NodeVerifyPublication }},
	} {
		t.Run(test.name, func(t *testing.T) {
			producer := Node{ID: "a-build", Kind: NodeBuild, BuildTarget: "output", RunsOn: "ubuntu-latest",
				Platform: "linux/amd64", Container: "producer@sha256:" + strings.Repeat("1", 64), Network: ir.NetworkNone,
				Outputs: []string{"result"}}
			other := producer
			other.ID, other.BuildTarget, other.Outputs = "m-other", "other", []string{"other-result"}
			utility := Node{ID: "z-verify", Kind: NodeVerify, BuildTarget: "output", RunsOn: producer.RunsOn,
				Platform: producer.Platform, Container: fixtureUtilityContainer, Network: ir.NetworkNone, Inputs: []string{"result"}}
			nodes := []Node{producer, other, utility}
			if test.mutate != nil {
				test.mutate(nodes)
			}
			plan := &Plan{Graph: ArtifactGraph{Nodes: nodes, Artifacts: []Artifact{
				{ID: "result", Producer: producer.ID}, {ID: "other-result", Producer: other.ID},
			}}}
			stages, jobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			if got := jobs[producer.ID] == jobs[utility.ID]; got != test.joined {
				t.Fatalf("consumer co-location=%t, want %t: %v", got, test.joined, jobs)
			}
			if jobs[other.ID] == jobs[producer.ID] {
				t.Fatal("independent output producer joined another producer job")
			}
			if test.joined {
				stage, ok := byNodeForJob(stages, jobs[producer.ID])
				if !ok || len(stage.Nodes) != 2 || stage.Nodes[0].ID != producer.ID || stage.Nodes[1].ID != utility.ID {
					t.Fatalf("producer or consumer execution order changed: %+v", stage)
				}
				minutes, err := githubStageTimeoutMinutes(stage)
				if err != nil || minutes != 120 {
					t.Fatalf("serial default budgets: minutes=%d error=%v", minutes, err)
				}
				if strings.Contains(stage.condition(plan), "always()") {
					t.Fatal("producer stage would start after a failed required dependency")
				}
			}
		})
	}
}

func TestGitHubProducerStagePreservesSignedWorkflowBoundaries(t *testing.T) {
	plan := githubSingleProducerPlan(t)
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
		t.Fatal(err)
	}
	stages, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	stage := requireGitHubProducerStage(t, stages)
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	job := document.Jobs[stage.ID]
	if job.Permissions["id-token"] != "" || job.Permissions["packages"] == "write" || job.Environment != "" ||
		job.Env["NVKR_CREDENTIAL"] != "" || job.Env[sourceTokenEnv] != "" {
		t.Fatalf("producer acquired signing or publication authority: %+v", job)
	}
	kinds := map[string]int{}
	var executed []string
	for _, step := range job.Steps {
		if step.ID != "" {
			executed = append(executed, step.ID)
			if step.ContinueOnError || step.If != "" {
				t.Fatal("a grouped member can continue after a preceding failure")
			}
		}
	}
	var expected []string
	for _, node := range stage.Nodes {
		kinds[node.Kind]++
		expected = append(expected, githubJobID(node.ID))
		for _, output := range node.Outputs {
			if !testActionRefs(t, plan).stepUploads(job.Steps, githubArtifactName(output), carrierPath(output)) {
				t.Fatalf("co-location dropped exact output carrier %q", output)
			}
		}
	}
	if kinds[NodeBuild] != 1 || kinds[NodeVerify] != 1 || kinds[NodeSBOM] != 1 || kinds[NodePackage] != 2 || !slices.Equal(executed, expected) {
		t.Fatalf("producer closure was not retained in execution order: kinds=%v execute=%v expected=%v", kinds, executed, expected)
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeFetch || node.Kind == NodePublish || signFamilyNode(node.Kind) {
			if jobs[node.ID] == stage.ID {
				t.Fatalf("credential boundary %q joined producer", node.ID)
			}
		}
	}
	if !strings.Contains(document.Jobs["nvkr-lifecycle-run"].If, "always()") {
		t.Fatal("finalization lost unconditional failure collection")
	}
	after, err := plan.Digest()
	if err != nil || before != after {
		t.Fatalf("scheduling mutated logical Plan identity: before=%s after=%s error=%v", before, after, err)
	}
}

func TestGitHubProducerStageBoundsSerialMembership(t *testing.T) {
	for _, count := range []int{16, 17} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			plan := &Plan{}
			for index := range count {
				id := fmt.Sprintf("node-%02d", index)
				node := Node{ID: id, Kind: NodePackage, BuildTarget: "output", RunsOn: "ubuntu-24.04",
					Platform: "linux/amd64", Container: fixtureUtilityContainer, Network: ir.NetworkNone,
					Requirements: &execution.Requirements{TimeoutSeconds: 60}, Outputs: []string{id + "-result"}}
				if index == 0 {
					node.Kind = NodeBuild
				} else {
					node.Inputs = []string{fmt.Sprintf("node-%02d-result", index-1)}
				}
				plan.Graph.Nodes = append(plan.Graph.Nodes, node)
				plan.Graph.Artifacts = append(plan.Graph.Artifacts, Artifact{ID: id + "-result", Producer: id})
			}
			stages, jobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			stage := requireGitHubProducerStage(t, stages)
			if len(stage.Nodes) != 16 || len(jobs) != count || len(stages) != count-15 {
				t.Fatalf("serial membership exceeded its cap or lost nodes: stages=%d producer=%d jobs=%d", len(stages), len(stage.Nodes), len(jobs))
			}
			if count == 17 && jobs["node-16"] == stage.ID {
				t.Fatal("overflow consumer lost its independent dependency boundary")
			}
		})
	}
}

func TestGitHubProducerStageFailureCannotAuthorizePartialOutputs(t *testing.T) {
	plan := githubSingleProducerPlan(t)
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	stage := requireGitHubProducerStage(t, stages)
	for _, status := range []string{"failure", "cancelled", "skipped"} { //nolint:misspell // GitHub result enum.
		t.Run(status, func(t *testing.T) {
			t.Chdir(t.TempDir())
			results := carrierTestResults(t, plan)
			results[stage.ID] = map[string]any{"result": status}
			var output bytes.Buffer
			err := recoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, results),
				GitHubStepOptions{Stdout: &output, Stderr: &output, SummaryPath: "summary.md"},
				func(context.Context, Artifact) (string, error) { return "restored:success", nil }, writeGitHubRestoreOutcome)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(HostedRestoreOutcomePath(".nvkr/factory", "all-provider-jobs"))
			if err != nil || string(raw) != "job-failed\n" {
				t.Fatalf("unsuccessful producer admitted partial carrier set: status=%q error=%v", raw, err)
			}
			assertFactoryErrorCode(t, ValidateHostedRestores(t.Context(), ".nvkr/factory", plan), e.CodeProvenanceMissing)
		})
	}
}

func githubSingleProducerPlan(t *testing.T) *Plan {
	t.Helper()
	original := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	definition := original.Definition.Clone()
	definition.Outputs[0].Components = []string{"kubectl"}
	environment := original.Environment.Clone()
	environment.Publication.Mode = "per-output-v1"
	environment.Publication.CredentialRef = "github:GITHUB_TOKEN"
	stack, err := catalog.ApplyStack(original.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = original.DefinitionPath, original.EnvironmentPath
	return plan
}

func requireGitHubProducerStage(t *testing.T, stages []githubStage) githubStage {
	t.Helper()
	for _, stage := range stages {
		if githubProducerStage(stage) && len(stage.Nodes) > 1 {
			return stage
		}
	}
	t.Fatal("fixture has no producer stage with offline consumers")
	return githubStage{}
}
