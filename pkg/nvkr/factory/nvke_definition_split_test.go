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
	{"core", 26, 32},
	{"services", 34, 34},
	{"telemetry", 17, 17},
	{"disks", 4, 4},
	{"disks-ubuntu24", 4, 4},
}

// Combined-only outputs belong to no family. Each maps to the family output that
// delivers the same selection under another name: one Release cannot publish the
// same image under two outputs, and NVKE requires the controller-profiles name.
var nvkeCombinedOnlyOutputs = map[string]string{"nico-reference-profiles-fixture": "controller-profiles"}

// Family-only outputs are not in the combined Definition. Each declaration is
// pinned exactly so a valid but unintended selection fails here rather than only
// after regeneration; the Ubuntu 24.04 disks must keep the Ubuntu 24 overlay.
var nvkeFamilyOnlyOutputs = map[string]struct {
	family string
	output definition.DefinitionOutput
}{
	"controller-profiles":                {"core", definition.DefinitionOutput{Name: "controller-profiles", Platforms: []string{"linux/amd64", "linux/arm64"}, Catalog: &definition.Catalog{Base: "../../catalogs/nvke/release-components"}, Components: []string{"nvke-nico-reference-profiles"}, Format: definition.OutputOCI}},
	"local-path-helper-image":            {"services", nvkeFamilyOnlyOutput("local-path-helper-image", "linux/amd64", "local-path-provisioner", definition.OutputOCI, "local-path-helper-image", nil)},
	"local-path-helper-image-arm64":      {"services", nvkeFamilyOnlyOutput("local-path-helper-image-arm64", "linux/arm64", "local-path-provisioner", definition.OutputOCI, "local-path-helper-image", nil)},
	"local-path-provisioner":             {"services", nvkeFamilyOnlyOutput("local-path-provisioner", "linux/amd64", "local-path-provisioner", definition.OutputHelm, "local-path-provisioner-chart", map[string]string{"helper": "local-path-helper-image", "provisioner": "local-path-provisioner-image"})},
	"local-path-provisioner-arm64":       {"services", nvkeFamilyOnlyOutput("local-path-provisioner-arm64", "linux/arm64", "local-path-provisioner", definition.OutputHelm, "local-path-provisioner-chart", map[string]string{"helper": "local-path-helper-image-arm64", "provisioner": "local-path-provisioner-image-arm64"})},
	"local-path-provisioner-image":       {"services", nvkeFamilyOnlyOutput("local-path-provisioner-image", "linux/amd64", "local-path-provisioner", definition.OutputOCI, "local-path-provisioner-image", nil)},
	"local-path-provisioner-image-arm64": {"services", nvkeFamilyOnlyOutput("local-path-provisioner-image-arm64", "linux/arm64", "local-path-provisioner", definition.OutputOCI, "local-path-provisioner-image", nil)},
	"operator-tools-linux-arm64":         {"core", definition.DefinitionOutput{Name: "operator-tools-linux-arm64", Platform: "linux/arm64", Catalog: &definition.Catalog{Base: "../../catalogs/tools/operator-arm64"}, Components: []string{"helm", "kubectl"}, Format: definition.OutputExecutable}},
	"nico":                               {"core", nvkeFamilyOnlyOutput("nico", "linux/amd64", "nico", definition.OutputHelm, "nico-chart", nil)},
	"nico-core-image":                    {"core", definition.DefinitionOutput{Name: "nico-core-image", Platforms: []string{"linux/amd64", "linux/arm64"}, Catalog: &definition.Catalog{Base: "../../catalogs/nico"}, Components: []string{"nico-core-image"}, Format: definition.OutputOCI}},
	"nico-flow":                          {"core", nvkeFamilyOnlyOutput("nico-flow", "linux/amd64", "nico", definition.OutputHelm, "nico-flow-chart", nil)},
	"nico-flow-image":                    {"core", definition.DefinitionOutput{Name: "nico-flow-image", Platforms: []string{"linux/amd64", "linux/arm64"}, Catalog: &definition.Catalog{Base: "../../catalogs/nico"}, Components: []string{"nico-flow-image"}, Format: definition.OutputOCI}},
	"nico-prereqs":                       {"core", nvkeFamilyOnlyOutput("nico-prereqs", "linux/amd64", "nico", definition.OutputHelm, "nico-prereqs-chart", nil)},
	"nvke-node-fde-ubuntu24-amd64":       {"disks-ubuntu24", nvkeUbuntu24Disk("nvke-node-fde-ubuntu24-amd64", "linux/amd64", "nvke-node-fde-amd64")},
	"nvke-node-fde-ubuntu24-arm64":       {"disks-ubuntu24", nvkeUbuntu24Disk("nvke-node-fde-ubuntu24-arm64", "linux/arm64", "nvke-node-fde-arm64")},
	"nvke-node-image-ubuntu24-amd64":     {"disks-ubuntu24", nvkeUbuntu24Disk("nvke-node-image-ubuntu24-amd64", "linux/amd64", "nvke-node-image-amd64")},
	"nvke-node-image-ubuntu24-arm64":     {"disks-ubuntu24", nvkeUbuntu24Disk("nvke-node-image-ubuntu24-arm64", "linux/arm64", "nvke-node-image")},
}

// TestNVKEUbuntu24DisksMatchDiagnostics binds each maintained Ubuntu 24.04 disk to
// the build-only diagnostic selection whose 24.04 producers, ISO and Packer series
// the lifecycle tests resolve (nvke_ubuntu24_test.go), so that coverage applies here.
func TestNVKEUbuntu24DisksMatchDiagnostics(t *testing.T) {
	nvkeSplitRepository(t)
	family := nvkeSplitDefinition(t, "inputs/definitions/nvke/disks-ubuntu24.yaml")
	if len(family.Outputs) != 4 {
		t.Fatalf("Ubuntu 24.04 disks family has %d outputs, want 4", len(family.Outputs))
	}
	for _, output := range family.Outputs {
		t.Run(output.Name, func(t *testing.T) {
			variant := strings.Replace(strings.TrimPrefix(output.Name, "nvke-node-"), "-ubuntu24", "", 1)
			diagnostic := nvkeSplitDefinition(t, "inputs/definitions/diagnostics/ubuntu24/definition-github-"+variant+".yaml")
			if len(diagnostic.Outputs) != 1 || diagnostic.Outputs[0].Catalog == nil {
				t.Fatalf("diagnostic %s does not select one catalog-scoped output", variant)
			}
			// The diagnostic Definition sits one directory deeper, so its catalog
			// paths carry one extra "../" to reach the same inputs/catalogs files.
			want := diagnostic.Outputs[0]
			stack := *want.Catalog
			stack.Base = strings.TrimPrefix(stack.Base, "../")
			stack.Overlays = nil
			for _, overlay := range want.Catalog.Overlays {
				stack.Overlays = append(stack.Overlays, strings.TrimPrefix(overlay, "../"))
			}
			want.Catalog = &stack
			if !bytes.Equal(nvkeSplitCanonical(t, output), nvkeSplitCanonical(t, want)) {
				t.Fatalf("maintained %s diverges from its diagnostic selection", output.Name)
			}
		})
	}
}

// nvkeUbuntu24Disk is a native disk with its complete material closure, selected
// through the release node-image stack plus the Ubuntu 24 overlay.
func nvkeUbuntu24Disk(name, platform, root string) definition.DefinitionOutput {
	installer, patched := "ubuntu-installer-iso", "nvke-patched-installer-iso"
	if platform == "linux/amd64" {
		installer, patched = installer+"-amd64", patched+"-amd64"
	}
	return definition.DefinitionOutput{
		Name:     name,
		Platform: platform,
		Catalog: &definition.Catalog{Base: "../../catalogs/node-images/release", Overlays: []string{
			"../../catalogs/node-images/variants/native-toolchains.yaml", "../../catalogs/node-images/variants/ubuntu24.json"}},
		Format: definition.OutputDiskImage,
		Components: []string{"nvke-agent", "nvke-cni-calico", "nvke-cni-ovn", "nvke-doca-ovs", "nvke-image-inputs",
			"nvke-kubeadm-distro", root, patched, "nvke-spire-agent", "ovn-kube-ubuntu", "ovn-kubernetes-chart", installer},
	}
}

func nvkeFamilyOnlyOutput(name, platform, catalog, format, component string, images map[string]string) definition.DefinitionOutput {
	output := definition.DefinitionOutput{
		Name:       name,
		Platform:   platform,
		Catalog:    &definition.Catalog{Base: "../../catalogs/" + catalog},
		Components: []string{component},
		Format:     format,
	}
	if images != nil {
		output.Helm = &definition.HelmOutput{Images: images}
	}
	return output
}

// Family-only catalogs declare no pin set to revalidate, so they load from disk:
// these public catalogs, plus each pinned family-only overlay stack.
var nvkeFamilyOnlyCatalogs = map[string]bool{"../../catalogs/local-path-provisioner": true, "../../catalogs/nico": true, "../../catalogs/tools/operator-arm64": true}

func TestNVKEDefinitionSplitPreservesCompleteMembership(t *testing.T) {
	nvkeSplitRepository(t)
	combined := nvkeSplitDefinition(t, "inputs/definitions/nvke/definition.yaml")
	original := make(map[string][]byte, len(combined.Outputs))
	for _, output := range combined.Outputs {
		original[output.Name] = nvkeSplitCanonical(t, output)
	}
	wantOutputs, wantNative := nvkeSplitExpectedTotals(combined)
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
				if want, ok := nvkeFamilyOnlyOutputs[output.Name]; ok {
					if want.family != family.name {
						t.Fatalf("family-only output %q belongs to %s, not %s", output.Name, want.family, family.name)
					}
					if !bytes.Equal(nvkeSplitCanonical(t, output), nvkeSplitCanonical(t, want.output)) {
						t.Fatalf("family-only output %q changed its declared selection", output.Name)
					}
					if _, combinedAlso := original[output.Name]; combinedAlso {
						t.Fatalf("family-only output %q is also in the combined Definition", output.Name)
					}
					native += max(1, len(output.Platforms))
					continue
				}
				// The whole declaration includes catalog/overlay selection, exact
				// material membership, native platforms and chart image bindings.
				if !bytes.Equal(nvkeSplitCanonical(t, output), original[output.Name]) {
					t.Fatalf("output %q is added or changes the combined selection", output.Name)
				}
				if (output.Format == definition.OutputDiskImage) != nvkeDiskFamily(family.name) {
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
	for name, replacement := range nvkeCombinedOnlyOutputs {
		if family, exists := seen[name]; exists {
			t.Fatalf("combined-only output %q is required by %s", name, family)
		}
		pinned, ok := nvkeFamilyOnlyOutputs[replacement]
		if !ok {
			t.Fatalf("combined-only %q names undeclared family output %q", name, replacement)
		}
		want := pinned.output
		want.Name = name
		if !bytes.Equal(nvkeSplitCanonical(t, want), original[name]) {
			t.Fatalf("%q no longer delivers the selection of combined-only %q", replacement, name)
		}
	}
	if len(seen) != wantOutputs || nativeTotal != wantNative {
		t.Fatalf("partition requires %d outputs and %d native targets; want %d and %d", len(seen), nativeTotal, wantOutputs, wantNative)
	}
}

// nvkeSplitExpectedTotals derives the family aggregate from the authored combined
// Definition, less its combined-only outputs, plus the pinned family-only declarations.
func nvkeSplitExpectedTotals(combined definition.Definition) (outputs, native int) {
	for _, output := range combined.Outputs {
		if _, combinedOnly := nvkeCombinedOnlyOutputs[output.Name]; !combinedOnly {
			outputs++
			native += max(1, len(output.Platforms))
		}
	}
	for _, only := range nvkeFamilyOnlyOutputs {
		native += max(1, len(only.output.Platforms))
	}
	return outputs + len(nvkeFamilyOnlyOutputs), native
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
			// Each family's workflow fits flat; only the combined producer partitions.
			want.GitHubExecutionPartitions = false
			sources := map[string]bool{"https://github.com/dsx-ai-factory/nvke": true}
			if nvkeDiskFamily(family.name) {
				// Disks also build the release catalog's NVIDIA OVN fork producers.
				sources = map[string]bool{"https://github.com/dsx-ai-factory/nvke.git": true, "https://github.com/dsx-ai-factory/ovn-kubernetes": true}
			} else {
				want.GitHubARC, want.DiskEvidence = "", ""
				want.ExecutionClasses, want.DiskInspectionProfiles = nil, nil
			}
			for selected := range want.SourceAccess {
				if !sources[selected] {
					delete(want.SourceAccess, selected)
				}
			}
			// Services still needs the private NVKE catalog pin-set credential,
			// even when every selected image/chart is acquired rather than built.
			if len(want.SourceAccess) != len(sources) || !bytes.Equal(nvkeSplitCanonical(t, env), nvkeSplitCanonical(t, want)) {
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
				// Multi-platform outputs name each native build <output>/<os>/<arch>.
				if _, familyOnly := nvkeFamilyOnlyOutputs[strings.SplitN(build.Name, "/", 2)[0]]; familyOnly {
					continue
				}
				if !bytes.Equal(nvkeSplitCanonical(t, build), builds[build.Name]) {
					t.Fatalf("native build %q changed its identity, catalog or complete producer graph", build.Name)
				}
			}
			for _, output := range plan.LifecycleOutputs {
				if _, familyOnly := nvkeFamilyOnlyOutputs[output.Name]; familyOnly {
					continue
				}
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
			if !nvkeDiskFamily(family.name) {
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
	if wantOutputs, wantNative := nvkeSplitExpectedTotals(combined.Definition); buildCount != wantNative || outputCount != wantOutputs {
		t.Fatalf("compiled families retain %d native builds and %d outputs, want %d and %d", buildCount, outputCount, wantNative, wantOutputs)
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
		if !ok && (nvkeFamilyOnlyCatalogs[spec.Base] && len(spec.Overlays) == 0 || nvkeFamilyOnlyStack(t, spec)) {
			return loadFactoryCatalogStack(ctx, "inputs/definitions/nvke/core.yaml", spec)
		}
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "split selected a catalog outside the combined baseline")
		}
		return stack, nil
	}
}

// nvkeFamilyOnlyStack admits exactly a pinned family-only output's catalog stack.
func nvkeFamilyOnlyStack(t *testing.T, spec definition.Catalog) bool {
	t.Helper()
	for _, only := range nvkeFamilyOnlyOutputs {
		if only.output.Catalog != nil && len(only.output.Catalog.Overlays) != 0 &&
			bytes.Equal(nvkeSplitCanonical(t, *only.output.Catalog), nvkeSplitCanonical(t, spec)) {
			return true
		}
	}
	return false
}

func nvkeSplitRepository(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

// nvkeDiskFamily reports whether a family owns native disk execution; each Ubuntu
// series is a sibling disk family, not an exception to the other.
func nvkeDiskFamily(family string) bool {
	return family == "disks" || family == "disks-ubuntu24"
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
