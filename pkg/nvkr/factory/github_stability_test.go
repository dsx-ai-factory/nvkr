// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type githubStabilityInputs struct {
	catalog          *catalog.Catalog
	definition       authored.Definition
	environment      authored.Environment
	generatorVersion string
}

func recompileGitHubStabilityPlan(t *testing.T, before *Plan, mutate func(*githubStabilityInputs)) *Plan {
	t.Helper()
	copy, err := catalog.ApplyStack(before.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	inputs := githubStabilityInputs{catalog: copy.Catalog, definition: before.Definition.Clone(),
		environment: before.Environment.Clone(), generatorVersion: before.GeneratorVersion}
	mutate(&inputs)
	stack, err := catalog.ApplyStack(inputs.catalog)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Compile(t.Context(), inputs.definition, inputs.environment, stack, inputs.generatorVersion)
	if err != nil {
		t.Fatal(err)
	}
	after.DefinitionPath, after.EnvironmentPath = before.DefinitionPath, before.EnvironmentPath
	return after
}

// Rotate a hex digit bijectively so distinct pins remain distinct and every
// member of a shared source cohort receives the same replacement identity.
func nextGitHubStabilityPin(t *testing.T, value string) string {
	t.Helper()
	index := 0
	if marker := strings.LastIndex(value, "sha256:"); marker >= 0 {
		index = marker + len("sha256:")
	}
	const digits = "0123456789abcdef"
	if index >= len(value) || !strings.ContainsRune(digits, rune(value[index])) {
		t.Fatalf("fixture pin is not hexadecimal: %q", value)
	}
	next := digits[(strings.IndexByte(digits, value[index])+1)%len(digits)]
	return value[:index] + string(next) + value[index+1:]
}

func TestGitHubWorkflowPinUpdateBoundaries(t *testing.T) {
	for _, test := range []struct {
		name         string
		fixture      func(*testing.T) *Plan
		mutate       func(*testing.T, *githubStabilityInputs)
		wantActions  bool
		wantData     bool
		wantWorkflow bool
		spliceCode   e.Code
		spliceMatch  func(string, []byte) bool
	}{
		{
			name: "source commits and trees", fixture: githubTestPlan, wantData: true,
			mutate: func(t *testing.T, inputs *githubStabilityInputs) {
				for name, component := range inputs.catalog.Components {
					if !component.FromSource {
						continue
					}
					previous := component.Commit
					component.Commit = nextGitHubStabilityPin(t, component.Commit)
					component.TreeHash = nextGitHubStabilityPin(t, component.TreeHash)
					component.SourceDate++
					for key, value := range component.VersionStamp {
						component.VersionStamp[key] = strings.ReplaceAll(value, previous, component.Commit)
					}
					inputs.catalog.Components[name] = component
				}
			},
		},
		{
			name: "artifact digest", fixture: func(t *testing.T) *Plan { return buildTransportPlan(t, "binary") },
			mutate: func(t *testing.T, inputs *githubStabilityInputs) {
				component := inputs.catalog.Components["containerd"]
				component.Prebuilt.Digests["amd64"] = nextGitHubStabilityPin(t, component.Prebuilt.Digests["amd64"])
				inputs.catalog.Components["containerd"] = component
			},
		},
		{
			name: "toolchain digests", fixture: githubTestPlan,
			mutate: func(t *testing.T, inputs *githubStabilityInputs) {
				for name, component := range inputs.catalog.Components {
					if !component.FromSource {
						continue
					}
					if component.ToolchainImage != "" {
						component.ToolchainImage = nextGitHubStabilityPin(t, component.ToolchainImage)
					}
					for arch, image := range component.ToolchainImages {
						component.ToolchainImages[arch] = nextGitHubStabilityPin(t, image)
					}
					inputs.catalog.Components[name] = component
				}
			},
		},
		{
			name: "release tool pins", fixture: func(t *testing.T) *Plan { return signedTestPlan(t, cosignSigningProfile(), ghcrTarget()) },
			mutate: func(t *testing.T, inputs *githubStabilityInputs) {
				pin := inputs.catalog.Tools["cosign"]
				pin.ArchiveDigest = nextGitHubStabilityPin(t, pin.ArchiveDigest)
				pin.ExecutableDigest = nextGitHubStabilityPin(t, pin.ExecutableDigest)
				inputs.catalog.Tools["cosign"] = pin
			},
		},
		{
			// The retained catalog pin changes Plan identity but no longer
			// participates in generated private GHCR transfer execution.
			name: "download action pin", fixture: githubTestPlan,
			mutate: func(t *testing.T, inputs *githubStabilityInputs) {
				pin := inputs.catalog.Actions["download-artifact"]
				pin.SHA = nextGitHubStabilityPin(t, pin.SHA)
				inputs.catalog.Actions["download-artifact"] = pin
			},
		},
		{
			// Actions uploads remain only as direct, metadata-only exceptions;
			// their pins do not change the private transfer implementation.
			name: "summary-referenced upload action pin", fixture: githubTestPlan, wantWorkflow: true,
			mutate: func(t *testing.T, inputs *githubStabilityInputs) {
				pin := inputs.catalog.Actions["upload-artifact"]
				pin.SHA = nextGitHubStabilityPin(t, pin.SHA)
				inputs.catalog.Actions["upload-artifact"] = pin
			},
		},
		{
			name: "generator version", fixture: githubTestPlan, wantActions: true, spliceCode: e.CodeInvalidCatalog,
			mutate: func(_ *testing.T, inputs *githubStabilityInputs) {
				inputs.generatorVersion = "v1.2.4 (feedface)"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforePlan := test.fixture(t)
			before, err := RenderGitHub(t.Context(), beforePlan)
			if err != nil {
				t.Fatal(err)
			}
			afterPlan := recompileGitHubStabilityPlan(t, beforePlan, func(inputs *githubStabilityInputs) { test.mutate(t, inputs) })
			after, err := RenderGitHub(t.Context(), afterPlan)
			if err != nil {
				t.Fatal(err)
			}
			workflowChanged := !bytes.Equal(before.Files[GitHubWorkflowPath], after.Files[GitHubWorkflowPath])
			if workflowChanged != test.wantWorkflow || (before.WorkflowDigest != after.WorkflowDigest) != test.wantWorkflow {
				t.Fatalf("pin update changed the top-level workflow = %t, want %t", workflowChanged, test.wantWorkflow)
			}
			if test.wantWorkflow {
				// The closing summary consumes this upload's outputs and outcome,
				// so its pinned external action must remain in the caller's scope.
				want := bytes.ReplaceAll(before.Files[GitHubWorkflowPath], []byte(beforePlan.Actions["upload-artifact"].Uses()), []byte(afterPlan.Actions["upload-artifact"].Uses()))
				if !bytes.Equal(want, after.Files[GitHubWorkflowPath]) {
					t.Fatal("upload pin update changed more than the visible pinned action")
				}
			}
			if !slices.Equal(sortedFileNames(before.Files), sortedFileNames(after.Files)) {
				t.Fatal("routine pin update changed generated member paths")
			}
			if before.PlanDigest == after.PlanDigest || bytes.Equal(before.Files[FactoryPlanPath], after.Files[FactoryPlanPath]) {
				t.Fatal("updated exact inputs did not change the authoritative Plan")
			}
			_, beforeManifest, err := ManifestFor(before)
			if err != nil {
				t.Fatal(err)
			}
			manifest, afterManifest, err := ManifestFor(after)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(beforeManifest, afterManifest) || manifest.PlanDigest != after.PlanDigest || manifest.WorkflowDigest != after.WorkflowDigest {
				t.Fatal("manifest does not bind the new Plan to its exact workflow")
			}
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), afterPlan, before), e.CodePipelineDrift)
			changedActions := 0
			spliceAction := ""
			for _, name := range sortedFileNames(after.Files) {
				if !strings.HasPrefix(name, githubActionsDirectory) || bytes.Equal(before.Files[name], after.Files[name]) {
					continue
				}
				changedActions++
				if spliceAction == "" && (test.spliceMatch == nil || test.spliceMatch(name, after.Files[name])) {
					spliceAction = name
				}
			}
			if (changedActions > 0) != test.wantActions {
				t.Fatalf("shared action implementation changed = %t, want %t", changedActions > 0, test.wantActions)
			}
			if test.wantActions {
				if spliceAction == "" {
					t.Fatal("fixture has no changed action matching the semantic splice guard")
				}
				spliced := *after
				spliced.Files = make(map[string][]byte, len(after.Files))
				for path, raw := range after.Files {
					spliced.Files[path] = raw
				}
				spliced.Files[spliceAction] = before.Files[spliceAction]
				assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), afterPlan, &spliced), test.spliceCode)
			}
			changedData := !bytes.Equal(before.Files[GitHubActionDataPath], after.Files[GitHubActionDataPath])
			if test.wantData && !changedData {
				t.Fatal("updated exact source selectors were not regenerated into action data")
			}
			if changedData {
				spliced := *after
				spliced.Files = make(map[string][]byte, len(after.Files))
				for path, raw := range after.Files {
					spliced.Files[path] = raw
				}
				spliced.Files[GitHubActionDataPath] = before.Files[GitHubActionDataPath]
				assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), afterPlan, &spliced), e.CodeEngineCapability)
			}
		})
	}
}

func TestGitHubWorkflowChangesWhenSchedulingAuthorityChanges(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*githubStabilityInputs)
		fixture func(*testing.T) *Plan
	}{
		{
			name: "checkout action pin", fixture: githubTestPlan,
			mutate: func(inputs *githubStabilityInputs) {
				pin := inputs.catalog.Actions["checkout"]
				pin.SHA = strings.Repeat("b", 40)
				inputs.catalog.Actions["checkout"] = pin
			},
		},
		{
			name: "runner", fixture: githubTestPlan,
			mutate: func(inputs *githubStabilityInputs) { inputs.environment.RunsOn = "ubuntu-22.04" },
		},
		{
			name: "required output", fixture: githubTestPlan,
			mutate: func(inputs *githubStabilityInputs) {
				output := inputs.definition.Outputs[0]
				output.Name = "worker-two"
				inputs.definition.Outputs = append(inputs.definition.Outputs, output)
			},
		},
		{
			name: "signing permissions", fixture: func(t *testing.T) *Plan { return signedTestPlan(t, cosignSigningProfile(), ghcrTarget()) },
			mutate: func(inputs *githubStabilityInputs) { inputs.environment.Signing = dryRunSigningProfile() },
		},
		{
			name: "split shared source cohort", fixture: githubTestPlan,
			mutate: func(inputs *githubStabilityInputs) {
				component := inputs.catalog.Components["kubeadm"]
				component.Commit = strings.Repeat("b", 40)
				component.TreeHash = strings.Repeat("c", 40)
				inputs.catalog.Components["kubeadm"] = component
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforePlan := test.fixture(t)
			before, err := RenderGitHub(t.Context(), beforePlan)
			if err != nil {
				t.Fatal(err)
			}
			afterPlan := recompileGitHubStabilityPlan(t, beforePlan, test.mutate)
			after, err := RenderGitHub(t.Context(), afterPlan)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before.Files[GitHubWorkflowPath], after.Files[GitHubWorkflowPath]) || before.WorkflowDigest == after.WorkflowDigest {
				t.Fatal("changed scheduling or permission authority is hidden from the top-level workflow")
			}
		})
	}
}
