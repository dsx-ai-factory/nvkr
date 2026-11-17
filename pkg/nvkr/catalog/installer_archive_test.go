// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerArchivePreservesLocalChartDependencies(t *testing.T) {
	version, err := exec.CommandContext(t.Context(), "tar", "--version").CombinedOutput()
	if err != nil || !bytes.Contains(version, []byte("GNU tar")) {
		t.Skip("installer archive recipe requires GNU tar")
	}
	probe := exec.CommandContext(t.Context(), "make", "-s", "-f", "/dev/null", "--eval=nvkr-test: ; @:", "nvkr-test")
	if _, err := probe.CombinedOutput(); err != nil {
		t.Skip("installer archive recipe requires make --eval support")
	}
	base, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/tools/native"))
	if err != nil {
		t.Fatal(err)
	}
	nightly, err := LoadOverlayContext(t.Context(), "../../../inputs/catalogs/tools/variants/nightly-go.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, producer := range map[string]Component{
		"native":  base.Components["nvke-installer-sources"],
		"nightly": *nightly.Components["nvke-installer-sources"].Component,
	} {
		for _, layout := range []string{"standalone", "sibling"} {
			t.Run(name+"/"+layout, func(t *testing.T) {
				root := t.TempDir()
				files := map[string]string{
					"make/tool-versions.mk":                          "CAPI_VERSION := v1.0.0\n",
					"controller/deploy/charts/controller/Chart.yaml": "apiVersion: v2\nname: controller\nversion: 0.1.0\n",
					"controller/deploy/charts/go.mod":                "module example.invalid/chart-tests\n",
				}
				for _, path := range []string{
					"deploy/nvke-deploy.sh", "deploy/lib/lib.sh", "deploy/nico/manifest.yaml",
					"cli/internal/bootstrap/sitespec/template.yaml", "nktp/deploy/charts/nktp/Chart.yaml",
					"controller/hack/apply-capi-spiffe-exec-auth.sh", "controller/deploy/k8s/capi-spiffe-exec-auth/overlay.yaml",
					"controller/test/fixtures/releases/release.yaml", "controller/nico-sim/internal/seed/default-seed.yaml",
					"controller/nico-sim/ui/index.html", "bin/placeholder",
				} {
					files[path] = "fixture\n"
				}
				if layout == "sibling" {
					files["controller/deploy/charts/controller/Chart.yaml"] += "dependencies:\n  - name: shared-crds\n    version: 0.1.0\n    repository: file://../shared-crds\n"
					files["controller/deploy/charts/shared-crds/Chart.yaml"] = "apiVersion: v2\nname: shared-crds\nversion: 0.1.0\n"
					files["controller/deploy/charts/shared-crds/templates/crds.yaml"] = "source-owned CRDs\n"
				}
				for path, data := range files {
					target := filepath.Join(root, path)
					if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(target, []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				command := producer.BuildCommand
				child := exec.CommandContext(t.Context(), command[0], command[1:]...)
				child.Dir = root
				child.Env = append(os.Environ(), "NKECTL_COMMIT="+strings.Repeat("a", 40), "SOURCE_DATE_EPOCH=1")
				if out, err := child.CombinedOutput(); err != nil {
					t.Fatalf("installer archive command: %v: %s", err, out)
				}
				raw, err := os.ReadFile(filepath.Join(root, producer.ArtifactPath))
				if err != nil {
					t.Fatal(err)
				}
				gz, err := gzip.NewReader(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				archive := tar.NewReader(gz)
				members := map[string]string{}
				for {
					header, err := archive.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(archive)
					if err != nil {
						t.Fatal(err)
					}
					members[header.Name] = string(data)
				}
				for path, data := range files {
					if strings.HasPrefix(path, "controller/deploy/charts/") && path != "controller/deploy/charts/go.mod" && members[path] != data {
						t.Errorf("installer omitted or changed local chart member %s", path)
					}
				}
				if _, exists := members["controller/deploy/charts/go.mod"]; exists {
					t.Error("installer includes the chart module's development files")
				}
			})
		}
	}
}
