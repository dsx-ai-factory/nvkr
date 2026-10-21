// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The negative cases run the actual compiled preparation command with Helm
// mocked, but real archive checks. An explicit local archive fixture adds a
// positive byte-check probe; neither mode proves upstream signature trust.
func TestLaptopNKTPDependencyArchiveChecks(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("archive-byte contracts require sha256sum")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	definitionPath := filepath.Join("inputs", "definitions", "diagnostics", "demo-arm64", "site.json")
	environmentPath := "inputs/environments/local-arm64-tcg.json"
	doc, err := definition.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	env, err := definition.LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := CompileDocuments(t.Context(), definitionPath, doc, environmentPath, env, "contract-test")
	if err != nil {
		t.Fatal(err)
	}
	var command []string
	for _, build := range plan.Builds {
		if build.Name == "nktp" {
			for _, node := range build.Graph.Nodes {
				if node.Kind == ir.KindFetch {
					command = node.VendorCmd
				}
			}
		}
	}
	if len(command) == 0 {
		t.Fatal("missing compiled NKTP preparation command")
	}
	archives := []string{"metrics-server-3.13.1.tgz", "opentelemetry-kube-stack-0.22.0.tgz", "prometheus-node-exporter-4.56.1.tgz"}
	for _, tc := range []struct {
		name, message string
		count         int
		fixture       bool
	}{
		{"republished archives fail hashes", "FAILED", 3, false},
		{"missing archive fails count", "Expected exactly three", 2, false},
		{"additional archive fails count", "Expected exactly three", 4, false},
		{"held acquired archive bytes pass", "", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := os.Getenv("NVKR_TEST_NKTP_CHART_ARCHIVES")
			if tc.fixture && fixture == "" {
				t.Skip("positive byte probe requires explicit NVKR_TEST_NKTP_CHART_ARCHIVES")
			}
			dir := t.TempDir()
			chart := filepath.Join(dir, "nktp/deploy/charts/nktp")
			tools := filepath.Join(dir, "tools")
			for _, path := range []string{filepath.Join(chart, "charts"), tools} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"Chart.yaml", "Chart.lock"} {
				if err := os.WriteFile(filepath.Join(chart, name), []byte("unchanged source fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for i := range tc.count {
				name := "additional.tgz"
				if i < len(archives) {
					name = archives[i]
				}
				data := []byte("republished archive\n")
				if tc.fixture {
					data, err = os.ReadFile(filepath.Join(fixture, name))
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(chart, "charts", name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(tools, "helm"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, command[0], command[1:]...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if tc.fixture {
				if err != nil || strings.Count(string(out), ".tgz: OK") != 3 {
					t.Fatalf("held archive checks failed: %v: %s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), tc.message) {
				t.Fatalf("preparation error=%v output=%s; want failure %q", err, out, tc.message)
			}
		})
	}
}

// These are compiler contracts, not a five-output build or installation test.
func TestLaptopSiteArtifactContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	definitionPath := filepath.Join("inputs", "definitions", "diagnostics", "demo-arm64", "site.json")
	for _, tc := range []struct {
		name             string
		foreign, unbound bool
		wantCode         e.Code
	}{
		{"Docker", false, false, ""},
		{"foreign target", true, false, e.CodeNoMatch},
		{"unbound controller chart", false, true, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := definition.LoadDefinition(t.Context(), definitionPath)
			if err != nil {
				t.Fatal(err)
			}
			environmentPath := "inputs/environments/local-arm64-tcg.json"
			env, err := definition.LoadEnvironment(t.Context(), environmentPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.foreign {
				for i := range doc.Outputs {
					doc.Outputs[i].Platform = "linux/amd64"
				}
			}
			if tc.unbound {
				doc.Outputs[0].Helm = nil
			}
			plan, err := CompileDocuments(t.Context(), definitionPath, doc, environmentPath, env, "contract-test")
			if tc.wantCode != "" {
				assertFactoryErrorCode(t, err, tc.wantCode)
				if plan != nil {
					t.Fatal("rejected inputs returned an executable Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			installerPin, err := plan.CatalogSnapshot.SourcePin("nvke-cli")
			if err != nil {
				t.Fatal(err)
			}
			installerSource := plan.CatalogSnapshot.Sources["nvke-cli"]
			compiler := installerSource.ToolchainImage
			var names []string
			for _, output := range plan.LifecycleOutputs {
				names = append(names, output.Name)
			}
			if !slices.Equal(names, []string{"controller-chart", "controller-profiles", "cwe", "nktp", "nvkectl"}) {
				t.Fatalf("not the declared five-output site subset: %v", names)
			}
			for _, build := range plan.Builds {
				if build.Platform != "linux/arm64" || len(build.Components) != 1 {
					t.Fatalf("unexpected source target: %+v", build)
				}
				fetches, producers := 0, 0
				for _, node := range build.Graph.Nodes {
					switch node.Kind {
					case ir.KindPull:
						t.Fatal("site applications/charts must not fall back to acquired releases")
					case ir.KindFetch:
						fetches++
						if node.Repo != installerSource.Repo || node.Commit != installerPin.Commit || node.TreeHash != installerPin.TreeHash {
							t.Fatal("CLI, controller and charts must share the selected installer source")
						}
						if build.Name == "nktp" {
							command := strings.Join(node.VendorCmd, " ")
							for _, required := range []string{"sh -euc", "HELM_REPOSITORY_CONFIG=/tmp/", "HELM_REPOSITORY_CACHE=/tmp/", "helm repo add nktp-otel https://open-telemetry.github.io/opentelemetry-helm-charts", "helm repo add nktp-prometheus https://prometheus-community.github.io/helm-charts", "helm repo add nktp-metrics https://kubernetes-sigs.github.io/metrics-server/", "helm dependency build nktp/deploy/charts/nktp --skip-refresh", "sha256sum -c -",
								"084e6edb680cf4e2acc30bd496568c53fdf663cbacf6e17876b25785c35b7a13  nktp/deploy/charts/nktp/charts/metrics-server-3.13.1.tgz",
								"df7d63a331d9d164a276f5b4395c6383c00fe801abcc6b2ea969de182914dc32  nktp/deploy/charts/nktp/charts/opentelemetry-kube-stack-0.22.0.tgz",
								"70fab291e00a34c58fd943bb075fecc2e2e73fa95db1bcbbe7cdfe008795354a  nktp/deploy/charts/nktp/charts/prometheus-node-exporter-4.56.1.tgz",
								`[ "$#" -ne 3 ]`} {
								if !strings.Contains(command, required) {
									t.Fatalf("NKTP dependency preparation lacks %q", required)
								}
							}
						}
						if build.Name == "nvkectl" {
							command := strings.Join(node.VendorCmd, " ")
							for _, required := range []string{"GOWORK=off", "go mod verify", "go mod vendor", "controller/api/go.sum", "controller/deploy/charts/go.mod", "test ! -e controller/deploy/charts/go.sum", "sha256sum --quiet -c"} {
								if !strings.Contains(command, required) {
									t.Fatalf("CLI preparation lacks %q", required)
								}
							}
						}
					case ir.KindBuild:
						producers++
						if build.Name == "cwe" {
							command := strings.Join(node.Command, " ")
							if node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || !strings.Contains(command, "--local dockerfile=/src/controller") {
								t.Fatal("controller must use its source-owned Dockerfile with declared build networking")
							}
							if strings.Count(command, "--opt build-arg:GOLANG_IMAGE=docker.io/library/"+compiler) != 1 {
								t.Fatal("controller must bind the exact reviewed compiler through its source-owned build argument")
							}
						} else if build.Name == "controller-profiles" {
							if node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || len(node.Patches) != 1 {
								t.Fatal("profiles must use the authenticated diagnostic image producer")
							}
						} else if node.Network != ir.NetworkNone || !node.SourceReadOnly {
							t.Fatal("CLI/chart producers must consume prepared sources offline and read-only")
						}
						if build.Name == "nvkectl" && node.Toolchain != compiler {
							t.Fatal("site CLI must select the exact reviewed compiler, not the retained producer pin")
						}
						if build.Name == "nvkectl" && !strings.Contains(strings.Join(node.Command, " "), "make -C cli build NVKECTL_TREE=clean") {
							t.Fatal("source-owned Make must own the changed CLI linker symbols and stamp assertion")
						}
					}
				}
				if fetches != 1 || producers != 1 {
					t.Fatalf("%s fetches=%d producers=%d", build.Name, fetches, producers)
				}
			}
			found := false
			for _, node := range plan.Graph.Nodes {
				if node.ID == "package/controller-chart" {
					found = node.HelmImage != nil && slices.Contains(node.Inputs, "package/cwe")
				}
				if node.Kind == "publish" || node.Kind == "sign" {
					t.Fatal("local candidate must not imply publication or signing")
				}
			}
			if !found {
				t.Fatal("controller chart must bind the exact produced controller image")
			}
		})
	}
}
