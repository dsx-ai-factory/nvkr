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

func nvkeComposite(t *testing.T, extra ...authored.CompositeMember) authored.CompositeDefinition {
	t.Helper()
	composite := authored.CompositeDefinition{APIVersion: authored.CompositeAPIVersion, Kind: authored.CompositeKind,
		Metadata: authored.Metadata{Name: "nvke-composite"}}
	for _, family := range []string{"core", "disks", "services", "telemetry"} {
		path, _ := nvkeSplitPaths(family)
		digest, err := nvkeSplitDefinition(t, path).Digest()
		if err != nil {
			t.Fatal(err)
		}
		composite.Members = append(composite.Members, authored.CompositeMember{Name: "nvke-" + family, Definition: family + ".yaml", Digest: digest})
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
	effective := composition.Effective()
	if len(composition.Members()) != 4 || len(effective.Outputs) != 69 || len(composition.Attribution()) != 69 {
		t.Fatalf("composite admits %d members and %d outputs, want 4 and 69", len(composition.Members()), len(effective.Outputs))
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
		{name: "combined Definition as fifth member", code: e.CodeInvalidCatalog, message: "is declared by members",
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
	if len(plan.LifecycleOutputs) != 69 || len(plan.Builds) != 73 || len(plan.Composition.Members) != 4 {
		t.Fatalf("composite Plan has %d outputs and %d native builds, want 69 and 73", len(plan.LifecycleOutputs), len(plan.Builds))
	}
	baselines := []*Plan{combined}
	for _, family := range nvkeSplitFamilies {
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
		baselines = append(baselines, familyPlan)
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
			if !bytes.Equal(mustCanonical(t, build), builds[build.Name]) {
				t.Fatalf("composition changed native build %q relative to %s", build.Name, baseline.Name)
			}
		}
		for _, output := range baseline.LifecycleOutputs {
			if !bytes.Equal(mustCanonical(t, output), outputs[output.Name]) {
				t.Fatalf("composition changed output %q relative to %s", output.Name, baseline.Name)
			}
		}
	}
}
