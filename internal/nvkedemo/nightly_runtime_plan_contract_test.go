// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Controlled source descriptions exercise real compilation and consumer joins;
// these package-shaped fixtures do not claim a completed upstream source build.
func nightlyRuntimeCompiledFixture(t *testing.T, profile NightlyProfile, build NightlyBuild, commit string, sourcePolicy ...bool) (*factory.Plan, lifecycle.Run) {
	t.Helper()
	root, err := filepath.Abs("../../inputs/catalogs/nvke/module-tags")
	if err != nil {
		t.Fatal(err)
	}
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	producer := base.Components["nvkectl"]
	base.Components = map[string]catalog.Component{}
	for _, name := range []string{"nvkectl", "nvke-controller", "nvke-controller-chart", "nktp-chart"} {
		component := producer
		component.Commit, component.TreeHash = commit, commit
		if len(sourcePolicy) != 0 && sourcePolicy[0] {
			component.Tag = commit
		}
		base.Components[name] = component
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	declared := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: profile.Name}, Catalog: definition.Catalog{Base: root}, SBOMTool: "syft", CosignTool: "cosign"}
	for _, role := range []struct{ name, component string }{{"controller-chart", "nvke-controller-chart"}, {"cwe", "nvke-controller"}, {"nktp", "nktp-chart"}, {"nvkectl", "nvkectl"}} {
		declared.Outputs = append(declared.Outputs, definition.DefinitionOutput{Name: role.name, Platform: "linux/arm64", Format: definition.OutputExecutable, Components: []string{role.component}})
	}
	environment, err := definition.LoadEnvironment(t.Context(), "../../docs/examples/nvke-tutorial/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	environment.GitHubBundle, environment.GitHubRuntimePlan = profile.Bundle, true
	if len(sourcePolicy) != 0 && sourcePolicy[0] {
		environment.GitHubRuntimeSource = &definition.GitHubRuntimeSource{Repository: "https://github.com/dsx-ai-factory/nvke", Ref: "refs/heads/main"}
	}
	environment.Publication.Mode, environment.Publication.RepositoryPrefix = "per-output-v1", "ghcr.io/example/nvkr/nightly/fixture"
	environment.Signing.Identity = "^https://github\\.com/example/nvkr/\\.github/workflows/" + profile.BuildWorkflow + "@refs/heads/main$"
	plan, err := factory.Compile(t.Context(), declared, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = profile.Definition, profile.Environment
	if environment.GitHubRuntimeSource != nil {
		plan.RuntimeSourceCommit = commit
	}
	run, err := lifecycle.PrepareRun(declared, environment, plan, fmt.Sprintf("%d-%d", build.ProducerRun, build.ProducerAttempt))
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

func nightlyRuntimeZIPFixture(t *testing.T, plan []byte, scenario string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entries := []struct {
		name string
		body []byte
	}{{"plan.json", plan}, {"action-data.json", []byte("{}")}, {"manifest.json", []byte("{}")}}
	if scenario == "extra runtime member" {
		entries = append(entries, struct {
			name string
			body []byte
		}{"unexpected", []byte("unexpected")})
	}
	if scenario == "duplicate runtime member" {
		entries[2].name = "plan.json"
	}
	for _, entry := range entries {
		member, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestNightlyRuntimePlanHandoffUsesFreshFrozenSourcePins(t *testing.T) {
	for _, sourcePolicy := range []bool{false, true} {
		t.Run(map[bool]string{false: "catalog pins", true: "cycle source policy"}[sourcePolicy], func(t *testing.T) {
			testNightlyRuntimePlanHandoff(t, sourcePolicy)
		})
	}
}

func testNightlyRuntimePlanHandoff(t *testing.T, sourcePolicy bool) {
	t.Helper()
	profile, build := nightlyIdentityFixture()
	if sourcePolicy {
		build.NVKECommit = strings.Repeat("d", 40)
	}
	baseline, _ := nightlyRuntimeCompiledFixture(t, profile, build, strings.Repeat("c", 40), sourcePolicy)
	fresh, freshRun := nightlyRuntimeCompiledFixture(t, profile, build, strings.Repeat("d", 40), sourcePolicy)
	baselineDigest, err := baseline.Digest()
	if err != nil {
		t.Fatal(err)
	}
	freshDigest, err := fresh.Digest()
	if err != nil || baselineDigest == freshDigest {
		t.Fatalf("fixture did not change exact source pins: %s, %s, %v", baselineDigest, freshDigest, err)
	}
	for _, scenario := range []string{"fresh", "current trusted Definition", "stale Plan", "mismatched Run", "substituted Environment", "substituted Definition", "substituted Plan path", "invalid Plan", "malformed Plan", "oversized Plan", "wrong runtime origin", "replaced runtime artifact", "corrupt runtime ZIP", "extra runtime member", "duplicate runtime member", "wrong cycle source", "missing cycle source"} {
		t.Run(scenario, func(t *testing.T) {
			locator := build
			if scenario == "wrong cycle source" || scenario == "missing cycle source" {
				if !sourcePolicy {
					t.Skip("catalog-pin policy does not require a cycle source selection")
				}
				locator.NVKECommit = ""
				if scenario == "wrong cycle source" {
					locator.NVKECommit = strings.Repeat("e", 40)
				}
			}
			selectedPlan := fresh
			expectedDigest := freshDigest
			run := freshRun
			if scenario == "stale Plan" {
				selectedPlan = baseline
			}
			if scenario == "current trusted Definition" || scenario == "substituted Plan path" || scenario == "invalid Plan" {
				changed := *fresh
				selectedPlan = &changed
				switch scenario {
				case "current trusted Definition":
					changed.Definition.Metadata.Description = "current source metadata absent from old generated Plan"
					changed.DefinitionDigest, err = changed.Definition.Digest()
					if err != nil {
						t.Fatal(err)
					}
					expectedDigest, err = changed.Digest()
					if err != nil {
						t.Fatal(err)
					}
					run.DefinitionDigest, run.PlanDigest = changed.DefinitionDigest, expectedDigest
				case "substituted Plan path":
					changed.DefinitionPath = "definitions/other/definition.json"
				case "invalid Plan":
					changed.SchemaVersion = "foreign"
				}
			}
			rawPlan, err := canonical.Marshal(selectedPlan)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "malformed Plan" {
				rawPlan = []byte("{broken")
			}
			if scenario == "oversized Plan" {
				rawPlan = bytes.Repeat([]byte(" "), int(factory.MaxPlanBytes)+1)
			}
			if scenario == "mismatched Run" {
				run.PlanDigest = baselineDigest
			}
			archives := nightlyDownloadFixture(t, fresh, run, "success")
			archives = append(archives, nightlyRuntimeZIPFixture(t, rawPlan, scenario))
			trustedDefinition, trustedEnvironment := fresh.Definition, fresh.Environment
			if scenario == "current trusted Definition" {
				trustedDefinition = selectedPlan.Definition
			}
			if scenario == "substituted Definition" {
				trustedDefinition.Metadata.Description = "a different trusted Definition"
			}
			if scenario == "substituted Environment" {
				trustedEnvironment.Metadata.Description = "a different trusted Environment"
			}
			capture := func(ctx context.Context, args []string, phase string, timeout time.Duration) ([]byte, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if timeout <= 0 {
					t.Fatal("unbounded provider call")
				}
				if strings.HasPrefix(phase, "nightly-producer-") {
					return json.Marshal(nightlyAPIRun(profile, build))
				}
				index, err := strconv.Atoi(phase[strings.LastIndex(phase, "-")+1:])
				if err != nil || index < 0 || index >= len(archives) {
					t.Fatalf("unexpected provider phase %s", phase)
				}
				if strings.HasPrefix(phase, "nightly-artifact-before-") || strings.HasPrefix(phase, "nightly-artifact-after-") {
					name := []string{"nvkr-lifecycle-run", "nvkr-publication-release", "nvkr-runtime-inputs"}[index]
					artifact := nightlyAPIArtifact(name, build)
					artifact.ID = int64(300 + index)
					artifact.Size, artifact.Digest = int64(len(archives[index])), pipeline.FileDigest(archives[index])
					if index == 2 && scenario == "wrong runtime origin" {
						artifact.WorkflowRun.HeadSHA = strings.Repeat("e", 40)
					}
					if index == 2 && scenario == "replaced runtime artifact" && strings.Contains(phase, "-after-") {
						artifact.ID++
					}
					return json.Marshal(nightlyArtifactList{TotalCount: 1, Artifacts: []nightlyGitHubArtifact{artifact}})
				}
				want := []string{"gh", "api", "--hostname", "github.com", "--allow-escape-sequences", "-H", "X-GitHub-Api-Version: 2026-03-10", fmt.Sprintf("repos/example/nvkr/actions/artifacts/%d/zip", 300+index)}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("incorrect exact download %q", args)
				}
				if index == 2 && scenario == "corrupt runtime ZIP" {
					changed := bytes.Clone(archives[index])
					changed[0] ^= 1
					return changed, nil
				}
				return archives[index], nil
			}
			workspace := t.TempDir()
			if err := os.Chmod(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			inputs, err := newNightlyInputsWithCapture(t.Context(), profile, locator, baseline, trustedDefinition, trustedEnvironment, workspace, "example/nvkr", "123", build.SourceCommit, "100", "2", capture)
			if scenario == "fresh" || scenario == "current trusted Definition" {
				if err != nil || inputs == nil || inputs.Plan == baseline || inputs.Run.PlanDigest != expectedDigest || inputs.HarnessCommit != strings.Repeat("d", 40) {
					t.Fatalf("fresh frozen Plan not consumed: %+v, %v", inputs, err)
				}
				return
			}
			if err == nil || inputs != nil {
				t.Fatal("invalid runtime Plan handoff admitted", inputs, err)
			}
			code := e.CodeVerifyFailed
			if sourcePolicy && scenario == "stale Plan" {
				code = e.CodeUnpinnedInput
			}
			switch scenario {
			case "malformed Plan", "substituted Plan path":
				code = e.CodePipelineDrift
			case "invalid Plan":
				code = e.CodeInvalidCatalog
			case "substituted Environment", "substituted Definition":
				code = e.CodeUnverifiedRef
			case "wrong runtime origin", "replaced runtime artifact", "wrong cycle source", "missing cycle source":
				code = e.CodeUnpinnedInput
			}
			assertNightlyCode(t, err, code)
		})
	}
}

func TestNightlyPlanHarnessCommitRequiresUniqueExactNVKECohort(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	for _, test := range []struct {
		name   string
		change func(*factory.Plan)
		code   e.Code
	}{
		{"complete", func(*factory.Plan) {}, ""},
		{"missing role", func(p *factory.Plan) { p.Builds = p.Builds[1:] }, e.CodeProvenanceMissing},
		{"duplicate role", func(p *factory.Plan) { p.Builds = append(p.Builds, p.Builds[0]) }, e.CodePipelineDrift},
		{"wrong component", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].Name = "other" }, e.CodeUnpinnedInput},
		{"acquired component", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].FromSource = false }, e.CodeUnpinnedInput},
		{"foreign source", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].Repo = "https://github.com/foreign/nvke" }, e.CodeUnpinnedInput},
		{"mixed revisions", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].Commit = strings.Repeat("e", 40) }, e.CodeUnpinnedInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, _ := nightlyRuntimeCompiledFixture(t, profile, build, strings.Repeat("d", 40))
			test.change(plan)
			commit, err := nightlyPlanHarnessCommit(plan, profile)
			if test.code != "" {
				assertNightlyCode(t, err, test.code)
			} else if err != nil || commit != strings.Repeat("d", 40) {
				t.Fatalf("wrong source cohort %s: %v", commit, err)
			}
		})
	}
}
