// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

var pinnedToolRegistry = `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  aicr:
    repo: https://github.com/NVIDIA/aicr
    constraint: 1.2.x
    tool:
      format: tar-gzip
      url: https://github.com/NVIDIA/aicr/releases/download/{{.Tag}}/aicr_{{.Version}}_linux_arm64.tar.gz
      platform: linux/arm64
      executablePath: aicr
  plain:
    repo: https://github.com/example/plain
components:
  recipe:
    builderType: pinned-tool
    tool: aicr
    toolchainImage: registry.example/runner@sha256:` + pinnedToolchainHex + `
    platforms: [linux/arm64]
    outputType: file
    artifactPath: recipe.yaml
    toolEnv: {AICR_LOG_FORMAT: text}
    toolArgs: [recipe, --accelerator, gb200, --output, '{{.Output}}/recipe.yaml']
  bundle:
    builderType: pinned-tool
    tool: aicr
    toolchainImage: registry.example/runner@sha256:` + pinnedToolchainHex + `
    platforms: [linux/arm64]
    outputType: oci-layout
    materials: {recipe: recipe}
    toolArgs: [bundle, --recipe, '{{.Material "recipe"}}', --output, 'oci-layout:{{.Output}}']
`

var pinnedToolLock = `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  aicr:
    tag: v1.2.0-rc1
    commit: ` + goodCommit + `
    treeHash: ` + goodTree + `
    sourceDateEpoch: 123
    tool:
      format: tar-gzip
      url: https://github.com/NVIDIA/aicr/releases/download/v1.2.0-rc1/aicr_1.2.0-rc1_linux_arm64.tar.gz
      platform: linux/arm64
      executablePath: aicr
      archiveDigest: ` + goodArtifactDigest + `
      executableDigest: sha256:` + pinnedToolExecutableHex + `
  plain:
    tag: v1.0.0
    commit: ` + goodCommit + `
    treeHash: ` + goodTree + `
    sourceDateEpoch: 123
`

var (
	pinnedToolchainHex      = strings.Repeat("c", 64)
	pinnedToolExecutableHex = strings.Repeat("b", 64)
)

func TestPinnedToolCatalogForm(t *testing.T) {
	// edit replaces one exact fixture line; a missing line fails the case instead of
	// silently testing the unmodified catalog.
	type edit struct{ registry, lock [2]string }
	for _, test := range []struct {
		name   string
		edits  []edit
		append string
		code   e.Code
	}{
		{name: "recipe and bundle chain"},
		{name: "unknown template token", edits: []edit{{registry: [2]string{`'{{.Output}}/recipe.yaml'`, `'{{.Env "HOME"}}'`}}}, code: e.CodeInvalidCatalog},
		{name: "unbalanced template delimiter", edits: []edit{{registry: [2]string{`'{{.Output}}/recipe.yaml'`, `'{{.Output}}/}}'`}}}, code: e.CodeInvalidCatalog},
		{name: "binding without token", edits: []edit{{registry: [2]string{`'{{.Material "recipe"}}'`, `recipe.yaml`}}}, code: e.CodeInvalidCatalog},
		{name: "token without binding", edits: []edit{{registry: [2]string{`'{{.Material "recipe"}}'`, `'{{.Material "other"}}'`}}}, code: e.CodeInvalidCatalog},
		{name: "authored network request", edits: []edit{{registry: [2]string{"    outputType: file\n", "    outputType: file\n    buildNetwork: none\n"}}}, code: e.CodeInvalidCatalog},
		{name: "source fetch", edits: []edit{{registry: [2]string{"    outputType: file\n", "    outputType: file\n    source: plain\n"}}}, code: e.CodeInvalidCatalog},
		{name: "build command", edits: []edit{{registry: [2]string{"    outputType: file\n", "    outputType: file\n    buildCommand: [sh]\n"}}}, code: e.CodeInvalidCatalog},
		{name: "patches", edits: []edit{{registry: [2]string{"    outputType: file\n", "    outputType: file\n    patches: [{name: p.patch, sha256: " + goodArtifactDigest + "}]\n"}}}, code: e.CodeInvalidCatalog},
		{name: "executor-owned environment", edits: []edit{{registry: [2]string{"AICR_LOG_FORMAT: text", "HOME: /root"}}}, code: e.CodeInvalidCatalog},
		{name: "template in environment value", edits: []edit{{registry: [2]string{"AICR_LOG_FORMAT: text", "AICR_LOG_FORMAT: '{{.Output}}'"}}}, code: e.CodeInvalidCatalog},
		{name: "binding containing the closing delimiter", edits: []edit{{registry: [2]string{"materials: {recipe: recipe}", `materials: {"re}}cipe": recipe}`}}, {registry: [2]string{`'{{.Material "recipe"}}'`, `'{{.Material "re}}cipe"}}'`}}}},
		{name: "tool fields on another builder", append: "  stray:\n    source: plain\n    fromSource: true\n    tool: aicr\n    platforms: [linux/arm64]\n    outputType: raw\n    buildCommand: [make]\n    artifactPath: out/x\n", code: e.CodeInvalidCatalog},
		{name: "unknown tool source", edits: []edit{{registry: [2]string{"    tool: aicr\n    toolchainImage", "    tool: missing\n    toolchainImage"}}}, code: e.CodeInvalidCatalog},
		{name: "source without tool declaration", edits: []edit{{registry: [2]string{"    tool: aicr\n    toolchainImage", "    tool: plain\n    toolchainImage"}}}, code: e.CodeUnpinnedSource},
		{name: "assignment in executable path", edits: []edit{{registry: [2]string{"executablePath: aicr\n", "executablePath: bin/aicr=1\n"}, lock: [2]string{"executablePath: aicr\n", "executablePath: bin/aicr=1\n"}}}, code: e.CodeInvalidCatalog},
		{name: "tool pin out of date", edits: []edit{{lock: [2]string{"_linux_arm64.tar.gz\n", "_linux_arm64.tgz\n"}}}, code: e.CodeUnpinnedSource},
		{name: "platform differs from tool", edits: []edit{{registry: [2]string{"    platforms: [linux/arm64]\n    outputType: file", "    platforms: [linux/amd64]\n    outputType: file"}}}, code: e.CodeEngineCapability},
		{name: "second platform", edits: []edit{{registry: [2]string{"    platforms: [linux/arm64]\n    outputType: file", "    platforms: [linux/amd64, linux/arm64]\n    outputType: file"}}}, code: e.CodeEngineCapability},
		{name: "tagged toolchain", edits: []edit{{registry: [2]string{"    toolchainImage: registry.example/runner@sha256:" + pinnedToolchainHex + "\n    platforms: [linux/arm64]\n    outputType: file", "    toolchainImage: registry.example/runner:latest\n    platforms: [linux/arm64]\n    outputType: file"}}}, code: e.CodeTagToolchain},
		{name: "directory output form", edits: []edit{{registry: [2]string{"outputType: oci-layout", "outputType: directory"}}}, code: e.CodeInvalidCatalog},
		{name: "file output without artifactPath", edits: []edit{{registry: [2]string{"    artifactPath: recipe.yaml\n", ""}}}, code: e.CodeInvalidCatalog},
		{name: "nested file artifactPath", edits: []edit{{registry: [2]string{"artifactPath: recipe.yaml", "artifactPath: out/recipe.yaml"}}}, code: e.CodeInvalidCatalog},
		{name: "oci-layout as material", append: "  consumer:\n    builderType: pinned-tool\n    tool: aicr\n    toolchainImage: registry.example/runner@sha256:" + pinnedToolchainHex + "\n    platforms: [linux/arm64]\n    outputType: file\n    artifactPath: out.txt\n    materials: {layout: bundle}\n    toolArgs: ['{{.Material \"layout\"}}']\n", code: e.CodeEngineCapability},
		{name: "cross-platform material", append: "  amd:\n    builderType: pinned-tool\n    tool: aicr\n    toolchainImage: registry.example/runner@sha256:" + pinnedToolchainHex + "\n    platforms: [linux/arm64]\n    outputType: file\n    artifactPath: out.txt\n    materials: {raw: rawfile}\n    toolArgs: ['{{.Material \"raw\"}}']\n  rawfile:\n    source: plain\n    fromSource: true\n    toolchainImage: registry.example/runner@sha256:" + pinnedToolchainHex + "\n    platforms: [linux/amd64]\n    outputType: raw\n    buildCommand: [make]\n    artifactPath: out/x\n", code: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, lock := pinnedToolRegistry+test.append, pinnedToolLock
			for _, change := range test.edits {
				for _, target := range []*struct {
					text *string
					pair [2]string
				}{{&registry, change.registry}, {&lock, change.lock}} {
					if target.pair[0] == "" {
						continue
					}
					if !strings.Contains(*target.text, target.pair[0]) {
						t.Fatalf("fixture lacks %q", target.pair[0])
					}
					*target.text = strings.Replace(*target.text, target.pair[0], target.pair[1], 1)
				}
			}
			cat, err := LoadContext(t.Context(), bytesProvider{RegistryFile: []byte(registry), LockFile: []byte(lock)})
			if got := pinErrorCode(err); got != test.code {
				t.Fatalf("error %v; want %s", err, test.code)
			}
			if test.code != "" {
				return
			}
			recipe := cat.Components["recipe"]
			if recipe.FromSource || recipe.BuilderType != BuilderPinnedTool || recipe.Tool != "aicr" || recipe.ToolExecutable == nil ||
				recipe.ToolExecutable.ExecutableDigest != "sha256:"+pinnedToolExecutableHex || recipe.Tag != "v1.2.0-rc1" ||
				recipe.Repo != "" || recipe.Commit != "" || !slices.Equal(recipe.ToolArgs, []string{"recipe", "--accelerator", "gb200", "--output", "{{.Output}}/recipe.yaml"}) {
				t.Fatalf("wrong resolved pinned-tool component: %+v", recipe)
			}
		})
	}
}

func TestRenderToolArgs(t *testing.T) {
	staged := map[string]string{"re cipe": "/in/re%20cipe", "re}}cipe": "/in/re%7D%7Dcipe", `re"cipe`: "/in/re%22cipe"}
	values := ToolArgValues{Output: "/out", Arch: "arm64", Material: func(binding string) (string, error) {
		if path, ok := staged[binding]; ok {
			return path, nil
		}
		return "", e.New(e.CodeInvalidCatalog, "unknown %q", binding)
	}}
	for _, test := range []struct {
		args []string
		want []string
		code e.Code
	}{
		{args: []string{"--out={{.Output}}/r.yaml", "{{.Arch}}", `{{.Material "re cipe"}}`, "plain"}, want: []string{"--out=/out/r.yaml", "arm64", "/in/re%20cipe", "plain"}},
		{args: []string{"{{.Output}}{{.Output}}"}, want: []string{"/out/out"}},
		// A quoted binding is one unit: a delimiter or escaped quote inside it does not end the token.
		{args: []string{`--recipe={{.Material "re}}cipe"}}`, `{{.Material "re\"cipe"}}`}, want: []string{"--recipe=/in/re%7D%7Dcipe", "/in/re%22cipe"}},
		{args: []string{`{{.Material "other"}}`}, code: e.CodeInvalidCatalog},
		{args: []string{"{{ .Output }}"}, code: e.CodeInvalidCatalog},
		{args: []string{"{{.Output}"}, code: e.CodeInvalidCatalog},
		{args: []string{"}}"}, code: e.CodeInvalidCatalog},
		{args: []string{`{{.Material "unterminated}}`}, code: e.CodeInvalidCatalog},
		{args: []string{`{{printf "x"}}`}, code: e.CodeInvalidCatalog},
	} {
		got, err := RenderToolArgs(test.args, values)
		if pinErrorCode(err) != test.code || (test.code == "" && !slices.Equal(got, test.want)) {
			t.Errorf("RenderToolArgs(%q) = %q, %v; want %q, %s", test.args, got, err, test.want, test.code)
		}
	}
}
