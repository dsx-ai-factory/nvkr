// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func runtimeTransportPlan(t *testing.T, plan *Plan) *Plan {
	t.Helper()
	plan.Environment.GitHubBundle = "runtime-transport"
	plan.Environment.GitHubRuntimePlan = true
	var err error
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func runtimeTransportBootstraps(t *testing.T, plan *Plan) map[string]string {
	t.Helper()
	jobs := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		projected, err := plan.GitHubExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		jobs[projected.RunsOn] = "build-runner-" + shortStableName(projected.RunsOn)
	}
	return jobs
}

func runtimeTransportMember(t *testing.T, bundle *GeneratedBundle, jobID string) string {
	t.Helper()
	job := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Jobs[jobID]
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "./") && strings.Contains(step.Uses, "/bootstrap-") {
			return strings.TrimPrefix(step.Uses, "./") + "/action.yml"
		}
	}
	t.Fatalf("job %s has no bootstrap action", jobID)
	return ""
}

func TestGitHubRuntimeTransportCompilesOnceAndRestoresEveryHelper(t *testing.T) {
	for _, fixture := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"native platforms", func(t *testing.T) *Plan {
			return nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
		}},
		{"native ARM KVM", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/arm64") }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plan := runtimeTransportPlan(t, fixture.plan(t))
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			if !reflect.DeepEqual(document.Permissions, map[string]string{"contents": "read"}) || document.Concurrency.CancelInProgress {
				t.Fatal("runtime transport changed root authority or production cancellation")
			}
			bootstrap := runtimeTransportBootstraps(t, plan)
			first := bootstrap[sortedKeys(bootstrap)[0]]
			paths := []string{bundle.PlanPath, path.Join(path.Dir(bundle.PlanPath), "action-data.json"), bundle.ManifestPath}
			compileCount, runtimeUploads, runnerUploads, secondaryDownloads := 0, 0, 0, 0
			for runner, id := range bootstrap {
				job := document.Jobs[id]
				if job.RunsOn != githubBootstrapRunner(runner) || len(job.Permissions) != 0 || job.Environment != "" || job.TimeoutMinutes != githubSupportJobTimeoutMinutes {
					t.Fatalf("bootstrap changed native route or privilege scope: %s %+v", id, job)
				}
				if id == first {
					if len(job.Needs) != 0 || !strings.Contains(joinRuns(job.Steps), githubRuntimeGenerationCommand(plan)) {
						t.Fatal("first bootstrap does not compile exactly before dependent work")
					}
				} else if !slices.Equal(job.Needs, []string{first}) || !strings.Contains(joinRuns(job.Steps), generationCommand(plan, EngineGitHub)) {
					t.Fatalf("secondary bootstrap omits the planner dependency or strict check: %s", id)
				}
				for index, step := range job.Steps {
					if step.Uses == plan.Actions["checkout"].Uses() && !reflect.DeepEqual(step.With, map[string]string{"persist-credentials": "false"}) {
						t.Fatal("bootstrap retained checkout credentials")
					}
					if strings.Contains(step.Run, " --runtime") {
						compileCount++
					}
					switch step.Name {
					case "Upload NVKR runtime inputs":
						runtimeUploads++
						want := map[string]string{"name": "nvkr-runtime-inputs", "path": strings.Join(paths, "\n") + "\n", "include-hidden-files": "true", "if-no-files-found": "error", "retention-days": "7"}
						if id != first || step.Uses != plan.Actions["upload-artifact"].Uses() || !reflect.DeepEqual(step.With, want) || index == 0 || job.Steps[index-1].ID != "validate-catalog" {
							t.Fatalf("planner does not upload exactly the checked flat three-file closure: %+v", step)
						}
					case "Download NVKR runtime inputs":
						secondaryDownloads++
						want := map[string]string{"name": "nvkr-runtime-inputs", "path": path.Dir(bundle.PlanPath)}
						if id == first || step.Uses != plan.Actions["download-artifact"].Uses() || !reflect.DeepEqual(step.With, want) || index+1 == len(job.Steps) || job.Steps[index+1].ID != "validate-catalog" {
							t.Fatal("secondary bootstrap does not restore current-Run inputs before checking")
						}
					case "Upload NVKR runner":
						runnerUploads++
						want := map[string]string{"name": "nvkr-exec-" + shortStableName(runner), "path": "nvkr-exec\n" + strings.Join(paths, "\n") + "\n", "include-hidden-files": "true", "if-no-files-found": "error", "retention-days": "7"}
						if step.Uses != plan.Actions["upload-artifact"].Uses() || !reflect.DeepEqual(step.With, want) {
							t.Fatalf("native helper omits the exact root-relative runtime closure: %+v", step)
						}
					}
				}
			}
			if compileCount != 1 || runtimeUploads != 1 || runnerUploads != len(bootstrap) || secondaryDownloads != len(bootstrap)-1 {
				t.Fatalf("runtime transport owners: compile=%d inputs=%d helpers=%d secondary=%d", compileCount, runtimeUploads, runnerUploads, secondaryDownloads)
			}
			// Turning off runtime compilation must preserve every execution,
			// credential, native route and finalizer step after helper restoration.
			ordinary := *plan
			ordinary.Environment = plan.Environment.Clone()
			ordinary.Environment.GitHubRuntimePlan = false
			ordinary.EnvironmentDigest, err = ordinary.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			oldBundle, err := RenderGitHub(t.Context(), &ordinary)
			if err != nil {
				t.Fatal(err)
			}
			old := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, oldBundle))
			helperConsumers := 0
			for id, job := range document.Jobs {
				if strings.HasPrefix(id, "build-runner-") {
					continue
				}
				previous := old.Jobs[id]
				job.Needs, previous.Needs = nil, nil // The admitted planner edge can reduce existing transitive edges.
				normalized := job
				normalized.Steps = slices.Clone(job.Steps)
				for index, step := range normalized.Steps {
					switch step.Name {
					case "Download NVKR runner":
						step.With = maps.Clone(step.With)
						step.With["path"] = "."
					case prepareHelperStepName:
						step.Run = prepareHelperRun
					}
					normalized.Steps[index] = step
				}
				if !reflect.DeepEqual(normalized, previous) {
					t.Fatalf("runtime transport changed execution or finalization job %s", id)
				}
				found := false
				for index, step := range job.Steps {
					if step.Name == "Download NVKR runner" {
						found = true
						if step.Uses != plan.Actions["download-artifact"].Uses() || len(step.With) != 2 || step.With["path"] != ".nvkr/runtime-runner" || !strings.HasPrefix(step.With["name"], "nvkr-exec-") || index+1 == len(job.Steps) || strings.TrimSpace(job.Steps[index+1].Run) != prepareHelperRunFor(plan) {
							t.Fatalf("job %s does not restore its current-Run helper and runtime closure before execution", id)
						}
					}
				}
				if !found {
					t.Fatalf("job %s omits helper/runtime restoration", id)
				}
				helperConsumers++
			}
			if helperConsumers == 0 || document.Jobs[githubFinalizerJobID].Name == "" {
				t.Fatal("fixture must cover execution and lifecycle finalization")
			}
		})
	}
}

func TestGitHubRuntimeTransportRejectsMissingOrSwappedCarrierContracts(t *testing.T) {
	plan := runtimeTransportPlan(t, nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"}))
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := runtimeTransportBootstraps(t, plan)
	runners := sortedKeys(bootstrap)
	first, second := bootstrap[runners[0]], bootstrap[runners[1]]
	firstMember, secondMember := runtimeTransportMember(t, bundle, first), runtimeTransportMember(t, bundle, second)
	for _, test := range []struct {
		name, member, step, key, value string
		code                           e.Code
	}{
		{"missing Plan", firstMember, "Upload NVKR runtime inputs", "path", path.Join(path.Dir(bundle.PlanPath), "action-data.json") + "\n" + bundle.ManifestPath + "\n", e.CodeEngineCapability},
		{"other namespace", firstMember, "Upload NVKR runtime inputs", "path", ".github/nvkr-run-other/plan.json\n.github/nvkr-run-other/action-data.json\n.github/nvkr-run-other/manifest.json\n", e.CodeEngineCapability},
		{"hidden files excluded", firstMember, "Upload NVKR runtime inputs", "include-hidden-files", "false", e.CodeEngineCapability},
		{"other carrier", secondMember, "Download NVKR runtime inputs", "name", "nvkr-runtime-inputs-other", e.CodeEngineCapability},
		{"wrong restore root", secondMember, "Download NVKR runtime inputs", "path", ".", e.CodeEngineCapability},
		{"cross-Run download", secondMember, "Download NVKR runtime inputs", "run-id", "1234", e.CodeEngineCapability},
		{"helper without Plan", firstMember, "Upload NVKR runner", "path", "nvkr-exec", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, test.member, func(root *yamlv3.Node) {
				step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "name", test.step)
				githubYAMLSet(githubTestYAMLField(t, step, "with"), test.key, githubYAMLString(test.value))
			})
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), test.code)
		})
	}
	t.Run("missing planner dependency", func(t *testing.T) {
		changed := mutateGitHubBundleYAML(t, bundle, bundle.WorkflowPath, func(root *yamlv3.Node) {
			githubYAMLRemove(githubTestYAMLField(t, root, "jobs", second), "needs")
		})
		assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeInvalidCatalog)
	})
}

func TestGitHubRuntimeTransportSourcePinUpdatePreservesProviderPrograms(t *testing.T) {
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
	root := t.TempDir()
	baseline := writeGitHubRuntimeBaseline(t, root, before)
	if err := PrepareGitHubRuntimeBundle(t.Context(), after, root, false); err != nil {
		t.Fatal(err)
	}
	fresh, err := RenderGitHub(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(t.Context(), root, fresh); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(baseline.Files[baseline.PlanPath], fresh.Files[fresh.PlanPath]) || bytes.Equal(baseline.Files[githubActionDataPathFor(before)], fresh.Files[githubActionDataPathFor(after)]) {
		t.Fatal("pin update did not refresh exact Plan and stage descriptor bytes")
	}
	for name, raw := range baseline.Files {
		if name != baseline.PlanPath && name != githubActionDataPathFor(before) && !bytes.Equal(raw, fresh.Files[name]) {
			t.Fatalf("pin update changed the scheduled provider program %s", name)
		}
	}
}

func TestGitHubRuntimeTransportOptOutAndCancellation(t *testing.T) {
	for _, fixture := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"default", githubTestPlan}, {"development", githubDevelopmentTestPlan},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plan := fixture.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			for id, job := range document.Jobs {
				if !strings.HasPrefix(id, "build-runner-") {
					continue
				}
				if len(job.Needs) != 0 || !strings.Contains(joinRuns(job.Steps), generationCommand(plan, EngineGitHub)) {
					t.Fatal("opt-out bootstrap changed its independent strict regeneration path")
				}
				for _, step := range job.Steps {
					if strings.Contains(step.Run, " --runtime") || step.With["name"] == "nvkr-runtime-inputs" || (step.Name == "Upload NVKR runner" && (step.With["path"] != "nvkr-exec" || step.With["include-hidden-files"] != "")) {
						t.Fatal("runtime transport leaked into an opt-out bundle")
					}
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	bundle, err := RenderGitHub(ctx, githubRuntimePlanFixture(t))
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	if bundle != nil {
		t.Fatal("canceled runtime rendering returned a partial bundle")
	}
}
