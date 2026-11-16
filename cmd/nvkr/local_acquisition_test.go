// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func acquisitionCatalogFixture(t *testing.T, url, digest string) string {
	t.Helper()
	root := t.TempDir()
	registry := fmt.Sprintf(`apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  source: {repo: https://example.invalid/source}
components:
  tool:
    platforms: [linux/amd64, linux/arm64]
    source: source
    outputType: raw
    prebuilt:
      url: %s
      evidence: {none: true}
`, url)
	lock := fmt.Sprintf(`apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  source: {tag: v1.2.3, commit: %s, treeHash: %s, sourceDateEpoch: 1733939955}
artifacts:
  tool: {tag: v1.2.3, digests: {amd64: %s, arm64: %s}}
`, strings.Repeat("a", 40), strings.Repeat("b", 40), digest, digest)
	for name, text := range map[string]string{"registry.yaml": registry, "pins.lock.yaml": lock} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveOutputFormsShareBuildIdentity(t *testing.T) {
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")
	invoke := func(format string) []byte {
		t.Helper()
		out, err := runCLIWithCapturedStdout(t, newApp(), "nvkr", "resolve", "--data", data, "--component", "containerd", "--platform", "linux/amd64", "-o", format)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	digest := strings.TrimSpace(string(invoke("digest")))
	if !canonical.IsDigest(digest) {
		t.Fatalf("digest = %q", digest)
	}
	var graph ir.Graph
	if err := json.Unmarshal(invoke("nodes"), &graph); err != nil {
		t.Fatal(err)
	}
	computed, err := graph.Digest()
	if err != nil || computed != digest {
		t.Fatalf("graph digest = %s, %v", computed, err)
	}
	view, err := graph.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := canonical.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(invoke("canonical"))) != string(expected) {
		t.Fatal("canonical output differs from build identity view")
	}
	for _, format := range []string{"ir", "components"} {
		out := string(invoke(format))
		if !strings.Contains(out, digest) || !strings.Contains(out, "containerd") {
			t.Fatalf("%s output lacks identity or members: %s", format, out)
		}
	}
}
