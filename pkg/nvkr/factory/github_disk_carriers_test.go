// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubDiskCarrierProjection(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := arcTestPlan(t, platform)
			stages, jobs, artifacts, err := admitGitHubCarriers(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			output := plan.LifecycleOutputs[0]
			wrapper := "package/" + output.Name
			var release githubStage
			for _, stage := range stages {
				if slices.Contains(stage.inputs(artifacts, jobs), output.ArtifactID) {
					release = stage
					break
				}
			}
			if release.ID == "" {
				t.Fatal("fixture requires an external raw disk dependency")
			}
			inputs := release.inputs(artifacts, jobs)
			carriers, err := release.carrierInputs(plan, artifacts, jobs)
			if err != nil {
				t.Fatal(err)
			}
			want := slices.DeleteFunc(slices.Clone(inputs), func(id string) bool { return id == output.ArtifactID })
			if !slices.Equal(carriers, want) || !slices.Contains(carriers, wrapper) {
				t.Fatalf("carrier projection changed another dependency: got %v want %v", carriers, want)
			}
			if !slices.Contains(release.needs(artifacts, jobs, "bootstrap"), jobs[artifacts[output.ArtifactID].Producer]) {
				t.Fatal("raw subject lost its producer job dependency")
			}
			actions, err := resolveGitHubActions(plan.Actions)
			if err != nil {
				t.Fatal(err)
			}
			var rendered bytes.Buffer
			if err := writeGitHubStageInputSteps(&rendered, plan, release, artifacts, jobs, actions); err != nil {
				t.Fatal(err)
			}
			steps := decodeGitHubSteps(t, rendered.Bytes())
			if strings.Contains(steps[0].With["pattern"], githubArtifactName(output.ArtifactID)) ||
				!strings.Contains(steps[0].With["pattern"], githubArtifactName(wrapper)) {
				t.Fatal("provider selection disagrees with carrier projection")
			}
			producer, consumer := t.TempDir(), t.TempDir()
			for _, id := range carriers {
				carrierTestArchive(t, producer, consumer, artifacts[id], id)
			}
			t.Chdir(consumer)
			if err := RestoreGitHubInputs(t.Context(), plan, release.ID, GitHubStepOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(artifacts[output.ArtifactID].Path); !os.IsNotExist(err) {
				t.Fatalf("raw disk was independently restored: %v", err)
			}
			for _, id := range carriers {
				if content, err := os.ReadFile(artifacts[id].Path); err != nil || string(content) != id {
					t.Fatalf("required carrier %s not restored: %v", id, err)
				}
			}
		})
	}
}

func TestGitHubRestoredCarriersHaveOneProducerUpload(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, mutation := range []string{"valid", "missing uploader", "duplicate uploader", "wrong producer"} {
			t.Run(platform+"/"+mutation, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				bundle, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
				stages, jobs, artifacts, err := admitGitHubCarriers(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				actions, err := resolveGitHubActions(plan.Actions)
				if err != nil {
					t.Fatal(err)
				}
				wrapper := diskPublicationWrapperID(plan.LifecycleOutputs[0])
				producer := jobs[artifacts[wrapper].Producer]
				job := document.Jobs[producer]
				found := false
				for i, step := range job.Steps {
					if step.Uses != actions.Upload || step.With["name"] != githubArtifactName(wrapper) {
						continue
					}
					found = true
					switch mutation {
					case "missing uploader":
						job.Steps = slices.Delete(job.Steps, i, i+1)
					case "duplicate uploader":
						job.Steps = append(job.Steps, step)
					case "wrong producer":
						job.Steps = slices.Delete(job.Steps, i, i+1)
						document.Jobs["not-producer"] = githubJob{Steps: []githubStep{step}}
					}
					break
				}
				if !found {
					t.Fatal("fixture does not upload the disk wrapper")
				}
				document.Jobs[producer] = job
				err = validateGitHubCarrierUploads(plan, document, stages, artifacts, jobs, actions)
				if mutation == "valid" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				}
			})
		}
	}
}

func TestGitHubDiskCarrierRequiresSelectedSubjectClosure(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, missing := range []string{"wrapper", "producer-result"} {
			t.Run(platform+"/"+missing, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				_, artifacts := planIndex(plan)
				output := plan.LifecycleOutputs[0]
				wrapper := diskPublicationWrapperID(output)
				result := diskPublicationProducerResultID(output, artifacts[output.ArtifactID])
				inputs := []string{output.ArtifactID, wrapper, result}
				omitted := wrapper
				if missing == "producer-result" {
					omitted = result
				}
				inputs = slices.DeleteFunc(inputs, func(id string) bool { return id == omitted })
				stage := githubStage{ID: "consumer", Nodes: []Node{{Inputs: inputs}}}
				jobs := map[string]string{}
				for _, id := range inputs {
					jobs[artifacts[id].Producer] = "disk-producer"
				}
				carriers, err := stage.carrierInputs(plan, artifacts, jobs)
				if carriers != nil || factoryErrorCode(err) != e.CodeEngineCapability {
					t.Fatalf("raw disk without selected %s: carriers=%v error=%v", missing, carriers, err)
				}
				actions, err := resolveGitHubActions(plan.Actions)
				if err != nil {
					t.Fatal(err)
				}
				var rendered bytes.Buffer
				err = writeGitHubStageInputSteps(&rendered, plan, stage, artifacts, jobs, actions)
				if factoryErrorCode(err) != e.CodeEngineCapability || rendered.Len() != 0 {
					t.Fatalf("rendered a rejected carrier selection: %s, %v", rendered.String(), err)
				}
			})
		}
	}
}

func TestGitHubDiskCarrierMissingWrapperFailsRestoration(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := arcTestPlan(t, platform)
			stages, jobs, artifacts, err := admitGitHubCarriers(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			output := plan.LifecycleOutputs[0]
			producer, consumer := t.TempDir(), t.TempDir()
			for _, stage := range stages {
				if !slices.Contains(stage.inputs(artifacts, jobs), output.ArtifactID) {
					continue
				}
				carriers, err := stage.carrierInputs(plan, artifacts, jobs)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range carriers {
					if id != "package/"+output.Name {
						carrierTestArchive(t, producer, consumer, artifacts[id], id)
					}
				}
				t.Chdir(consumer)
				if err := RestoreGitHubInputs(t.Context(), plan, stage.ID, GitHubStepOptions{}); factoryErrorCode(err) != e.CodeVerifyFailed {
					t.Fatalf("missing exact wrapper did not reject restoration: %v", err)
				}
				return
			}
			t.Fatal("fixture requires an external raw disk dependency")
		})
	}
}

// Complete Plan rederivation rejects missing edges before any carrier operation;
// the lower-level projection test separately covers its own coded error.
func TestGitHubDiskCarrierBoundariesRejectIncompleteGraph(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, missing := range []string{"wrapper", "producer-result"} {
			t.Run(platform+"/"+missing, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				bundle, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				_, artifacts := planIndex(plan)
				output := plan.LifecycleOutputs[0]
				omitted := diskPublicationWrapperID(output)
				wantCode := e.CodePipelineDrift
				if missing == "producer-result" {
					omitted = diskPublicationProducerResultID(output, artifacts[output.ArtifactID])
					wantCode = e.CodeProvenanceMissing
				}
				stages, jobs, err := githubStages(plan)
				if err != nil {
					t.Fatal(err)
				}
				stageID := ""
				for _, stage := range stages {
					if slices.Contains(stage.inputs(artifacts, jobs), output.ArtifactID) {
						stageID = stage.ID
					}
				}
				if stageID == "" {
					t.Fatal("fixture requires a raw disk consumer")
				}
				for i := range plan.Graph.Nodes {
					if jobs[plan.Graph.Nodes[i].ID] == stageID {
						plan.Graph.Nodes[i].Inputs = slices.DeleteFunc(plan.Graph.Nodes[i].Inputs,
							func(id string) bool { return id == omitted })
					}
				}
				t.Chdir(t.TempDir())
				rejected, err := RenderGitHub(t.Context(), plan)
				if rejected != nil || factoryErrorCode(err) != wantCode {
					t.Fatalf("render admission: bundle=%v error=%v", rejected, err)
				}
				if err := ValidateGitHubBundle(t.Context(), plan, bundle); factoryErrorCode(err) != wantCode {
					t.Fatalf("bundle admission: %v", err)
				}
				var diagnostic bytes.Buffer
				err = RestoreGitHubInputs(t.Context(), plan, stageID, GitHubStepOptions{Stdout: &diagnostic, Stderr: &diagnostic})
				if factoryErrorCode(err) != wantCode || diagnostic.Len() != 0 {
					t.Fatalf("restore admission: output=%s error=%v", diagnostic.String(), err)
				}
				entries, err := os.ReadDir(".")
				if err != nil || len(entries) != 0 {
					t.Fatalf("rejected graph performed filesystem work: %v, %v", entries, err)
				}
			})
		}
	}
}
