// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func nightlyIdentityFixture() (NightlyProfile, NightlyBuild) {
	profile := NightlyProfile{Name: "nightly-fixture", Bundle: "nightly-fixture", Definition: "definitions/nightly-fixture/definition.json", Environment: "definitions/nightly-fixture/environment-github.json", Plan: ".github/nvkr-run-nightly-fixture/plan.json", BuildWorkflow: "nvkr-run-nightly-fixture.yml", HarnessCommit: strings.Repeat("a", 40), RuntimeTarget: "kind-vms-arm64", Network: "nvidia-ovn", DiskOutput: "disk", ProfilesOutput: "profiles", OSProfile: "os", KubernetesVersion: "v1.36.0", LabelFilter: "nightly", SecurityStatus: "admitted"}
	build := NightlyBuild{Profile: profile, CoordinatorRun: "100", CoordinatorAttempt: "2", ProducerRun: 200, ProducerAttempt: 1, SourceCommit: strings.Repeat("b", 40), ProducerURL: "https://github.com/example/nvkr/actions/runs/200", Trust: "github-run-facts; publication-not-yet-verified"}
	return profile, build
}

func assertNightlyCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if err == nil || !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v; want %s", err, code)
	}
}

func TestNightlyCycleBindsCoordinatorSourceProfileAndProducer(t *testing.T) {
	profile, base := nightlyIdentityFixture()
	for _, test := range []struct {
		name   string
		change func(*NightlyBuild)
	}{
		{"profile substitution", func(b *NightlyBuild) { b.Profile.HarnessCommit = strings.Repeat("c", 40) }},
		{"head substitution", func(b *NightlyBuild) { b.SourceCommit = strings.Repeat("c", 40) }},
		{"producer retry", func(b *NightlyBuild) { b.ProducerAttempt = 2 }},
		{"missing run", func(b *NightlyBuild) { b.ProducerRun = 0 }},
		{"same coordinator", func(b *NightlyBuild) { b.ProducerRun = 100 }},
		{"imprecise JSON id", func(b *NightlyBuild) { b.ProducerRun = nightlyMaxJSONInteger + 1 }},
		{"coordinator retry", func(b *NightlyBuild) { b.CoordinatorAttempt = "3" }},
		{"coordinator substitution", func(b *NightlyBuild) { b.CoordinatorRun = "101" }},
		{"URL substitution", func(b *NightlyBuild) { b.ProducerURL = "https://github.com/example/nvkr/actions/runs/201" }},
		{"claimed authority", func(b *NightlyBuild) { b.Trust = "verified" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.change(&changed)
			assertNightlyCode(t, validateNightlyCycle(profile, changed, "example/nvkr", "123", base.SourceCommit, "100", "2"), e.CodeUnpinnedInput)
		})
	}
	if err := validateNightlyCycle(profile, base, "example/nvkr", "123", base.SourceCommit, "100", "2"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"0", "-1", "01", "1.5", "9007199254740992"} {
		t.Run("noncanonical id "+id, func(t *testing.T) {
			assertNightlyCode(t, validateNightlyCycle(profile, base, "example/nvkr", id, base.SourceCommit, "100", "2"), e.CodeUnpinnedInput)
		})
	}
}

func TestNightlyInputReadersRejectAmbiguousUnsafeAndUnboundedRecords(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	directory := t.TempDir()
	profilePath := filepath.Join(directory, "profiles.json")
	buildPath := filepath.Join(directory, "build.json")
	write := func(file string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(profilePath, []NightlyProfile{profile})
	selected, err := loadNightlyProfile(t.Context(), profilePath, profile.Name)
	if err != nil || selected != profile {
		t.Fatal(selected, err)
	}
	write(buildPath, build)
	decoded, err := loadNightlyBuild(t.Context(), buildPath)
	if err != nil || decoded != build {
		t.Fatal(decoded, err)
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"unknown field", []byte(`{"profile":{},"unrecognized":true}`)},
		{"duplicate field", []byte(`{"producerRun":1,"producerRun":2}`)},
		{"fractional id", []byte(`{"producerRun":1.5}`)},
		{"trailing object", []byte(`{} {}`)},
		{"oversized", make([]byte, nightlyRecordLimit+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(buildPath, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadNightlyBuild(t.Context(), buildPath)
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
	write(profilePath, []NightlyProfile{profile, profile})
	_, err = loadNightlyProfile(t.Context(), profilePath, profile.Name)
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	profile.Definition = "../outside.json"
	write(profilePath, []NightlyProfile{profile})
	_, err = loadNightlyProfile(t.Context(), profilePath, profile.Name)
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	symlink := filepath.Join(directory, "symlink.json")
	if err = os.Symlink(buildPath, symlink); err != nil {
		t.Fatal(err)
	}
	_, err = loadNightlyBuild(t.Context(), symlink)
	assertNightlyCode(t, err, e.CodeVerifyFailed)
}

func nightlyCompiledFixture(t *testing.T, profile NightlyProfile, build NightlyBuild) (*factory.Plan, lifecycle.Run) {
	t.Helper()
	catalog, err := filepath.Abs("../../definitions/nvke-v1/catalog")
	if err != nil {
		t.Fatal(err)
	}
	declared := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: profile.Name}, Catalog: definition.Catalog{Base: catalog}, SBOMTool: "syft", CosignTool: "cosign", Outputs: []definition.DefinitionOutput{{Name: "nvkectl", Platform: "linux/arm64", Format: definition.OutputExecutable, Components: []string{"nvkectl"}}}}
	environment, err := definition.LoadEnvironment(t.Context(), "../../definitions/nvke-v1/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	environment.GitHubBundle = profile.Bundle
	environment.Publication.Mode = "per-output-v1"
	environment.Publication.RepositoryPrefix = "ghcr.io/example/nvkr/nightly/fixture"
	environment.Signing.Identity = "^https://github\\.com/example/nvkr/\\.github/workflows/" + profile.BuildWorkflow + "@refs/heads/main$"
	plan, err := factory.CompileDocuments(t.Context(), profile.Definition, declared, profile.Environment, environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	run, err := lifecycle.PrepareRun(declared, environment, plan, "200-1")
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Lane = lifecycle.RunComplete, lifecycle.LanePipeline
	for _, output := range plan.LifecycleOutputs {
		run.Observed = append(run.Observed, delivery.Output{Name: output.Name, Kind: output.Kind, BuildDigest: output.BuildDigest, Components: output.Components, Digest: pipeline.FileDigest([]byte("observed " + output.Name))})
	}
	run.ContentSubject, err = delivery.ContentDigest(run.Observed)
	if err != nil {
		t.Fatal(err)
	}
	return plan, *run
}

func nightlyAPIRun(profile NightlyProfile, build NightlyBuild) nightlyGitHubRun {
	run := nightlyGitHubRun{ID: build.ProducerRun, RunAttempt: 1, HeadSHA: build.SourceCommit, HeadBranch: "main", Event: "workflow_dispatch", Path: ".github/workflows/" + profile.BuildWorkflow, Status: "completed", Conclusion: "success", HTMLURL: build.ProducerURL}
	run.Repository.ID, run.Repository.FullName = 123, "example/nvkr"
	run.HeadRepository = run.Repository
	return run
}

func nightlyAPIArtifact(name string, build NightlyBuild) nightlyGitHubArtifact {
	expired := false
	artifact := nightlyGitHubArtifact{ID: 300, Name: name, Size: 100, Digest: pipeline.FileDigest([]byte(name)), Expired: &expired}
	if name == "nvkr-publication-release" {
		artifact.ID = 301
	}
	artifact.WorkflowRun.ID, artifact.WorkflowRun.RepositoryID, artifact.WorkflowRun.HeadRepositoryID, artifact.WorkflowRun.HeadSHA, artifact.WorkflowRun.HeadBranch = build.ProducerRun, 123, 123, build.SourceCommit, "main"
	return artifact
}

func TestNightlyArtifactsRejectDuplicateExpiredAndForeignOrigins(t *testing.T) {
	_, build := nightlyIdentityFixture()
	for _, test := range []struct {
		name   string
		change func(*nightlyArtifactList)
	}{
		{"duplicate", func(list *nightlyArtifactList) {
			list.TotalCount = 2
			list.Artifacts = append(list.Artifacts, list.Artifacts[0])
		}},
		{"expired", func(list *nightlyArtifactList) { expired := true; list.Artifacts[0].Expired = &expired }},
		{"missing expiration", func(list *nightlyArtifactList) { list.Artifacts[0].Expired = nil }},
		{"wrong run", func(list *nightlyArtifactList) { list.Artifacts[0].WorkflowRun.ID++ }},
		{"wrong source", func(list *nightlyArtifactList) { list.Artifacts[0].WorkflowRun.HeadSHA = strings.Repeat("c", 40) }},
		{"fork repository", func(list *nightlyArtifactList) { list.Artifacts[0].WorkflowRun.HeadRepositoryID++ }},
		{"unsafe size", func(list *nightlyArtifactList) { list.Artifacts[0].Size = 33 << 20 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			list := nightlyArtifactList{TotalCount: 1, Artifacts: []nightlyGitHubArtifact{nightlyAPIArtifact("nvkr-lifecycle-run", build)}}
			test.change(&list)
			_, err := selectNightlyArtifact(list, "nvkr-lifecycle-run", build, 123)
			code := e.CodeUnpinnedInput
			if test.name == "duplicate" {
				code = e.CodeVerifyFailed
			}
			assertNightlyCode(t, err, code)
		})
	}
}

func TestNightlyExactDownloadsAndOwnerCarrierRestore(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	plan, baseRun := nightlyCompiledFixture(t, profile, build)
	for _, scenario := range []string{"success", "wrong head", "wrong attempt", "wrong workflow", "producer rerun after download", "artifact replacement", "corrupt run", "corrupt content subject", "wrong reference namespace", "extra carrier path"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.Chmod(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			downloads := 0
			capture := func(ctx context.Context, args []string, phase string, timeout time.Duration) ([]byte, error) {
				t.Helper()
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if timeout <= 0 {
					t.Fatal("unbounded command")
				}
				marshal := func(value any) ([]byte, error) { return json.Marshal(value) }
				if strings.HasPrefix(phase, "nightly-producer-") {
					run := nightlyAPIRun(profile, build)
					switch scenario {
					case "wrong head":
						run.HeadSHA = strings.Repeat("c", 40)
					case "wrong attempt":
						run.RunAttempt = 2
					case "wrong workflow":
						run.Path = ".github/workflows/other.yml"
					}
					if scenario == "producer rerun after download" && strings.HasSuffix(phase, "after") {
						run.RunAttempt = 2
					}
					return marshal(run)
				}
				if strings.HasPrefix(phase, "nightly-artifact-before-") || strings.HasPrefix(phase, "nightly-artifact-after-") {
					name := "nvkr-lifecycle-run"
					if strings.HasSuffix(phase, "1") {
						name = "nvkr-publication-release"
					}
					artifact := nightlyAPIArtifact(name, build)
					if scenario == "artifact replacement" && strings.Contains(phase, "-after-") {
						artifact.ID++
					}
					return marshal(nightlyArtifactList{TotalCount: 1, Artifacts: []nightlyGitHubArtifact{artifact}})
				}
				if len(args) != 10 || args[0] != "gh" || args[1] != "run" || args[2] != "download" || args[3] != "200" || args[4] != "--repo" || args[5] != "github.com/example/nvkr" || args[6] != "--name" || args[8] != "--dir" {
					t.Fatalf("incorrect exact download command %q", args)
				}
				downloads++
				destination := args[9]
				if strings.HasSuffix(phase, "0") {
					run := baseRun
					if scenario == "corrupt run" {
						run.PlanDigest = pipeline.FileDigest([]byte("different plan"))
					}
					if scenario == "corrupt content subject" {
						run.ContentSubject = pipeline.FileDigest([]byte("different outputs"))
					}
					raw, err := json.Marshal(run)
					if err != nil {
						return nil, err
					}
					return nil, os.WriteFile(filepath.Join(destination, "run.json"), raw, 0o600)
				}
				source := t.TempDir()
				relative := ".nvkr/factory/publications/release.txt"
				if err := os.MkdirAll(filepath.Join(source, filepath.Dir(relative)), 0o700); err != nil {
					return nil, err
				}
				reference := plan.Environment.Publication.RepositoryPrefix + "/releases@" + pipeline.FileDigest([]byte("published root")) + "\n"
				if scenario == "wrong reference namespace" {
					reference = "ghcr.io/other/nvkr/releases@" + pipeline.FileDigest([]byte("published root")) + "\n"
				}
				if err := os.WriteFile(filepath.Join(source, relative), []byte(reference), 0o600); err != nil {
					return nil, err
				}
				allowed := []string{relative}
				if scenario == "extra carrier path" {
					if err := os.WriteFile(filepath.Join(source, "unexpected"), []byte("not admitted"), 0o600); err != nil {
						return nil, err
					}
					allowed = append(allowed, "unexpected")
				}
				archive := filepath.Join(source, "publication-release.tar")
				if err := transportarchive.Pack(ctx, archive, source, allowed); err != nil {
					return nil, err
				}
				return nil, os.Rename(archive, filepath.Join(destination, "publication-release.tar"))
			}
			inputs, err := newNightlyInputsWithCapture(t.Context(), profile, build, plan, workspace, "example/nvkr", "123", build.SourceCommit, "100", "2", capture)
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if inputs.ExpectedInvocation != "https://github.com/example/nvkr/actions/runs/200/attempts/1" || inputs.Run.PlanDigest != baseRun.PlanDigest || !strings.HasPrefix(inputs.Reference, plan.Environment.Publication.RepositoryPrefix+"/releases@") {
					t.Fatal("handoff binding differs", inputs)
				}
				if _, err := os.Stat(filepath.Join(inputs.LocatorRoot, ".nvkr/factory/publications/release.txt")); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || inputs != nil {
				t.Fatal("invalid handoff accepted", inputs, err)
			}
			code := e.CodeUnpinnedInput
			if scenario == "corrupt run" || scenario == "corrupt content subject" || scenario == "extra carrier path" {
				code = e.CodeVerifyFailed
			}
			assertNightlyCode(t, err, code)
			if (scenario == "wrong head" || scenario == "wrong attempt" || scenario == "wrong workflow") && downloads != 0 {
				t.Fatal("identity rejection happened after artifact output")
			}
		})
	}
}

func TestNightlyCancellationAndSecurityBlockPrecedeArtifactWork(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	plan, _ := nightlyCompiledFixture(t, profile, build)
	for _, scenario := range []string{"canceled", "security blocked", "wrong trusted source path"} {
		t.Run(scenario, func(t *testing.T) {
			selected, locator := profile, build
			ctx := t.Context()
			code := e.CodeTimeout
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if scenario == "security blocked" {
				selected.SecurityStatus = "blocked"
				selected.BlockedReason = "security check failed"
				locator.Profile = selected
				code = e.CodeEngineCapability
			}
			if scenario == "wrong trusted source path" {
				selected.Definition = "definitions/other/definition.json"
				locator.Profile = selected
				code = e.CodePipelineDrift
			}
			workspace := t.TempDir()
			if err := os.Chmod(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			capture := func(context.Context, []string, string, time.Duration) ([]byte, error) { calls++; return nil, nil }
			inputs, err := newNightlyInputsWithCapture(ctx, selected, locator, plan, workspace, "example/nvkr", "123", build.SourceCommit, "100", "2", capture)
			assertNightlyCode(t, err, code)
			entries, readErr := os.ReadDir(workspace)
			if inputs != nil || calls != 0 || readErr != nil || len(entries) != 0 {
				t.Fatal("rejected admission performed artifact work", inputs, calls, entries, readErr)
			}
		})
	}
}
