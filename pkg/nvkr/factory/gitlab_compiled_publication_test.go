// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
)

// The producer bytes are synthetic, but every observation, SBOM, image,
// image-bound chart, executable and release package is the real compiled node.
func gitlabCompiledPublicationFixture(t *testing.T, prefix string) (*Plan, map[string]Artifact) {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	chart := base.Components["containerd"]
	chart.OutputType = "tarball"
	chart.ArtifactPath = "bin/chart.tgz"
	chart.WriteDir = "bin"
	chart.BuildCommand = []string{"tar", "-czf", "/src/bin/chart.tgz", "chart"}
	chart.Helm = &catalog.HelmPackaging{Chart: ".", Images: map[string]catalog.HelmImage{"controller": {Values: catalog.HelmImageValues{Digest: "image.digest"}}}}
	base.Components["chart-source"] = chart
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	def, env := fixtureInputs("gitlab-image-chart", "cli", "")
	def.Outputs = []authored.DefinitionOutput{
		{Name: "image", Platform: "linux/amd64", Format: authored.OutputOCI, Components: []string{"coredns"}},
		{Name: "chart", Platform: "linux/amd64", Format: authored.OutputHelm, Components: []string{"chart-source"}, Helm: &authored.HelmOutput{Images: map[string]string{"controller": "image"}}},
		{Name: "cli", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"containerd"}},
	}
	slices.SortFunc(def.Outputs, func(a, b authored.DefinitionOutput) int { return strings.Compare(a.Name, b.Name) })
	env.GitLab = &authored.GitLab{APIURL: "https://gitlab.example.com/api/v4", ProjectID: 123, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: map[string]authored.GitLabRunner{"linux/amd64": {Tags: []string{"native"}, BootstrapImage: "registry.example.com/go@sha256:" + strings.Repeat("a", 64)}}}
	env.Publication = &authored.Publication{RepositoryPrefix: prefix, Anonymous: true}
	plan, err := compileDocumentsWithLoader(t.Context(), "config/definition.json", def, "config/environment.json", env, "v0.0.0 (test)", func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	for _, id := range []string{"artifact/image/coredns", "artifact/chart/chart-source", "artifact/cli/containerd"} {
		if err := os.MkdirAll(filepath.Dir(artifacts[id].Path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	binary := filepath.Join(work, "worker")
	if err := os.WriteFile(binary, []byte("synthetic worker"), 0755); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(work, "layout")
	if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", layout); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(artifacts["artifact/image/coredns"].Path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	err = filepath.WalkDir(layout, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(layout, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0644, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	chartDir := filepath.Join(work, "chart")
	if err := os.MkdirAll(filepath.Join(chartDir, "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"Chart.yaml": "apiVersion: v2\nname: controller\nversion: 0.1.0\n", "values.yaml": "image:\n  repository: old\n  tag: dev\n  digest: ''\n", "templates/controller.yaml": helmDeploymentFixture} {
		if err := os.WriteFile(filepath.Join(chartDir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	chartArchive := filepath.Join(work, "chart.tar")
	if err := transportarchive.Pack(t.Context(), chartArchive, work, []string{"chart"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(chartArchive, artifacts["artifact/chart/chart-source"].Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts["artifact/cli/containerd"].Path, []byte("synthetic executable"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"artifact/image/coredns", "result/image/coredns"}, {"artifact/chart/chart-source", "result/chart/chart-source"}, {"artifact/cli/containerd", "result/cli/containerd"}} {
		writeProducerRecordFixture(t, plan, artifacts[pair[0]], artifacts[pair[1]].Path)
	}
	for _, id := range []string{"image/terminal-observe", "chart/terminal-observe", "cli/terminal-observe", "image/sbom", "chart/sbom", "cli/sbom", "package/image", "package/chart", "package/cli", "package/release"} {
		if err := ExecuteNode(t.Context(), plan, id, io.Discard, io.Discard); err != nil {
			t.Fatalf("native compiled operation %s: %v", id, err)
		}
	}
	if _, err := delivery.Inspect(t.Context(), artifacts["package/release"].Path); err != nil {
		t.Fatal(err)
	}
	return plan, artifacts
}
