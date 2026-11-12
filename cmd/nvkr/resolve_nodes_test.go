// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/urfave/cli/v2"
)

func TestResolveNodesOutputPreservesIdentityFrame(t *testing.T) {
	queryArgs := []string{
		"--component", "kubectl", "--platform", "linux/amd64",
		"--data", filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"),
	}
	app := &cli.App{Commands: []*cli.Command{cmdResolve()}}
	nodes, err := runCLIWithCapturedStdout(t, app,
		append([]string{"nvkr", "resolve"}, append(queryArgs, "-o", "nodes")...)...)
	if err != nil {
		t.Fatalf("resolve -o nodes failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(nodes, &decoded); err != nil {
		t.Fatalf("nodes output is not JSON: %v\n%s", err, nodes)
	}
	if _, ok := decoded["nodes"].([]any); !ok {
		t.Fatalf("nodes output omitted the full execution graph: %s", nodes)
	}
	if decoded["platform"] != "linux/amd64" {
		t.Fatalf("nodes output lost target platform: %s", nodes)
	}
	for _, legacy := range []string{"criteria", "recipeID", "recipeId"} {
		if _, present := decoded[legacy]; present {
			t.Fatalf("nodes output contains removed %s", legacy)
		}
	}

}

func runCLIWithCapturedStdout(t *testing.T, app *cli.App, args ...string) ([]byte, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = output
	runErr := app.Run(args)
	os.Stdout = original
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return raw, runErr
}

func TestResolveComponentsListsEverySelectedProducer(t *testing.T) {
	for _, test := range []struct {
		name, data, platform string
		components           []string
		rows                 []string
	}{
		{
			name:       "source build beside acquired executable and image",
			data:       filepath.Join("..", "..", "testdata", "examples", "operator-catalog"),
			platform:   "linux/amd64",
			components: []string{"acme-pause", "acme-kubectl-bin", "acme-kubectl"},
			rows: []string{
				"acme-kubectl               host-package  source      toolchain=sha256:9e4d7fdad5d2b10609edfb3f0cadb7b4ca7bea6a9d2e42bca6b175de85c8d8d2",
				"acme-kubectl-bin           host-package  acquisition digest=sha256:646d58f6d98ee670a71d9cdffbf6625aeea2849d567f214bc43a35f8ccb7bf70",
				"acme-pause                 oci-image     acquisition digest=sha256:7c38f24774e3cbd906d2d33c38354ccf787635581c122965132c9bd309754d4a",
			},
		},
		{
			name:       "archive and index acquisitions beside a pinned tool and its consumer",
			data:       producerModesCatalogFixture(t),
			platform:   "linux/arm64",
			components: []string{"recipe", "pause", "helm", "consumer"},
			rows: []string{
				"consumer                   raw           source      toolchain=example.invalid/toolchain@" + verifyTestDigest('7'),
				"helm                       host-package  acquisition digest=" + verifyTestDigest('2') + " archive=" + verifyTestDigest('1'),
				"pause                      oci-image     acquisition digest=" + verifyTestDigest('4') + " parent=" + verifyTestDigest('5'),
				"recipe                     file          pinned-tool tool=renderer executable=" + verifyTestDigest('3') +
					" toolchain=example.invalid/runner@" + verifyTestDigest('6'),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolve := func(format string) string {
				t.Helper()
				args := []string{"nvkr", "resolve", "--data", test.data, "--platform", test.platform, "-o", format}
				for _, component := range test.components {
					args = append(args, "--component", component)
				}
				out, err := runCLIWithCapturedStdout(t, &cli.App{Commands: []*cli.Command{cmdResolve()}}, args...)
				if err != nil {
					t.Fatalf("resolve -o %s: %v", format, err)
				}
				return string(out)
			}
			want := "buildDigest: " + strings.TrimSpace(resolve("digest")) + "\n\n" + strings.Join(test.rows, "\n") + "\n"
			if got := resolve("components"); got != want {
				t.Fatalf("components output:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestResolveComponentsRejectsAProducerWithoutANode(t *testing.T) {
	resolution, err := client.New(client.WithData(filepath.Join("..", "..", "testdata", "examples", "operator-catalog"))).
		ResolveBuild(t.Context(), client.ResolveRequest{Components: []string{"acme-kubectl", "acme-pause"}, Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	resolution.Graph.Nodes = slices.DeleteFunc(resolution.Graph.Nodes, func(node ir.Node) bool { return node.Component == "acme-pause" })
	rows, err := componentRows(resolution)
	assertLifecycleCommandCode(t, err, e.CodeInternal)
	if rows != nil {
		t.Fatalf("rows = %q, want none", rows)
	}
}

func producerModesCatalogFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	registry := `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  helm:
    repo: https://example.invalid/helm
    constraint: 4.1.x
    tool:
      format: tar-gzip
      url: https://example.invalid/helm-{{.Tag}}-linux-arm64.tar.gz
      platform: linux/arm64
      executablePath: linux-arm64/helm
  renderer:
    repo: https://example.invalid/renderer
    constraint: 1.2.x
    tool:
      format: raw
      url: https://example.invalid/renderer-{{.Tag}}
      platform: linux/arm64
      executablePath: renderer
  pause:
    repo: https://example.invalid/pause
    constraint: 3.10.x
  consumer:
    repo: https://example.invalid/consumer
    toolchainImage: example.invalid/toolchain@` + verifyTestDigest('7') + `
components:
  helm:
    source: helm
    platforms: [linux/arm64]
    outputType: host-package
    prebuilt: {releaseExecutable: true, evidence: {none: true}}
  pause:
    source: pause
    platforms: [linux/arm64]
    outputType: oci-image
    prebuilt: {image: 'example.invalid/pause:{{.Tag}}', evidence: {none: true}}
  recipe:
    builderType: pinned-tool
    tool: renderer
    toolchainImage: example.invalid/runner@` + verifyTestDigest('6') + `
    platforms: [linux/arm64]
    outputType: file
    artifactPath: recipe.yaml
    toolArgs: [recipe, --output, '{{.Output}}/recipe.yaml']
  consumer:
    source: consumer
    fromSource: true
    platforms: [linux/arm64]
    outputType: raw
    buildCommand: [make]
    writeDir: out
    artifactPath: out/artifact
    materials: {recipe: recipe}
`
	commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	lock := `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  helm:
    tag: v4.1.4
    commit: ` + commit + `
    treeHash: ` + tree + `
    sourceDateEpoch: 1733939955
    tool:
      format: tar-gzip
      url: https://example.invalid/helm-v4.1.4-linux-arm64.tar.gz
      platform: linux/arm64
      executablePath: linux-arm64/helm
      archiveDigest: ` + verifyTestDigest('1') + `
      executableDigest: ` + verifyTestDigest('2') + `
  renderer:
    tag: v1.2.0
    commit: ` + commit + `
    treeHash: ` + tree + `
    sourceDateEpoch: 1733939955
    tool:
      format: raw
      url: https://example.invalid/renderer-v1.2.0
      platform: linux/arm64
      executablePath: renderer
      archiveDigest: ` + verifyTestDigest('3') + `
      executableDigest: ` + verifyTestDigest('3') + `
  pause:
    tag: v3.10.0
    commit: ` + commit + `
    treeHash: ` + tree + `
    sourceDateEpoch: 1733939955
  consumer:
    tag: v1.0.0
    commit: ` + commit + `
    treeHash: ` + tree + `
    sourceDateEpoch: 1733939955
artifacts:
  pause:
    tag: v3.10.0
    digests:
      arm64: ` + verifyTestDigest('4') + `
    imageParentDigest: ` + verifyTestDigest('5') + `
`
	for name, text := range map[string]string{"registry.yaml": registry, "pins.lock.yaml": lock} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
