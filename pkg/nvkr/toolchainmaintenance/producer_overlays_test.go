// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalogmaintenance"
)

func TestRepinPreservesGeneratedProducerOverlayInputs(t *testing.T) {
	isolateGitEnvironment(t)
	root := t.TempDir()
	t.Chdir(root)
	const base = "inputs/catalogs/base"
	const destination = "inputs/catalogs/variants/frozen.json"
	const policy = "inputs/policies/ovn-producer-overlays.json"
	oldImage := Registry + "/packer-native@" + digest("old-arm")
	writeJSON(t, filepath.Join(base, catalog.RegistryFile), map[string]any{
		"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
		"sources":    map[string]any{"tool": map[string]any{"repo": "https://example.invalid/tool", "toolchainImagesByArch": map[string]string{"arm64": oldImage}}},
		"components": map[string]any{"tool": map[string]any{"fromSource": true, "source": "tool", "platforms": []string{"linux/arm64"}, "outputType": "host-package", "artifactPath": "tool", "buildCommand": []string{"go", "build", "-o", "tool", "."}}},
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
	if err := catalogmaintenance.GenerateProducerOverlays(t.Context(), policy, false); err != nil {
		t.Fatal(err)
	}
	if err := catalogmaintenance.GenerateProducerOverlays(t.Context(), policy, true); err != nil {
		t.Fatal(err)
	}
	// Exceptions and unrelated policies must never be rewritten with consumer pins.
	const approval = "inputs/policies/toolchain-maintenance/123.json"
	writeJSON(t, approval, map[string]string{"image": oldImage})
	before, err := os.ReadFile(approval)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "inputs"}} {
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
	after, err := os.ReadFile(approval)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("repin rewrote exception policy: %v", err)
	}
}
