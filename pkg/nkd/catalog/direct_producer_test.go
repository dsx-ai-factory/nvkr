// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestLegacyRecipeInputsFailClosed(t *testing.T) {
	for _, legacy := range []string{
		"recipes: []\n", "layers: {}\n", "criteria: {}\n",
		"base: node\n", "mixins: []\n",
	} {
		t.Run(strings.TrimSpace(legacy), func(t *testing.T) {
			provider := bytesProvider{RegistryFile: []byte(lockTestRegistry + legacy)}
			_, err := ReadSourcesContext(context.Background(), provider)
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
		})
	}
	legacy := strings.Replace(lockTestRegistry, "nkd.dev/catalog/v3", "nkd.dev/catalog/v1", 1)
	_, err := ReadSourcesContext(context.Background(), bytesProvider{RegistryFile: []byte(legacy)})
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
}

func TestRemovedClassificationFieldsFailBeforePinReads(t *testing.T) {
	for _, field := range []string{"category: os", "roles: [worker]"} {
		t.Run(field, func(t *testing.T) {
			registry := strings.Replace(lockTestRegistry, "x: {platforms:", "x: {"+field+", platforms:", 1)
			// The absent lock proves strict schema admission rejects the old field
			// before pin loading can produce a different failure.
			provider := bytesProvider{RegistryFile: []byte(registry)}
			cat, err := LoadContext(context.Background(), provider)
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if cat != nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("retired field admitted or did not fail strict decoding: %+v, %v", cat, err)
			}
			_, err = ReadSourcesContext(context.Background(), provider)
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
		})
	}
	legacy := strings.Replace(lockTestRegistry, "nkd.dev/catalog/v3", "nkd.dev/catalog/v2", 1)
	_, err := ReadSourcesContext(context.Background(), bytesProvider{RegistryFile: []byte(legacy)})
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
}

func TestLegacyCompositionLayoutRejectsBeforePinUpdates(t *testing.T) {
	for _, directory := range []string{"overlays", "mixins"} {
		for _, symlink := range []bool{false, true} {
			name := directory
			if symlink {
				name += " symlink"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, RegistryFile), []byte(lockTestRegistry), 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, directory)
				if symlink {
					if err := os.Symlink(t.TempDir(), path); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, "old.yaml"), []byte("kind: RecipeMetadata\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				_, err := LoadContext(context.Background(), Dir(root))
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
				_, err = ReadSourcesContext(context.Background(), Dir(root))
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
				_, err = LoadLockContext(context.Background(), root)
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
				if _, err := os.Stat(filepath.Join(root, LockFile)); !os.IsNotExist(err) {
					t.Fatalf("rejected legacy catalog produced lock: %v", err)
				}
			})
		}
	}
}

func TestArtifactPinTargetsComeFromProducerPlatforms(t *testing.T) {
	registry := strings.Replace(lockTestRegistry,
		"x: {platforms: [linux/amd64, linux/arm64], fromSource: false, exclusionReason: test-only}",
		"x: {platforms: [linux/amd64], source: s, outputType: raw, prebuilt: {url: https://example.invalid/x, evidence: {none: true}}}", 1)
	lock := lockYAML("  s: {tag: v1, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("b", 40) + ", sourceDateEpoch: 1}\n")
	for _, test := range []struct {
		name, digests string
		code          e.Code
	}{
		{"exact target", "amd64: " + goodArtifactDigest, ""},
		{"missing target", "arm64: " + goodArtifactDigest, e.CodeUnpinnedSource},
		{"undeclared extra target", "amd64: " + goodArtifactDigest + ", arm64: " + goodArtifactDigest, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			withArtifacts := string(lock) + "artifacts:\n  x: {tag: v1, digests: {" + test.digests + "}}\n"
			_, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(registry), LockFile: []byte(withArtifacts)})
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, test.code)
			}
		})
	}
}
