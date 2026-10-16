// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubPrivateTransfersExposeOnlyLocatorsToActions(t *testing.T) {
	for _, fixture := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"production", githubTestPlan}, {"development", githubDevelopmentTestPlan},
		{"signed development", githubDevelopmentPublicationTestPlan},
		{"runtime Plan", githubRuntimePlanFixture},
		{"ARC ARM", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/arm64") }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plan := fixture.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			actions := testActionRefs(t, plan)
			documents, err := githubCIActionDocuments(plan, bundle.Files[bundle.WorkflowPath], bundle.Files)
			if err != nil {
				t.Fatal(err)
			}
			uploads, downloads, builds := 0, 0, 0
			for name, document := range documents {
				if name != bundle.WorkflowPath && githubYAMLContains(document, "${{ secrets.") {
					t.Fatalf("composite %s reaches a caller-only secrets context", name)
				}
				if err := githubCIWalkSteps(document, func(step *yamlv3.Node) error {
					uses := githubYAMLValue(step, "uses")
					with := githubYAMLField(step, "with")
					switch uses {
					case actions.Upload:
						uploads++
						if name != githubCITransferPath(plan, "upload")+"/action.yml" || githubYAMLValue(with, "path") != "${{ steps.store.outputs.locator_path }}" || githubYAMLValue(step, "if") != githubCIIndexCondition {
							t.Fatalf("provider upload in %s can expose bytes or fall back when private storage refuses", name)
						}
					case actions.Download:
						downloads++
						if name != githubCITransferPath(plan, "download")+"/action.yml" && name != githubCITransferPath(plan, "bootstrap-download")+"/action.yml" {
							t.Fatalf("provider payload download remains in %s", name)
						}
						if githubYAMLValue(with, "path") != "${{ steps.directory.outputs.path }}" {
							t.Fatal("locator download does not use a fresh directory")
						}
					case actions.SetupGo:
						if name == githubCITransferPath(plan, "bootstrap-download")+"/action.yml" {
							builds++
							if githubYAMLValue(with, "cache") != "false" || githubYAMLValue(with, "go-version-file") != ".go-version" {
								t.Fatal("private helper bootstrap does not provision the pinned toolchain")
							}
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				// Only the two fixed transfer shell commands receive storage
				// credentials; producer/source/build commands retain their scopes.
				var visit func(*yamlv3.Node)
				visit = func(node *yamlv3.Node) {
					if node == nil {
						return
					}
					if env := githubYAMLField(node, "env"); env != nil && githubYAMLField(env, "NVKR_CI_STORAGE_TOKEN") != nil {
						run := githubYAMLValue(node, "run")
						if (!strings.Contains(run, " factory github ci-upload ") && !strings.Contains(run, " factory github ci-download ")) || (githubYAMLValue(env, "NVKR_CI_STORAGE_TOKEN") != githubCICredential(githubCIStorageInput, false) && githubYAMLValue(env, "NVKR_CI_STORAGE_TOKEN") != githubCICredential(githubCIReadInput, false)) {
							t.Fatalf("storage token escapes fixed transfer commands in %s", name)
						}
					}
					for _, child := range node.Content {
						visit(child)
					}
				}
				visit(document)
			}
			if uploads != 1 || downloads != 2 || builds != 1 {
				t.Fatalf("closed transport template membership changed: uploads=%d downloads=%d bootstraps=%d", uploads, downloads, builds)
			}
			actual := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			basePermissions, err := githubCIBasePermissions(plan)
			if err != nil {
				t.Fatal(err)
			}
			for id, job := range actual.Jobs {
				transfer, err := githubCIJobTransfers(plan, githubTestYAMLField(t, documents[bundle.WorkflowPath].Content[0], "jobs", id), documents, map[string]bool{})
				if err != nil {
					t.Fatal(err)
				}
				if !maps.Equal(job.Permissions, githubCITransferPermissions(basePermissions[id], transfer)) {
					t.Fatalf("job %s elevated repository token permissions for transport", id)
				}
			}
			if bytes.Contains(bundle.Files[bundle.WorkflowPath], []byte("|| github.token")) {
				t.Fatal("private transport falls back to repository credentials")
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatal("private projection changed logical graph authority", err)
			}
		})
	}
}

func TestGitHubPrivateUploadsPreserveCancellationAndFailureOrdering(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := githubCIActionDocuments(plan, bundle.Files[bundle.WorkflowPath], bundle.Files)
	if err != nil {
		t.Fatal(err)
	}
	template := documents[githubCITransferPath(plan, "upload")+"/action.yml"].Content[0]
	if githubYAMLValue(githubTestYAMLField(t, template, "inputs", githubCIAlwaysInput), "default") != "false" {
		t.Fatal("ordinary private uploads must remain cancellable by default")
	}
	steps := githubTestYAMLField(t, template, "runs", "steps")
	for _, stage := range []struct{ id, condition string }{
		{"directory", githubCIUploadCondition},
		{"store", githubCIStoreCondition},
		{"index", githubCIIndexCondition},
	} {
		step := githubTestYAMLStep(t, steps, "id", stage.id)
		if githubYAMLValue(step, "if") != stage.condition {
			t.Fatalf("private upload %s lost cancellation or predecessor-success admission", stage.id)
		}
	}
	ordinary, always := 0, 0
	for name, document := range documents {
		if name == githubCITransferPath(plan, "upload")+"/action.yml" {
			continue
		}
		if err := githubCIWalkSteps(document, func(step *yamlv3.Node) error {
			if githubCITransferMode(plan, githubYAMLValue(step, "uses")) != "upload" {
				return nil
			}
			flag := githubYAMLValue(githubYAMLField(step, "with"), githubCIAlwaysInput)
			if githubYAMLValue(step, "if") == githubAlwaysCondition {
				always++
				if flag != "true" {
					t.Fatalf("always-run upload in %s can be skipped during cancellation", name)
				}
			} else {
				ordinary++
				if flag != "" {
					t.Fatalf("ordinary upload in %s became uncancellable", name)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if ordinary == 0 || always == 0 {
		t.Fatal("fixture must exercise ordinary and always-run private uploads")
	}
	_, bootstrapMember := githubTestBootstrap(t, bundle)
	for _, mutation := range []struct {
		name, member string
		edit         func(*yamlv3.Node)
	}{
		{"ordinary upload made uncancellable", bootstrapMember, func(root *yamlv3.Node) {
			steps := githubTestYAMLField(t, root, "runs", "steps")
			step := githubTestYAMLStep(t, steps, "uses", "./"+githubCITransferPath(plan, "upload"))
			githubYAMLSet(githubYAMLField(step, "with"), githubCIAlwaysInput, githubYAMLString("true"))
		}},
		{"missing always binding", bundle.WorkflowPath, func(root *yamlv3.Node) {
			steps := githubTestYAMLField(t, root, "jobs", githubFinalizerJobID, "steps")
			step := githubTestYAMLStep(t, steps, "id", "upload-run-record")
			githubYAMLRemove(githubYAMLField(step, "with"), githubCIAlwaysInput)
		}},
		{"arbitrary always expression", bundle.WorkflowPath, func(root *yamlv3.Node) {
			steps := githubTestYAMLField(t, root, "jobs", githubFinalizerJobID, "steps")
			step := githubTestYAMLStep(t, steps, "id", "upload-run-record")
			githubYAMLSet(githubYAMLField(step, "with"), githubCIAlwaysInput, githubYAMLString("${{ github.event.inputs.always }}"))
		}},
		{"lost directory cancellation", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "directory")
			githubYAMLRemove(step, "if")
		}},
		{"store after directory failure", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "store")
			githubYAMLSet(step, "if", githubYAMLString(githubAlwaysCondition))
		}},
		{"locator after storage failure", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "index")
			githubYAMLSet(step, "if", githubYAMLString(githubAlwaysCondition))
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, mutation.member, mutation.edit)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}

func TestGitHubPrivateTransfersRejectPayloadFallbackAndCredentialTampering(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, _ := githubTestBootstrap(t, bundle)
	for _, test := range []struct {
		name, member string
		mutate       func(*yamlv3.Node)
	}{
		{"upload original bytes", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "index")
			githubTestYAMLField(t, step, "with", "path").Value = "${{ inputs.path }}"
		}},
		{"public fallback", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "store")
			githubTestYAMLField(t, step, "run").Value += " || true"
		}},
		{"cache bootstrap", githubCITransferPath(plan, "bootstrap-download") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "uses", plan.Actions["setup-go"].Uses())
			githubTestYAMLField(t, step, "with", "cache").Value = "true"
		}},
		{"credential scope", bundle.WorkflowPath, func(root *yamlv3.Node) {
			job := githubTestYAMLField(t, root, "jobs", bootstrap)
			step := githubTestYAMLStep(t, githubTestYAMLField(t, job, "steps"), "name", "Prepare NVKR runner (ubuntu-24.04)")
			githubTestYAMLField(t, step, "with", githubCIStorageInput).Value = "${{ secrets.UNRELATED }}"
		}},
		{"elevated package scope", bundle.WorkflowPath, func(root *yamlv3.Node) {
			githubYAMLSet(githubTestYAMLField(t, root, "jobs", bootstrap), "permissions", githubYAMLMap("contents", githubYAMLString("read"), "packages", githubYAMLString("write")))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, test.member, test.mutate)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
	for _, mode := range []string{"upload", "download", "bootstrap-download"} {
		t.Run("missing "+mode, func(t *testing.T) {
			changed := *bundle
			changed.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				if name != githubCITransferPath(plan, mode)+"/action.yml" {
					changed.Files[name] = raw
				}
			}
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
		})
	}
}

func TestGitHubPrivateTransferProjectionRemainsStableAcrossSourcePins(t *testing.T) {
	before := githubRuntimePlanFixture(t)
	after := recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) {
		for name, component := range in.catalog.Components {
			if component.FromSource {
				prior := component.Commit
				component.Commit = nextGitHubStabilityPin(t, component.Commit)
				component.TreeHash = nextGitHubStabilityPin(t, component.TreeHash)
				component.SourceDate++
				for key, value := range component.VersionStamp {
					component.VersionStamp[key] = strings.ReplaceAll(value, prior, component.Commit)
				}
				in.catalog.Components[name] = component
			}
		}
	})
	first, err := RenderGitHub(t.Context(), before)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderGitHub(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range first.Files {
		if name != first.PlanPath && name != githubActionDataPathFor(before) && !bytes.Equal(raw, second.Files[name]) {
			t.Fatalf("source pin update changed private transport implementation %s", name)
		}
	}
}
