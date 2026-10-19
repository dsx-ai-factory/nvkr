// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

var nvkeSplitFamilies = []struct {
	name            string
	outputs, native int
}{
	{"core", 20, 24},
	{"services", 28, 28},
	{"telemetry", 17, 17},
	{"disks", 4, 4},
}

func TestNVKEDefinitionSplitPreservesCompleteMembership(t *testing.T) {
	nvkeSplitRepository(t)
	combined := nvkeSplitDefinition(t, "inputs/definitions/nvke/definition.yaml")
	original := make(map[string][]byte, len(combined.Outputs))
	for _, output := range combined.Outputs {
		original[output.Name] = nvkeSplitCanonical(t, output)
	}
	seen := make(map[string]string)
	nativeTotal := 0
	for _, family := range nvkeSplitFamilies {
		t.Run(family.name, func(t *testing.T) {
			path, _ := nvkeSplitPaths(family.name)
			doc := nvkeSplitDefinition(t, path)
			if !bytes.Equal(nvkeSplitCanonical(t, doc.Catalog), nvkeSplitCanonical(t, combined.Catalog)) ||
				doc.SBOMTool != combined.SBOMTool || doc.CosignTool != combined.CosignTool {
				t.Fatal("family changed the shared catalog or evidence tools")
			}
			native := 0
			for _, output := range doc.Outputs {
				if previous, exists := seen[output.Name]; exists {
					t.Fatalf("output %q is required by both %s and %s", output.Name, previous, family.name)
				}
				seen[output.Name] = family.name
				// The whole declaration includes catalog/overlay selection, exact
				// material membership, native platforms and chart image bindings.
				if !bytes.Equal(nvkeSplitCanonical(t, output), original[output.Name]) {
					t.Fatalf("output %q is added or changes the combined selection", output.Name)
				}
				if (output.Format == definition.OutputDiskImage) != (family.name == "disks") {
					t.Fatalf("disk output %q escaped its native execution family", output.Name)
				}
				native += max(1, len(output.Platforms))
			}
			if len(doc.Outputs) != family.outputs || native != family.native {
				t.Fatalf("outputs/native targets = %d/%d, want %d/%d", len(doc.Outputs), native, family.outputs, family.native)
			}
			nativeTotal += native
		})
	}
	if len(original) != 69 || len(seen) != len(original) || nativeTotal != 73 {
		t.Fatalf("partition requires %d of %d original outputs and %d native targets; want 69/69 and 73", len(seen), len(original), nativeTotal)
	}
}

func TestNVKEDefinitionSplitEnvironmentIsolation(t *testing.T) {
	nvkeSplitRepository(t)
	combined := nvkeSplitEnvironment(t, "inputs/environments/github-release.yaml")
	for _, family := range nvkeSplitFamilies {
		t.Run(family.name, func(t *testing.T) {
			_, path := nvkeSplitPaths(family.name)
			env := nvkeSplitEnvironment(t, path)
			bundleName := "nvke-" + family.name
			workflow := ".github/workflows/nvkr-run-" + bundleName + ".yml"
			identity := "https://github.com/dsx-ai-factory/nvkr/" + workflow
			policy, err := regexp.Compile(env.Signing.Identity)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range nvkeSplitFamilies {
				for _, ref := range []string{"refs/heads/main", "refs/heads/main-other", "refs/heads/feature/split", "refs/tags/main"} {
					subject := "https://github.com/dsx-ai-factory/nvkr/.github/workflows/nvkr-run-nvke-" + candidate.name + ".yml@" + ref
					if got, want := policy.MatchString(subject), candidate.name == family.name && ref == "refs/heads/main"; got != want {
						t.Fatalf("signer policy admits %q = %t, want %t", subject, got, want)
					}
				}
			}
			if policy.MatchString("https://github.com/dsx-ai-factory/nvkr/.github/workflows/nvkr-run.yml@refs/heads/main") ||
				!policy.MatchString(identity+"@refs/heads/main") {
				t.Fatal("family signer is not isolated from the transitional combined producer")
			}

			// Preserve the complete execution/publication policy except the
			// explicitly scoped family identity, credentials and disk capability.
			want := combined.Clone()
			want.Metadata = env.Metadata
			want.GitHubBundle = bundleName
			want.Publication.RepositoryPrefix += "/" + family.name
			want.Signing.Identity = "^" + regexp.QuoteMeta(identity) + "@refs/heads/main$"
			source := "https://github.com/dsx-ai-factory/nvke"
			if family.name == "disks" {
				source += ".git"
			} else {
				want.GitHubARC, want.DiskEvidence = "", ""
				want.ExecutionClasses, want.DiskInspectionProfiles = nil, nil
			}
			for selected := range want.SourceAccess {
				if selected != source {
					delete(want.SourceAccess, selected)
				}
			}
			// Services still needs the private NVKE catalog pin-set credential,
			// even when every selected image/chart is acquired rather than built.
			if len(want.SourceAccess) != 1 || !bytes.Equal(nvkeSplitCanonical(t, env), nvkeSplitCanonical(t, want)) {
				t.Fatal("family changes policy outside its namespace or retains the wrong source/disk capabilities")
			}
		})
	}
}

// Keep stale authored selections visible in the default suite without catalog
// loading, full compilation or rendering. The extended contract below separately
// compares complete producer records and every generated bundle member.
func TestNVKEDefinitionSplitCommittedPlanBindings(t *testing.T) {
	nvkeSplitRepository(t)
	for _, family := range nvkeSplitFamilies {
		t.Run(family.name, func(t *testing.T) {
			definitionPath, environmentPath := nvkeSplitPaths(family.name)
			plan, err := LoadPlanContext(t.Context(), ".github/nvkr-run-nvke-"+family.name+"/plan.json")
			if err != nil {
				t.Fatal(err)
			}
			if plan.DefinitionPath != definitionPath || plan.EnvironmentPath != environmentPath {
				t.Fatal("committed family Plan binds different authored input paths")
			}
			if !bytes.Equal(nvkeSplitCanonical(t, plan.Definition), nvkeSplitCanonical(t, nvkeSplitDefinition(t, definitionPath))) {
				t.Fatal("committed family Plan has a stale authored Definition")
			}
			if !bytes.Equal(nvkeSplitCanonical(t, plan.Environment), nvkeSplitCanonical(t, nvkeSplitEnvironment(t, environmentPath))) {
				t.Fatal("committed family Plan has a stale authored Environment")
			}
		})
	}
}

func TestNVKEDefinitionSplitRejectsCrossFamilyChartBinding(t *testing.T) {
	nvkeSplitRepository(t)
	for _, image := range []string{"missing-controller", "spire-agent-image"} {
		t.Run(image, func(t *testing.T) {
			doc := nvkeSplitDefinition(t, "inputs/definitions/nvke/core.yaml")
			found := false
			for index := range doc.Outputs {
				if doc.Outputs[index].Name == "controller-chart" {
					doc.Outputs[index].Helm.Images["controller"] = image
					found = true
				}
			}
			if !found {
				t.Fatal("core selection omitted its bound controller chart")
			}
			assertFactoryErrorCode(t, doc.Validate(), e.CodeInvalidCatalog)
		})
	}
}

// Full product compilation/rendering remains opt-in. This test establishes
// local identity and generation contracts, never hosted or Release acceptance.
func TestNVKEDefinitionSplitCompiledIdentityAndGeneratedBundles(t *testing.T) {
	testutil.RequireExtended(t)
	nvkeSplitRepository(t)
	combined, err := LoadPlanContext(t.Context(), FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nvkeSplitCanonical(t, combined.Definition), nvkeSplitCanonical(t, nvkeSplitDefinition(t, combined.DefinitionPath))) {
		t.Fatal("transitional Plan no longer represents the combined authored selection")
	}
	builds := make(map[string][]byte, len(combined.Builds))
	for _, build := range combined.Builds {
		builds[build.Name] = nvkeSplitCanonical(t, build)
	}
	outputs := make(map[string][]byte, len(combined.LifecycleOutputs))
	for _, output := range combined.LifecycleOutputs {
		outputs[output.Name] = nvkeSplitCanonical(t, output)
	}
	loadStack := nvkeSplitCatalogLoader(t, combined)
	ownedFiles := make(map[string]string)
	buildCount, outputCount := 0, 0
	for _, family := range nvkeSplitFamilies {
		t.Run(family.name, func(t *testing.T) {
			definitionPath, environmentPath := nvkeSplitPaths(family.name)
			doc := nvkeSplitDefinition(t, definitionPath)
			env := nvkeSplitEnvironment(t, environmentPath)
			plan, err := compileDocumentsWithLoader(t.Context(), definitionPath, doc, environmentPath, env, combined.GeneratorVersion, loadStack)
			if err != nil {
				t.Fatal(err)
			}
			for _, build := range plan.Builds {
				if !bytes.Equal(nvkeSplitCanonical(t, build), builds[build.Name]) {
					t.Fatalf("native build %q changed its identity, catalog or complete producer graph", build.Name)
				}
			}
			for _, output := range plan.LifecycleOutputs {
				if !bytes.Equal(nvkeSplitCanonical(t, output), outputs[output.Name]) {
					t.Fatalf("output %q changed its named artifact, evidence or native-child binding", output.Name)
				}
			}
			buildCount += len(plan.Builds)
			outputCount += len(plan.LifecycleOutputs)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			workflowDocument := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			for id, job := range workflowDocument.Jobs {
				if strings.HasPrefix(id, "build-runner-") && job.If != "${{ "+githubExecutionCondition+" }}" {
					t.Fatalf("family helper %s permits nondefault-ref execution: %q", id, job.If)
				}
			}
			if family.name != "disks" {
				assertNoPrivateToolchainPreparation(t, bundle)
			}
			if bundle.WorkflowPath != ".github/workflows/nvkr-run-nvke-"+family.name+".yml" ||
				bundle.PlanPath != ".github/nvkr-run-nvke-"+family.name+"/plan.json" ||
				bundle.ManifestPath != ".github/nvkr-run-nvke-"+family.name+"/manifest.json" {
				t.Fatal("family does not own its independent named bundle")
			}
			for name := range bundle.Files {
				if previous, exists := ownedFiles[name]; exists {
					t.Fatalf("generated file %q is shared with %s", name, previous)
				}
				ownedFiles[name] = family.name
			}
			if err := CheckGeneratedBundle(t.Context(), ".", bundle); err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), combined, bundle), e.CodePipelineDrift)
			workflow := bundle.Files[bundle.WorkflowPath]
			events, err := countGitHubWorkflowYAMLNodes(workflow)
			if err != nil {
				t.Fatal(err)
			}
			if len(workflow) > maxGitHubWorkflowBytes*95/100 || events > maxGitHubWorkflowYAMLNodes*95/100 ||
				int64(len(bundle.Files[bundle.PlanPath])) > MaxPlanBytes {
				t.Fatal("family exhausted workflow headroom or cannot reload its generated Plan")
			}
			jobs := decodeGitHubWorkflow(t, workflow).Jobs
			t.Logf("%d outputs, %d native builds, %d nodes, %d jobs, %d workflow bytes, %d Plan bytes, %d YAML events",
				len(plan.LifecycleOutputs), len(plan.Builds), len(plan.Graph.Nodes), len(jobs), len(workflow), len(bundle.Files[bundle.PlanPath]), events)
		})
	}
	if buildCount != 73 || outputCount != 69 {
		t.Fatalf("compiled families retain %d native builds and %d outputs, want 73 and 69", buildCount, outputCount)
	}
}

func nvkeSplitCatalogLoader(t *testing.T, combined *Plan) func(context.Context, definition.Catalog) (*catalog.Stack, error) {
	t.Helper()
	// Compare compilation against the validated baseline's exact resolved
	// catalogs, without fetching private pin sets in a local regression test.
	// CLI generation and workflows-check separately authenticate current inputs;
	// CheckGeneratedBundle above binds this comparison to those generated bytes.
	stacks := map[string]*catalog.Stack{
		string(nvkeSplitCanonical(t, combined.Definition.Catalog)): {
			Catalog: combined.CatalogSnapshot, Digest: combined.CatalogDigest, Applied: combined.AppliedOverlays,
		},
	}
	for _, build := range combined.Builds {
		selection, ok := combined.selectionForBuild(build.Name)
		if !ok {
			t.Fatalf("baseline build %q has no authored output", build.Name)
		}
		spec := combined.Definition.Catalog
		if selection.Catalog != nil {
			spec = *selection.Catalog
		}
		key := string(nvkeSplitCanonical(t, spec))
		stack := &catalog.Stack{Catalog: build.CatalogSnapshot, Digest: build.CatalogDigest, Applied: build.AppliedOverlays}
		if previous, exists := stacks[key]; exists && (!bytes.Equal(nvkeSplitCanonical(t, previous), nvkeSplitCanonical(t, stack)) ||
			!bytes.Equal(nvkeSplitCanonical(t, previous.Catalog), nvkeSplitCanonical(t, stack.Catalog))) {
			t.Fatalf("catalog selection is inconsistent for baseline build %q", build.Name)
		}
		stacks[key] = stack
	}
	return func(ctx context.Context, spec definition.Catalog) (*catalog.Stack, error) {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "split fixture catalog load canceled")
		}
		stack, ok := stacks[string(nvkeSplitCanonical(t, spec))]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "split selected a catalog outside the combined baseline")
		}
		return stack, nil
	}
}

func nvkeSplitRepository(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

func nvkeSplitPaths(family string) (string, string) {
	return "inputs/definitions/nvke/" + family + ".yaml", "inputs/environments/github-nvke-" + family + ".yaml"
}

func nvkeSplitDefinition(t *testing.T, path string) definition.Definition {
	t.Helper()
	doc, err := definition.LoadDefinition(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func nvkeSplitEnvironment(t *testing.T, path string) definition.Environment {
	t.Helper()
	env, err := definition.LoadEnvironment(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func nvkeSplitCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
