// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestCommandFilesIdentityGoldens(t *testing.T) {
	const buildBody = "printf 'build\r\n'\n"
	const vendorBody = "printf 'vendor'\n\n"
	const wantBuild = "sha256:177960329d1123664be89f85535bc1f2ac4506b95569714935a95dbaf999531f"
	const wantVendor = "sha256:4855c2041b02fa3c3ba7d000f9a5c4e6fde3a9da23ed210938005ccb467539ad"
	for _, tc := range []struct {
		name                           string
		fileBacked, platform, fragment bool
		buildPath, vendorPath          string
	}{
		{name: "inline"},
		{name: "files", fileBacked: true, buildPath: "build.sh", vendorPath: "vendor.sh"},
		{name: "renamed", fileBacked: true, buildPath: "renamed-build.sh", vendorPath: "renamed-vendor.sh"},
		{name: "platform override", fileBacked: true, platform: true, buildPath: "build.sh", vendorPath: "vendor.sh"},
		{name: "sibling fragment platform", fileBacked: true, platform: true, fragment: true, buildPath: "build.sh", vendorPath: "vendor.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			catalogRoot, declarationRoot := root, root
			if tc.fragment {
				catalogRoot, declarationRoot = filepath.Join(root, "catalog"), filepath.Join(root, "shared")
				for _, dir := range []string{catalogRoot, declarationRoot} {
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			var build, vendor any = buildBody, vendorBody
			if tc.fileBacked {
				build, vendor = map[string]string{"file": tc.buildPath}, map[string]string{"file": tc.vendorPath}
				for name, body := range map[string]string{tc.buildPath: buildBody, tc.vendorPath: vendorBody} {
					if err := os.WriteFile(filepath.Join(declarationRoot, name), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			registry := map[string]any{
				"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
				"sources":    map[string]any{"source": map[string]any{"repo": "https://example.test/source", "toolchainImage": "builder@sha256:" + strings.Repeat("a", 64), "vendorCommand": []any{"sh", "-ec", vendor}}},
				"components": map[string]any{"tool": map[string]any{"platforms": []string{"linux/amd64"}, "fromSource": true, "source": "source", "outputType": "host-package", "artifactPath": "out/tool", "writeDir": "out", "buildCommand": []any{"sh", "-ec", build}}},
			}
			if tc.platform {
				component := registry["components"].(map[string]any)["tool"].(map[string]any)
				component["buildCommandByPlatform"] = map[string]any{"linux/amd64": component["buildCommand"]}
				component["buildCommand"] = []string{"unused-fallback"}
			}
			if tc.fragment {
				fragment := map[string]any{"apiVersion": "nvkr.dev/catalog-fragment/v1", "kind": "CatalogFragment", "sources": registry["sources"], "components": registry["components"]}
				raw, err := json.Marshal(fragment)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(declarationRoot, "fragment.yaml"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				registry = map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "imports": []any{map[string]any{
					"path": "../shared/fragment.yaml", "sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "sources": map[string]string{"source": "source"}, "components": map[string]string{"tool": "tool"},
				}}}
			}
			raw, err := json.Marshal(registry)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(catalogRoot, catalog.RegistryFile), raw, 0600); err != nil {
				t.Fatal(err)
			}
			lock := "apiVersion: nvkr.dev/lock/v1\nkind: PinLock\npins:\n  source:\n    tag: v1.0.0\n    commit: " + strings.Repeat("b", 40) + "\n    treeHash: " + strings.Repeat("c", 40) + "\n    sourceDateEpoch: 1\n"
			if err := os.WriteFile(filepath.Join(catalogRoot, catalog.LockFile), []byte(lock), 0600); err != nil {
				t.Fatal(err)
			}
			identities := func(t *testing.T) (string, string) {
				t.Helper()
				cat, err := catalog.LoadContext(t.Context(), catalog.Dir(catalogRoot))
				if err != nil {
					t.Fatal(err)
				}
				resolved, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/amd64"})
				if err != nil {
					t.Fatal(err)
				}
				graph := mustCompile(t, resolved)
				buildDigest, err := graph.Digest()
				if err != nil {
					t.Fatal(err)
				}
				vendorDigest := ""
				for _, node := range graph.Nodes {
					if node.Kind == ir.KindFetch {
						vendorDigest = node.VendorSpecDigest
					}
				}
				return buildDigest, vendorDigest
			}
			buildDigest, vendorDigest := identities(t)
			if buildDigest != wantBuild || vendorDigest != wantVendor {
				t.Fatalf("file operands changed identity: %s / %s versus %s / %s", buildDigest, vendorDigest, wantBuild, wantVendor)
			}
			if tc.fileBacked {
				for _, mutation := range []struct {
					name, path, body string
					vendorChanges    bool
				}{
					{"build bytes", tc.buildPath, buildBody + "# changed\n", false},
					{"vendor bytes", tc.vendorPath, vendorBody + "# changed\n", true},
				} {
					t.Run(mutation.name, func(t *testing.T) {
						// Each check starts from the golden bytes, then loads fresh.
						for name, body := range map[string]string{tc.buildPath: buildBody, tc.vendorPath: vendorBody} {
							if err := os.WriteFile(filepath.Join(declarationRoot, name), []byte(body), 0600); err != nil {
								t.Fatal(err)
							}
						}
						if err := os.WriteFile(filepath.Join(declarationRoot, mutation.path), []byte(mutation.body), 0600); err != nil {
							t.Fatal(err)
						}
						changedBuild, changedVendor := identities(t)
						if changedBuild == wantBuild {
							t.Fatal("fresh script change did not change Build identity")
						}
						if (changedVendor != wantVendor) != mutation.vendorChanges {
							t.Fatal("VendorSpec identity did not track exactly vendor bytes")
						}
					})
				}
			}
		})
	}
}
