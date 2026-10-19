// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubPackageStagesRetainDependencySuccessGate(t *testing.T) {
	for _, scanner := range []bool{false, true} {
		name := "without scanner"
		if scanner {
			name = "with pinned scanner"
		}
		t.Run(name, func(t *testing.T) {
			assertGitHubPackageStageGates(t, githubStageOutcomePlan(t, scanner))
		})
	}
}

func githubStageOutcomePlan(t *testing.T, scanner bool) *Plan {
	t.Helper()
	plan := referenceTestPlan(t)
	if !scanner {
		return plan
	}
	return recompileGitHubStabilityPlan(t, plan, func(inputs *githubStabilityInputs) {
		inputs.definition.SBOMTool = "syft"
		digest := "sha256:" + strings.Repeat("3", 64)
		inputs.catalog.Tools["syft"] = catalog.ToolPin{Format: ToolFormatRaw, URL: "https://example.invalid/syft",
			Platform: "linux/amd64", ExecutablePath: "syft", ArchiveDigest: digest, ExecutableDigest: digest}
	})
}

func assertGitHubPackageStageGates(t *testing.T, plan *Plan) {
	t.Helper()
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	workflow := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	finalizer := workflow.Jobs["nvkr-lifecycle-run"]
	packages, observations := 0, 0
	for _, stage := range stages {
		job := workflow.Jobs[stage.ID]
		switch stage.Nodes[0].Kind {
		case NodeVerify:
			observations++
			if len(stage.Nodes) != 2 || stage.Nodes[1].Kind != NodeSBOM ||
				stage.Nodes[0].BuildTarget != stage.Nodes[1].BuildTarget {
				t.Fatalf("target observation lost its independent SBOM pair: %+v", stage)
			}
			if job.If != "${{ always() && "+githubExecutionCondition+" }}" {
				t.Fatalf("observation job no longer reports unsuccessful dependencies: %q", job.If)
			}
			if plan.Definition.SBOMTool != "" {
				tool := "tool/" + plan.Definition.SBOMTool
				if slices.Contains(stage.Nodes[0].Inputs, tool) || !slices.Contains(stage.Nodes[1].Inputs, tool) ||
					!slices.Contains(job.Needs, nodeJobs[tool]) {
					t.Fatal("SBOM-only scanner prerequisite was lost when its observation joined the stage")
				}
				_, artifacts := planIndex(plan)
				inputs, err := stage.carrierInputs(plan, artifacts, nodeJobs)
				if err != nil || !slices.Contains(inputs, tool) {
					t.Fatalf("paired stage omits its pinned scanner carrier: %v / %v", inputs, err)
				}
			}
		case NodePackage:
			packages++
			if len(stage.Nodes) != 1 {
				t.Fatalf("package shares another operation's failure boundary: %+v", stage)
			}
			// No status function overrides GitHub's implicit success() gate.
			if job.If != "${{ "+githubExecutionCondition+" }}" {
				t.Fatalf("package bypasses dependency success: %q", job.If)
			}
			if !slices.Contains(finalizer.Needs, stage.ID) {
				t.Fatalf("finalizer omits independent package job %q", stage.ID)
			}
		}
	}
	if packages == 0 || observations != len(plan.Builds) {
		t.Fatalf("fixture did not exercise packaging and every target: packages=%d observations=%d", packages, observations)
	}
	for _, output := range plan.LifecycleOutputs {
		packageJob := nodeJobs["package/"+output.Name]
		observationJob := nodeJobs[output.Name+"/terminal-observe"]
		if packageJob == "" || observationJob == "" || packageJob == observationJob ||
			!slices.Contains(workflow.Jobs[packageJob].Needs, observationJob) {
			t.Fatalf("output %q lost its separate package dependency on observation", output.Name)
		}
	}
}

func TestGitHubFinalizerRequiresIndependentPackageSuccess(t *testing.T) {
	plan := githubStageOutcomePlan(t, true)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	workflow := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	_, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	packageNode := "package/" + plan.LifecycleOutputs[0].Name
	scannerNode := "tool/" + plan.Definition.SBOMTool
	packageJob := nodeJobs[packageNode]
	if packageJob == "" || !slices.Contains(workflow.Jobs["nvkr-lifecycle-run"].Needs, packageJob) {
		t.Fatal("fixture omits the independent output package from finalizer dependencies")
	}
	carriers := 0
	for _, artifact := range plan.Graph.Artifacts {
		if finalizerArtifact(plan, artifact) {
			carriers++
			if artifact.Producer == packageNode || artifact.Producer == scannerNode {
				t.Fatal("fixture must exercise required package/scanner jobs whose payloads are not restored by the metadata-only finalizer")
			}
		}
	}
	if carriers == 0 {
		t.Fatal("fixture has no finalizer carriers")
	}
	for _, test := range []struct {
		name, node, status string
		recoveryCode       e.Code
		finalizerCode      e.Code
	}{
		{name: "success", node: packageNode, status: "success"},
		{name: "package failure", node: packageNode, status: "failure", finalizerCode: e.CodeProvenanceMissing},
		{name: "package skipped", node: packageNode, status: "skipped", finalizerCode: e.CodeProvenanceMissing},
		{name: "package missing", node: packageNode, recoveryCode: e.CodeInvalidCatalog, finalizerCode: e.CodeProvenanceMissing},
		{name: "scanner failure", node: scannerNode, status: "failure", finalizerCode: e.CodeProvenanceMissing},
		{name: "scanner skipped", node: scannerNode, status: "skipped", finalizerCode: e.CodeProvenanceMissing},
		{name: "scanner missing", node: scannerNode, recoveryCode: e.CodeInvalidCatalog, finalizerCode: e.CodeProvenanceMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			results := map[string]any{}
			for _, job := range workflow.Jobs["nvkr-lifecycle-run"].Needs {
				results[job] = map[string]string{"result": "success"}
			}
			job := nodeJobs[test.node]
			if job == "" || results[job] == nil {
				t.Fatalf("finalizer omits required job for %q", test.node)
			}
			if test.status == "" {
				delete(results, job)
			} else {
				results[job] = map[string]string{"result": test.status}
			}
			restored := 0
			// Successful fake transport isolates the provider-job closure gate;
			// these descriptions establish no external artifact authenticity.
			err := recoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, results),
				GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard},
				func(context.Context, Artifact) (string, error) {
					restored++
					return "restored:success", nil
				}, writeGitHubRestoreOutcome)
			if test.recoveryCode != "" {
				assertFactoryErrorCode(t, err, test.recoveryCode)
				entries, readErr := os.ReadDir(".")
				if readErr != nil || len(entries) != 0 || restored != 0 {
					t.Fatalf("incomplete provider results performed recovery: entries=%v restored=%d error=%v", entries, restored, readErr)
				}
			} else if err != nil || restored != carriers {
				t.Fatalf("metadata carrier recovery: restored=%d want=%d error=%v", restored, carriers, err)
			}
			err = ValidateHostedRestores(t.Context(), ".nvkr/factory", plan)
			if test.finalizerCode != "" {
				assertFactoryErrorCode(t, err, test.finalizerCode)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
