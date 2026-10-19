// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Compiler admission is not proof that the upstream tools were built or that a
// management cluster, site or QCOW2-backed tenant runs.
func TestLaptopInstallerToolsContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	path := filepath.Join("inputs", "definitions", "diagnostics", "demo-arm64", "tools.json")
	for _, tc := range []struct {
		name               string
		foreign, duplicate bool
		wantCode           e.Code
	}{
		{"Docker", false, false, ""},
		{"foreign target", true, false, e.CodeNoMatch},
		{"duplicate tool", false, true, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := definition.LoadDefinition(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			envPath := "inputs/environments/local-arm64-tcg.json"
			env, err := definition.LoadEnvironment(t.Context(), envPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.foreign {
				doc.Outputs[0].Platform = "linux/amd64"
			}
			if tc.duplicate {
				doc.Outputs[0].Components = append(doc.Outputs[0].Components, "helm")
			}
			plan, err := CompileDocuments(t.Context(), path, doc, envPath, env, "tools-contract")
			if tc.wantCode != "" {
				assertFactoryErrorCode(t, err, tc.wantCode)
				if plan != nil {
					t.Fatal("invalid selection returned an executable Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			want := []string{"clusterctl", "helm", "kind", "kubectl", "oras"}
			if len(plan.Builds) != 1 || !slices.Equal(plan.Builds[0].Components, want) ||
				len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "installer-tools-arm64" {
				t.Fatal("tools must be one complete five-component executable bundle")
			}
			versions := map[string]string{"clusterctl": "v1.12.3", "helm": "v4.2.4", "kind": "v0.32.0", "kubectl": "v1.36.0", "oras": "v1.3.3"}
			for _, component := range plan.Builds[0].ResolvedComponents {
				if component.Tag != versions[component.Name] || !component.FromSource || component.Prebuilt != nil ||
					component.OutputType != "raw" || component.ArtifactPath != "nvkr-output/"+component.Name {
					t.Fatalf("%s must compile the exact upstream tool, not acquire a release binary", component.Name)
				}
			}
			fetches, producers := 0, 0
			for _, node := range plan.Builds[0].Graph.Nodes {
				switch node.Kind {
				case ir.KindPull:
					t.Fatal("tools must not fall back to prebuilt release acquisition")
				case ir.KindFetch:
					fetches++
					command := strings.Join(node.VendorCmd, " ")
					if command != "" {
						for _, required := range []string{"GOTOOLCHAIN=local", "go mod download", "go mod verify", "go mod vendor", "sha256sum --quiet -c -"} {
							if !strings.Contains(command, required) {
								t.Fatalf("incomplete bounded preparation: missing %s", required)
							}
						}
					}
					if node.Repo == "https://github.com/helm/helm" {
						if strings.Count(command, "test \"$(readlink ") != 4 || !strings.Contains(command, "held hosted fetch route") ||
							!strings.Contains(command, "git -c safe.directory=/src rm --") || strings.Contains(command, "safe.directory=*") {
							t.Fatal("Helm fixture preparation must be exact, checked and explicitly local-only")
						}
						for _, path := range []string{
							"internal/chart/v3/loader/testdata/frobnitz_with_dev_null/null",
							"pkg/chart/v2/loader/testdata/frobnitz_with_dev_null/null",
							"internal/third_party/dep/fs/testdata/symlinks/invalid-symlink",
							"internal/third_party/dep/fs/testdata/symlinks/windows-file-symlink",
						} {
							if strings.Count(command, path) != 2 {
								t.Fatalf("Helm fixture %s must be target-checked and removed exactly once", path)
							}
						}
					}
				case ir.KindBuild:
					producers++
					if node.BuilderType != ir.BuilderOCIHermetic || node.Network != ir.NetworkNone || !node.SourceReadOnly ||
						node.Toolchain != "docker.io/library/golang@sha256:2bcff0c6ad6e8c5353e8c0d5c7f17bfcc87d667d0169615e34db1acc0db985b9" {
						t.Fatal("prepared tools must build offline in the exact native compiler image")
					}
					command := strings.Join(node.Command, " ")
					if !strings.Contains(command, "GOPROXY=off") || !strings.Contains(command, "-mod=vendor") || strings.Contains(command, "GOFLAGS=") {
						t.Fatal("offline producers must preserve the compiler's reproducibility GOFLAGS")
					}
					if node.Component == "helm" && (!strings.Contains(command, "version.metadata=") || !strings.Contains(command, "version --template")) {
						t.Fatal("Helm must stamp and execute the exact selected version")
					}
					if node.Component == "kubectl" && (!strings.Contains(command, "k8s.io/component-base/version.gitVersion=") || !strings.Contains(command, "k8s.io/client-go/pkg/version.gitVersion=")) {
						t.Fatal("kubectl must stamp both upstream version namespaces")
					}
				}
			}
			if fetches != 5 || producers != 5 {
				t.Fatalf("fetches=%d producers=%d; want five independently pinned source tools", fetches, producers)
			}
			for _, node := range plan.Graph.Nodes {
				if node.Kind == "sign" || node.Kind == "publish" {
					t.Fatal("the local tools lane cannot imply signed publication")
				}
			}
		})
	}
}
