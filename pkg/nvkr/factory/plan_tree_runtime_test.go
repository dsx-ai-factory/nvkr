// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

// Exercise the actual maintained selections with synthetic pin changes. This
// proves storage/refresh parity, not GitHub execution or source availability.
func TestPlanTreeMaintainedRuntimeRefresh(t *testing.T) {
	for _, name := range []string{"nvke-nightly-arm64", "nvke-nightly-gpu-site", "nvke-nightly-upstream-ovn-arm64"} {
		t.Run(name, func(t *testing.T) {
			before, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-run-"+name+"/plan.json")
			if err != nil {
				t.Fatal(err)
			}
			if before.RuntimeInputs != nil || before.RuntimeSourceCommit != "" {
				t.Fatal("maintained baseline unexpectedly contains dispatch-frozen inputs")
			}
			rootStack := repinPlanTreeStack(t, before.CatalogSnapshot, before.AppliedOverlays)
			stacks := make(map[string]*catalog.Stack)
			for _, build := range before.Builds {
				stacks[build.Name] = repinPlanTreeStack(t, build.CatalogSnapshot, build.AppliedOverlays)
			}
			for _, output := range before.Definition.Outputs {
				selections := nativeOutputSelections(output)
				selected := stacks[selections[0].Name]
				for _, selection := range selections {
					if stacks[selection.Name].Digest != selected.Digest {
						t.Fatal("native targets disagree about their shared catalog")
					}
				}
				stacks[output.Name] = selected
			}
			after, err := compileWithTargetStacksFor(t.Context(), before.Definition, before.Environment,
				rootStack, stacks, before.GeneratorVersion, before.DefinitionPath, before.EnvironmentPath, before.Composition)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			baseline := writeGitHubRuntimeBaseline(t, root, before)
			fresh, err := RenderGitHub(t.Context(), after)
			if err != nil {
				t.Fatal(err)
			}
			if baseline.PlanDigest == fresh.PlanDigest {
				t.Fatal("synthetic source pins did not change the Plan")
			}
			for member, raw := range baseline.Files {
				if strings.HasSuffix(member, ".sh") && !bytes.Equal(raw, fresh.Files[member]) {
					t.Fatalf("pin-only refresh changed script %s", member)
				}
			}
			if err := PrepareGitHubRuntimeBundle(t.Context(), after, root, false); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, fresh.PlanPath))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, after)) {
				t.Fatal("refreshed tree changed compiled Plan bytes")
			}
		})
	}
}

func repinPlanTreeStack(t *testing.T, snapshot *catalog.Catalog, prior []catalog.AppliedOverlay) *catalog.Stack {
	t.Helper()
	base, err := catalog.ApplyStack(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
		Name: "test-source-pins", RequiresCatalogDigest: base.Digest, Components: map[string]catalog.ComponentReplacement{}}
	for name, component := range base.Catalog.Components {
		if !component.FromSource {
			continue
		}
		old := component.Commit
		component.Commit = nextGitHubStabilityPin(t, component.Commit)
		component.TreeHash = nextGitHubStabilityPin(t, component.TreeHash)
		component.SourceDate++
		component.VersionStamp = maps.Clone(component.VersionStamp)
		for key, value := range component.VersionStamp {
			component.VersionStamp[key] = strings.ReplaceAll(value, old, component.Commit)
		}
		overlay.Components[name] = catalog.ComponentReplacement{Component: &component}
	}
	if len(overlay.Components) == 0 {
		base.Applied = append([]catalog.AppliedOverlay(nil), prior...)
		return base
	}
	result, err := catalog.ApplyStack(base.Catalog, overlay)
	if err != nil {
		t.Fatal(err)
	}
	result.Applied = append(append([]catalog.AppliedOverlay(nil), prior...), result.Applied...)
	return result
}
