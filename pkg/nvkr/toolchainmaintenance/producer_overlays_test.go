// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalogmaintenance"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestRepinPreservesGeneratedProducerOverlayInputs(t *testing.T) {
	isolateGitEnvironment(t)
	root := t.TempDir()
	t.Chdir(root)
	const base = "inputs/catalogs/base"
	const destination = "inputs/catalogs/variants/frozen.json"
	const policy = "inputs/policies/ovn-producer-overlays.json"
	oldImage := Registry + "/packer-native@" + digest("old-arm")
	oldLocal := digest("old-local-amd")
	writeJSON(t, filepath.Join(base, catalog.RegistryFile), map[string]any{
		"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
		"sources":    map[string]any{"tool": map[string]any{"repo": "https://example.invalid/tool", "toolchainImagesByArch": map[string]string{"amd64": oldLocal, "arm64": oldImage}}},
		"components": map[string]any{"tool": map[string]any{"fromSource": true, "source": "tool", "platforms": []string{"linux/amd64", "linux/arm64"}, "outputType": "host-package", "artifactPath": "tool", "buildCommand": []string{"go", "build", "-o", "tool", "."}}},
	})
	writeJSON(t, filepath.Join(base, catalog.LockFile), catalog.PinLock{
		APIVersion: "nvkr.dev/lock/v1", Kind: "PinLock", Pins: map[string]catalog.Pin{
			"tool": {Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDateEpoch: 1},
		},
	})
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(base))
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	component := cat.Components["tool"]
	frozen := map[string]catalog.ComponentReplacement{"tool": {Component: &component}}
	writeJSON(t, destination, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "frozen", RequiresCatalogDigest: baseDigest, Components: frozen})
	writeJSON(t, policy, catalogmaintenance.ProducerOverlayInputs{
		APIVersion: "nvkr.dev/producer-overlay-maintenance/v1",
		Overlays:   []catalogmaintenance.ProducerOverlayInput{{Name: "frozen", Destination: destination, Base: base, Frozen: frozen}},
	})
	const nativePath = "inputs/catalogs/variants/native.json"
	writeJSON(t, nativePath, catalog.Overlay{
		APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "native", RequiresCatalogDigest: baseDigest,
		ToolchainSubstitutions: []catalog.ToolchainSubstitution{{Arch: "amd64", From: oldLocal, To: Registry + "/packer-native@" + digest("old-amd"), Components: []string{"tool"}}},
	})
	if err := catalogmaintenance.GenerateProducerOverlays(t.Context(), policy, false); err != nil {
		t.Fatal(err)
	}
	if err := catalogmaintenance.GenerateProducerOverlays(t.Context(), policy, true); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, localToolchainBindings, []factory.LocalToolchainBinding{{Catalog: "base", Arch: "arm64", From: oldImage, Components: []string{"tool"}}})
	// Exceptions and unrelated policies must never be rewritten with consumer pins.
	const approval = "inputs/policies/toolchain-maintenance/123.json"
	writeJSON(t, approval, map[string]string{"image": oldImage, "localImage": oldLocal})
	before, err := os.ReadFile(approval)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "inputs", "toolchains"}} {
		if _, err := command(t.Context(), root, "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	r := recordFixture()
	if _, err := repin(t.Context(), root, r, true); err != nil {
		t.Fatal(err)
	}
	if err := catalogmaintenance.GenerateProducerOverlays(t.Context(), policy, true); err != nil {
		t.Fatalf("repin left generated overlays inconsistent with their authored inputs: %v", err)
	}
	if err := catalogmaintenance.GenerateProducerOverlays(t.Context(), policy, false); err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	image, err := selectedImage(r, "arm64", "packer")
	if err != nil || overlay.Components["tool"].Component.ToolchainImages["arm64"] != image.Reference() {
		t.Fatalf("owner regeneration reverted the selected toolchain: %v", err)
	}
	bindings, err := factory.LoadLocalToolchainBindings(t.Context(), localToolchainBindings)
	if err != nil || len(bindings) != 1 || bindings[0].From != image.Reference() {
		t.Fatalf("local bindings retained an old predecessor: %v", err)
	}
	local, err := selectedImage(r, "amd64", "packer")
	if err != nil || overlay.Components["tool"].Component.ToolchainImages["amd64"] != local.ImageID {
		t.Fatalf("frozen producer retained an old local configuration ID: %v", err)
	}
	updated, err := catalog.LoadContext(t.Context(), catalog.Dir(base))
	if err != nil || updated.Components["tool"].ToolchainImages["amd64"] != local.ImageID {
		t.Fatalf("base producer retained an old local configuration ID: %v", err)
	}
	native, err := catalog.LoadOverlayContext(t.Context(), nativePath)
	if err != nil {
		t.Fatal(err)
	}
	if native.ToolchainSubstitutions[0].From != local.ImageID || native.ToolchainSubstitutions[0].To != local.Reference() {
		t.Fatal("native substitution does not bind the approved configuration and manifest")
	}
	if _, err := catalog.ApplyStack(updated, native); err != nil {
		t.Fatalf("native substitution no longer applies to the local producer: %v", err)
	}
	after, err := os.ReadFile(approval)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("repin rewrote exception policy: %v", err)
	}
}

func TestRepinRejectsAmbiguousLocalImageBinding(t *testing.T) {
	isolateGitEnvironment(t)
	root := t.TempDir()
	path := filepath.Join(root, "inputs/catalogs/variants/native.json")
	oldLocal := digest("shared-local-id")
	writeJSON(t, path, catalog.Overlay{
		APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "native", RequiresCatalogDigest: digest("base"),
		ToolchainSubstitutions: []catalog.ToolchainSubstitution{
			{Arch: "amd64", From: oldLocal, To: Registry + "/packer-native@" + digest("old-amd"), Components: []string{"tool"}},
			{Arch: "arm64", From: oldLocal, To: Registry + "/packer-native@" + digest("old-arm"), Components: []string{"tool"}},
		},
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "inputs"}} {
		if _, err := command(t.Context(), root, "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	_, err = repin(t.Context(), root, recordFixture(), true)
	assertCode(t, err, e.CodeAmbiguous)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("ambiguous binding changed an input: %v", err)
	}
}

func TestRepinProducerOverlayPolicyVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		valid         bool
	}{
		{"supported", "nvkr.dev/producer-overlay-maintenance/v1", true},
		{"unsupported", "nvkr.dev/producer-overlay-maintenance/v999", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateGitEnvironment(t)
			root := t.TempDir()
			const base = "inputs/catalogs/base"
			const registry = base + "/registry.yaml"
			const overlay = "inputs/catalogs/variants/frozen.json"
			oldImage := Registry + "/packer-native@" + digest("old-arm")
			writeJSON(t, filepath.Join(root, registry), map[string]any{
				"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
				"sources":    map[string]any{"tool": map[string]any{"repo": "https://example.invalid/tool", "toolchainImagesByArch": map[string]string{"arm64": oldImage}}},
				"components": map[string]any{"tool": map[string]any{"fromSource": true, "source": "tool", "platforms": []string{"linux/arm64"}, "outputType": "host-package", "artifactPath": "tool", "buildCommand": []string{"go", "build", "-o", "tool", "."}}},
			})
			writeJSON(t, filepath.Join(root, base, catalog.LockFile), catalog.PinLock{
				APIVersion: "nvkr.dev/lock/v1", Kind: "PinLock", Pins: map[string]catalog.Pin{
					"tool": {Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDateEpoch: 1},
				},
			})
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, base)))
			if err != nil {
				t.Fatal(err)
			}
			baseDigest, err := cat.SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			component := cat.Components["tool"]
			frozen := map[string]catalog.ComponentReplacement{"tool": {Component: &component}}
			writeJSON(t, filepath.Join(root, overlay), catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "frozen", RequiresCatalogDigest: baseDigest, Components: frozen})
			writeJSON(t, filepath.Join(root, producerOverlayPolicy), catalogmaintenance.ProducerOverlayInputs{
				APIVersion: tc.version,
				Overlays:   []catalogmaintenance.ProducerOverlayInput{{Name: "frozen", Destination: overlay, Base: base, Frozen: frozen}},
			})
			before := map[string][]byte{}
			for _, name := range []string{registry, overlay, producerOverlayPolicy} {
				raw, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				before[name] = raw
			}
			for _, args := range [][]string{{"init", "--quiet"}, {"add", "inputs"}} {
				if _, err := command(t.Context(), root, "git", args...); err != nil {
					t.Fatal(err)
				}
			}
			indexBefore, err := command(t.Context(), root, "git", "write-tree")
			if err != nil {
				t.Fatal(err)
			}
			changed, err := repin(t.Context(), root, recordFixture(), true)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				for name := range before {
					if !slices.Contains(changed, name) {
						t.Fatalf("supported policy did not refresh %s", name)
					}
				}
				return
			}
			assertCode(t, err, e.CodeInvalidCatalog)
			for name, raw := range before {
				after, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || !bytes.Equal(raw, after) {
					t.Fatalf("unsupported policy changed %s: %v", name, err)
				}
			}
			indexAfter, err := command(t.Context(), root, "git", "write-tree")
			if err != nil || !bytes.Equal(indexBefore, indexAfter) {
				t.Fatalf("unsupported policy changed staged inputs: %v", err)
			}
		})
	}
}
