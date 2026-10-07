// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/release"
)

func TestDiagnosticPublicationMetadataIdentityAndExtraction(t *testing.T) {
	artifact := "sha256:" + strings.Repeat("a", 64)
	inventory := release.Inventory{SchemaVersion: release.InventorySchemaVersion, Version: "1",
		Components: []release.InventoryComponent{{Name: "node", RequiredArtifacts: []string{artifact},
			ArtifactKinds: []release.ArtifactKindBinding{{ArtifactDigest: artifact, Kind: release.ArtifactHostPackage}}}}}
	inventoryRaw, err := canonical.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	inventoryDigest, err := inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                 string
		mismatch, incomplete bool
		code                 e.Code
	}{
		{name: "exact metadata"},
		{name: "wrong inventory binding", mismatch: true, code: e.CodeInventoryUnpinned},
		{name: "missing release set", incomplete: true, code: e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := release.ReleaseSet{SchemaVersion: release.ReleaseSetSchemaVersion, Version: "1", InventoryRef: inventoryDigest,
				Members: []release.Member{{Component: "node", Arch: "amd64", BuildDigest: "sha256:" + strings.Repeat("b", 64),
					VectorDigest: "sha256:" + strings.Repeat("c", 64), ResolvedInputDigest: "sha256:" + strings.Repeat("d", 64), ArtifactDigests: []string{artifact}}}}
			if tc.mismatch {
				set.InventoryRef = artifact
			}
			setRaw, err := set.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			invPath, setPath, root := filepath.Join(dir, "inventory.json"), filepath.Join(dir, "set.json"), filepath.Join(dir, "publication")
			for path, raw := range map[string][]byte{invPath: inventoryRaw, setPath: setRaw} {
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"publish", "root", "--inventory", invPath, "--out", root}
			if !tc.incomplete {
				args = append(args, "--release-set", setPath)
			}
			output, err := invokeLifecycleCLI(t, args...)
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("rejected metadata created publication: %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(output, "UNQUALIFIED") {
				t.Fatalf("publication output = %q, %v", output, err)
			}
			output, err = invokeLifecycleCLI(t, "publish", "inspect", "--layout", root, "--json")
			if err != nil {
				t.Fatal(err)
			}
			var inspected delivery.PublicationRoot
			if err := json.Unmarshal([]byte(output), &inspected); err != nil {
				t.Fatal(err)
			}
			setDigest, err := set.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if inspected.InventoryDigest != inventoryDigest || inspected.ReleaseSetDigest != setDigest || len(inspected.Objects) != 2 || inspected.Qualification != delivery.QualificationDiagnostic {
				t.Fatalf("publication bindings = %+v", inspected)
			}
			for name, want := range map[string][]byte{"inventory": inventoryRaw, "release-set": setRaw} {
				out := filepath.Join(dir, "extracted-"+name)
				if _, err := invokeLifecycleCLI(t, "publish", "get", "--layout", root, "--object", name, "--out", out); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(out)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("extracted %s = %q, %v", name, got, err)
				}
			}
		})
	}
}
