// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"encoding/json"
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
	for _, fileBacked := range []bool{false, true} {
		root := t.TempDir()
		var build, vendor any = buildBody, vendorBody
		if fileBacked {
			build, vendor = map[string]string{"file": "build.sh"}, map[string]string{"file": "vendor.sh"}
			for name, body := range map[string]string{"build.sh": buildBody, "vendor.sh": vendorBody} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		registry := map[string]any{
			"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
			"sources":    map[string]any{"source": map[string]any{"repo": "https://example.test/source", "toolchainImage": "builder@sha256:" + strings.Repeat("a", 64), "vendorCommand": []any{"sh", "-ec", vendor}}},
			"components": map[string]any{"tool": map[string]any{"platforms": []string{"linux/amd64"}, "fromSource": true, "source": "source", "outputType": "host-package", "artifactPath": "out/tool", "writeDir": "out", "buildCommand": []any{"sh", "-ec", build}}},
		}
		raw, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, catalog.RegistryFile), raw, 0600); err != nil {
			t.Fatal(err)
		}
		lock := "apiVersion: nvkr.dev/lock/v1\nkind: PinLock\npins:\n  source:\n    tag: v1.0.0\n    commit: " + strings.Repeat("b", 40) + "\n    treeHash: " + strings.Repeat("c", 40) + "\n    sourceDateEpoch: 1\n"
		if err := os.WriteFile(filepath.Join(root, catalog.LockFile), []byte(lock), 0600); err != nil {
			t.Fatal(err)
		}
		cat, err := catalog.LoadContext(t.Context(), catalog.Dir(root))
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
		if buildDigest != wantBuild || vendorDigest != wantVendor {
			t.Fatalf("file operands changed identity: %s / %s versus %s / %s", buildDigest, vendorDigest, wantBuild, wantVendor)
		}
		t.Logf("file=%v Build=%s VendorSpec=%s", fileBacked, buildDigest, vendorDigest)
	}
}
