// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// These are compiler contracts, not a four-output build or installation test.
func TestLaptopSiteArtifactContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	definitionPath := filepath.Join("definitions", "nke-laptop-arm64", "definition-site.json")
	for _, tc := range []struct {
		name, environment string
		foreign, unbound  bool
		wantCode          e.Code
	}{
		{"Docker", "environment-local-docker.json", false, false, ""},
		{"Podman", "environment-local.json", false, false, ""},
		{"foreign target", "environment-local-docker.json", true, false, e.CodeNoMatch},
		{"unbound controller chart", "environment-local-docker.json", false, true, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := definition.LoadDefinition(t.Context(), definitionPath)
			if err != nil {
				t.Fatal(err)
			}
			environmentPath := filepath.Join(filepath.Dir(definitionPath), tc.environment)
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
			var names []string
			for _, output := range plan.LifecycleOutputs {
				names = append(names, output.Name)
			}
			if !slices.Equal(names, []string{"controller-chart", "cwe", "nkectl", "nktp"}) {
				t.Fatalf("not the declared four-output site subset: %v", names)
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
						if node.Commit != "8fb2d53368823bd386b4fbd55b4ab6d3c0931345" || node.TreeHash != "36569cf4ec0e2360ee023cd7e53da65b96ce770d" {
							t.Fatal("CLI, controller and charts must share the selected installer source")
						}
						if build.Name == "nktp" {
							command := strings.Join(node.VendorCmd, " ")
							for _, required := range []string{"sh -euc", "HELM_REPOSITORY_CONFIG=/tmp/", "HELM_REPOSITORY_CACHE=/tmp/", "helm repo add nktp-otel https://open-telemetry.github.io/opentelemetry-helm-charts", "helm repo add nktp-prometheus https://prometheus-community.github.io/helm-charts", "helm repo add nktp-metrics https://kubernetes-sigs.github.io/metrics-server/", "helm dependency build nktp/deploy/charts/nktp --skip-refresh", "sha256sum -c -"} {
								if !strings.Contains(command, required) {
									t.Fatalf("NKTP dependency preparation lacks %q", required)
								}
							}
						}
						if build.Name == "nkectl" {
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
							if node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || !strings.Contains(strings.Join(node.Command, " "), "--local dockerfile=/src/controller") {
								t.Fatal("controller must use its source-owned Dockerfile with declared build networking")
							}
						} else if node.Network != ir.NetworkNone || !node.SourceReadOnly {
							t.Fatal("CLI/chart producers must consume prepared sources offline and read-only")
						}
						if build.Name == "nkectl" && !strings.Contains(strings.Join(node.Command, " "), "make -C cli build NKECTL_TREE=clean") {
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
