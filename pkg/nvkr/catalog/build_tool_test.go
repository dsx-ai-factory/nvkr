// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBuildToolCatalogBinding(t *testing.T) {
	registry := strings.Split(pinnedToolRegistry, "components:")[0] + `components:
  consumer:
    fromSource: true
    source: plain
    platforms: [linux/arm64]
    toolchainImage: registry.example/runner@sha256:` + pinnedToolchainHex + `
    buildTool: {linux/arm64: aicr}
    buildCommand: [make, 'TOOL={{.BuildTool}}']
    writeDir: out
    outputType: tarball
    artifactPath: out/result.tar
`
	for _, test := range []struct {
		name, before, after string
		code                e.Code
	}{
		{"native pin", "", "", ""},
		{"unknown tool", "linux/arm64: aicr", "linux/arm64: absent", e.CodeUnpinnedSource},
		{"wrong architecture", "buildTool: {linux/arm64: aicr}", "buildTool: {linux/amd64: aicr}", e.CodeEngineCapability},
		{"no binding", "    buildTool: {linux/arm64: aicr}\n", "", e.CodeInvalidCatalog},
		{"no invocation", "TOOL={{.BuildTool}}", "TOOL=/usr/local/bin/aicr", e.CodeInvalidCatalog},
		{"unknown token", "{{.BuildTool}}", "{{.BuildTools}}", e.CodeInvalidCatalog},
		{"network", "    writeDir: out", "    buildNetwork: default\n    writeDir: out", e.CodeEngineCapability},
		{"writable source", "    writeDir: out\n", "", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := registry
			if test.before != "" {
				input = strings.Replace(input, test.before, test.after, 1)
			}
			cat, err := LoadContext(t.Context(), bytesProvider{RegistryFile: []byte(input), LockFile: []byte(pinnedToolLock)})
			if got := pinErrorCode(err); got != test.code {
				t.Fatalf("got %v; want %s", err, test.code)
			}
			if err == nil && cat.Components["consumer"].BuildToolPins["linux/arm64"].ExecutableDigest != "sha256:"+pinnedToolExecutableHex {
				t.Fatal("loader lost the exact executable binding")
			}
		})
	}
}
