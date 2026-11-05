// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The ADR-26 worked example lives beside the families but is not an authored
// input; tests admit it from memory through the real bounded reader.
const nvkeCompositePath = "inputs/definitions/nvke/composite.yaml"

// nvkeCompositeFamilies are the worked example's members. The Ubuntu 24.04 disk
// family shares component names with the Ubuntu 26.04 one at different pins, so
// ADR-26 refuses it as a separate member unless the composite declares those
// components as variants of the two disk families.
var nvkeCompositeFamilies = []string{"core", "disks", "services", "telemetry"}

const nvkeCompositeExcludedFamily = "disks-ubuntu24"

// nvkeUbuntuSeriesVariants are the components the Ubuntu 24.04 overlay replaces,
// sorted as a composite declares them.
var nvkeUbuntuSeriesVariants = []string{"nvke-agent", "nvke-doca-ovs", "nvke-node-fde-amd64", "nvke-node-fde-arm64",
	"nvke-node-image", "nvke-node-image-amd64", "ubuntu-installer-iso", "ubuntu-installer-iso-amd64"}

func nvkeCompositeMember(t *testing.T, family string) authored.CompositeMember {
	t.Helper()
	path, _ := nvkeSplitPaths(family)
	digest, err := nvkeSplitDefinition(t, path).Digest()
	if err != nil {
		t.Fatal(err)
	}
	return authored.CompositeMember{Name: "nvke-" + family, Definition: family + ".yaml", Digest: digest}
}

// nvkeCompositeExcludedOutputs names the family-only outputs the worked example omits.
func nvkeCompositeExcludedOutputs(t *testing.T) map[string]bool {
	t.Helper()
	path, _ := nvkeSplitPaths(nvkeCompositeExcludedFamily)
	excluded := map[string]bool{}
	for _, output := range nvkeSplitDefinition(t, path).Outputs {
		excluded[output.Name] = true
	}
	return excluded
}

func nvkeComposite(t *testing.T, extra ...authored.CompositeMember) authored.CompositeDefinition {
	t.Helper()
	composite := authored.CompositeDefinition{APIVersion: authored.CompositeAPIVersion, Kind: authored.CompositeKind,
		Metadata: authored.Metadata{Name: "nvke-composite"}}
	for _, family := range nvkeCompositeFamilies {
		composite.Members = append(composite.Members, nvkeCompositeMember(t, family))
	}
	composite.Members = append(composite.Members, extra...)
	slices.SortFunc(composite.Members, func(a, b authored.CompositeMember) int { return strings.Compare(a.Name, b.Name) })
	return composite
}

func admitNVKEComposite(t *testing.T, composite authored.CompositeDefinition) (authored.Composition, error) {
	t.Helper()
	return authored.AdmitComposite(t.Context(), nvkeCompositePath, mustCanonical(t, composite), authored.RootReader("."))
}

func TestNVKECompositeAdmission(t *testing.T) {
	nvkeSplitRepository(t)
	combined := nvkeSplitDefinition(t, "inputs/definitions/nvke/definition.yaml")
	combinedDigest, err := combined.Digest()
	if err != nil {
		t.Fatal(err)
	}
	composition, err := admitNVKEComposite(t, nvkeComposite(t))
	if err != nil {
		t.Fatal(err)
	}
	effective, want := composition.Effective(), len(combined.Outputs)-len(nvkeCombinedOnlyOutputs)+len(nvkeFamilyOnlyOutputs)-len(nvkeCompositeExcludedOutputs(t))
	if len(composition.Members()) != len(nvkeCompositeFamilies) || len(effective.Outputs) != want || len(composition.Attribution()) != want {
		t.Fatalf("composite admits %d members and %d outputs, want %d and %d", len(composition.Members()), len(effective.Outputs), len(nvkeCompositeFamilies), want)
	}
	if !bytes.Equal(mustCanonical(t, effective.Catalog), mustCanonical(t, combined.Catalog)) {
		t.Fatal("rebasing beside the families changed the shared catalog stack")
	}
	for _, tc := range []struct {
		name, message string
		extra         authored.CompositeMember
		mutate        func(*authored.CompositeDefinition)
		code          e.Code
	}{
		{name: "combined Definition as extra member", code: e.CodeInvalidCatalog, message: "is declared by members",
			extra: authored.CompositeMember{Name: combined.Metadata.Name, Definition: "definition.yaml", Digest: combinedDigest}},
		{name: "stale family pin", code: e.CodeUnverifiedRef, message: "run definition pin", mutate: func(composite *authored.CompositeDefinition) {
			composite.Members[3].Digest = combinedDigest
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			composite := nvkeComposite(t)
			if tc.extra.Name != "" {
				composite = nvkeComposite(t, tc.extra)
			}
			if tc.mutate != nil {
				tc.mutate(&composite)
			}
			_, err := admitNVKEComposite(t, composite)
			assertFactoryErrorCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("refused for an unexpected reason: %v", err)
			}
		})
	}
}

// Identity invariance remains opt-in with the split contract: compiling the
// composite must keep every family and combined output and native build record.
func TestNVKECompositeIdentityInvariance(t *testing.T) {
	testutil.RequireExtended(t)
	nvkeSplitRepository(t)
	combined, err := LoadPlanContext(t.Context(), FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	loadStack := nvkeSplitCatalogLoader(t, combined)
	composition, err := admitNVKEComposite(t, nvkeComposite(t))
	if err != nil {
		t.Fatal(err)
	}
	environment := nvkeSplitEnvironment(t, "inputs/environments/github-release.yaml")
	environment.Metadata = authored.Metadata{Name: "nvke-composite-github"}
	environment.GitHubBundle, environment.GitHubExecutionPartitions = "nvke-composite", true
	environment.Publication.RepositoryPrefix += "/composite"
	environment.Signing.Identity = "^" + regexp.QuoteMeta("https://github.com/dsx-ai-factory/nvkr/.github/workflows/nvkr-run-nvke-composite.yml") + "@refs/heads/main$"
	plan, err := compileComposition(t.Context(), composition, "inputs/environments/github-release.yaml", environment, combined.GeneratorVersion, loadStack)
	if err != nil {
		t.Fatal(err)
	}
	// Both Ubuntu disk series as separate members select divergent producers
	// for the same components, which ADR-26 refuses at compilation unless the
	// composite declares each of them as a variant of the two disk families.
	series := nvkeComposite(t, nvkeCompositeMember(t, nvkeCompositeExcludedFamily))
	bothSeries, err := admitNVKEComposite(t, series)
	if err != nil {
		t.Fatal(err)
	}
	_, err = compileComposition(t.Context(), bothSeries, "inputs/environments/github-release.yaml", environment, combined.GeneratorVersion, loadStack)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	if !strings.Contains(err.Error(), "select divergent producers") {
		t.Fatalf("refused for an unexpected reason: %v", err)
	}
	for _, component := range nvkeUbuntuSeriesVariants {
		series.Variants = append(series.Variants, authored.DeclaredVariant{Component: component, Members: []string{"nvke-disks", "nvke-" + nvkeCompositeExcludedFamily}})
	}
	declared, err := admitNVKEComposite(t, series)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compileComposition(t.Context(), declared, "inputs/environments/github-release.yaml", environment, combined.GeneratorVersion, loadStack); err != nil {
		t.Fatalf("both Ubuntu series with declared variants: %v", err)
	}
	excluded := nvkeCompositeExcludedOutputs(t)
	if want := len(combined.Definition.Outputs) - len(nvkeCombinedOnlyOutputs) + len(nvkeFamilyOnlyOutputs) - len(excluded); len(plan.LifecycleOutputs) != want || len(plan.Composition.Members) != len(nvkeCompositeFamilies) {
		t.Fatalf("composite Plan has %d outputs, want %d", len(plan.LifecycleOutputs), want)
	}
	// The combined Definition lacks family-only outputs, so the plain baseline is the effective Definition.
	partitioned, err := compileDocumentsWithLoader(t.Context(), nvkeCompositePath, composition.Effective(), "inputs/environments/github-release.yaml",
		environment, combined.GeneratorVersion, loadStack)
	if err != nil {
		t.Fatal(err)
	}
	assertPartitionsIgnoreMembers(t, partitioned, plan)
	baselines, familyBuilds := []*Plan{combined}, 0
	for _, family := range nvkeSplitFamilies {
		if family.name == nvkeCompositeExcludedFamily {
			continue
		}
		definitionPath, environmentPath := nvkeSplitPaths(family.name)
		familyPlan, err := compileDocumentsWithLoader(t.Context(), definitionPath, nvkeSplitDefinition(t, definitionPath), environmentPath,
			nvkeSplitEnvironment(t, environmentPath), combined.GeneratorVersion, loadStack)
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range familyPlan.LifecycleOutputs {
			if member, _ := plan.Composition.MemberOf(output.Name); member != "nvke-"+family.name {
				t.Fatalf("output %q is attributed to %q, not nvke-%s", output.Name, member, family.name)
			}
		}
		baselines, familyBuilds = append(baselines, familyPlan), familyBuilds+len(familyPlan.Builds)
	}
	if len(plan.Builds) != familyBuilds {
		t.Fatalf("composite Plan has %d native builds, families have %d", len(plan.Builds), familyBuilds)
	}
	builds := map[string][]byte{}
	outputs := map[string][]byte{}
	for _, build := range plan.Builds {
		builds[build.Name] = mustCanonical(t, build)
	}
	for _, output := range plan.LifecycleOutputs {
		outputs[output.Name] = mustCanonical(t, output)
	}
	for _, baseline := range baselines {
		for _, build := range baseline.Builds {
			output, _, _ := strings.Cut(build.Name, "/")
			if _, combinedOnly := nvkeCombinedOnlyOutputs[output]; excluded[output] || combinedOnly {
				continue
			}
			if !bytes.Equal(mustCanonical(t, build), builds[build.Name]) {
				t.Fatalf("composition changed native build %q relative to %s", build.Name, baseline.Name)
			}
		}
		for _, output := range baseline.LifecycleOutputs {
			if _, combinedOnly := nvkeCombinedOnlyOutputs[output.Name]; excluded[output.Name] || combinedOnly {
				continue
			}
			if !bytes.Equal(mustCanonical(t, output), outputs[output.Name]) {
				t.Fatalf("composition changed output %q relative to %s", output.Name, baseline.Name)
			}
		}
	}
}

// assertPartitionsIgnoreMembers requires the composite's worker partitions to
// hold exactly the jobs of the same outputs compiled as one plain Definition:
// partitions follow dependency depth and capacity, never member boundaries.
func assertPartitionsIgnoreMembers(t *testing.T, plain, composite *Plan) {
	t.Helper()
	partitionJobs := func(plan *Plan) [][]string {
		bundle, err := RenderGitHub(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for name := range bundle.Files {
			if githubPartitionWorkflowSuffix.MatchString(name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		var partitions [][]string
		for _, name := range names {
			var jobs []string
			for id := range decodeGitHubWorkflow(t, bundle.Files[name]).Jobs {
				jobs = append(jobs, id)
			}
			slices.Sort(jobs)
			partitions = append(partitions, jobs)
		}
		return partitions
	}
	want, got := partitionJobs(plain), partitionJobs(composite)
	if len(got) < 2 || len(got) != len(want) {
		t.Fatalf("composite has %d partitions, plain Definition has %d", len(got), len(want))
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("partition %d differs from the plain Definition's partition", i)
		}
	}
}
