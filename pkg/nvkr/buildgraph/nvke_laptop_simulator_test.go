// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func TestLaptopNICoSimSourceBuild(t *testing.T) {
	testNICoSimSourceBuild(t, "tools", true)
}

func TestNativeNICoSimSourceBuild(t *testing.T) {
	testNICoSimSourceBuild(t, "tools", false)
}

func testNICoSimSourceBuild(t *testing.T, directory string, patched bool) {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "inputs/catalogs", directory)))
	if err != nil {
		t.Fatal(err)
	}
	type simulatorCase struct {
		name, platform string
		components     []string
		wantCode       e.Code
	}
	cases := []simulatorCase{
		{"server", "linux/arm64", []string{"nico-sim"}, ""},
		{"CLI", "linux/arm64", []string{"nico-simctl"}, ""},
		{"runtime assets", "linux/arm64", []string{"nico-sim-runtime-assets"}, ""},
		{"both share one prepared source", "linux/arm64", []string{"nico-sim", "nico-simctl"}, ""},
		{"self-contained simulator selection", "linux/arm64", []string{"nico-sim", "nico-sim-runtime-assets", "nico-simctl"}, ""},
		{"Mac simulator requires an admitted CGO toolchain", "darwin/arm64", []string{"nico-sim"}, e.CodeEngineCapability},
		{"Mac API CLI remains cross-buildable", "darwin/arm64", []string{"nico-simctl"}, ""},
		{"AMD64 server", "linux/amd64", []string{"nico-sim"}, ""},
		{"AMD64 CLI", "linux/amd64", []string{"nico-simctl"}, ""},
		{"AMD64 runtime assets", "linux/amd64", []string{"nico-sim-runtime-assets"}, ""},
		{"AMD64 tools share one prepared source", "linux/amd64", []string{"nico-sim", "nico-sim-runtime-assets", "nico-simctl"}, ""},
		{"unsupported architecture fails closed", "linux/riscv64", []string{"nico-sim"}, e.CodeInvalidCatalog},
	}
	if patched {
		// Only the runtime-assets variant remains from this retired executable cohort.
		cases = []simulatorCase{
			{"runtime assets", "linux/arm64", []string{"nico-sim-runtime-assets"}, ""},
			{"AMD64 runtime assets", "linux/amd64", []string{"nico-sim-runtime-assets"}, e.CodeEngineCapability},
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCode := tc.wantCode
			if patched {
				for i := range tc.components {
					tc.components[i] += "-nicosim"
				}
			}
			resolved, err := Resolve(cat, Selection{Components: tc.components, Platform: tc.platform, OutputFormat: "executable"})
			if wantCode != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != wantCode {
					t.Fatalf("wanted %s, got %v", wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			graph := mustCompile(t, resolved)
			fetches, builds := 0, 0
			for _, node := range graph.Nodes {
				switch node.Kind {
				case ir.KindPull:
					t.Fatal("simulator must not fall back to acquired application binaries")
				case ir.KindFetch:
					fetches++
					command := strings.Join(node.VendorCmd, " ")
					if !strings.Contains(command, "go mod verify") || !strings.Contains(command, "go mod vendor") || !strings.Contains(command, "sha256sum --quiet -c") {
						t.Fatalf("prepared source must retain module integrity checks: %s", command)
					}
				case ir.KindBuild:
					componentName := strings.TrimSuffix(node.Component, "-nicosim")
					builds++
					if node.SourceCommit != cat.Components[node.Component].Commit || node.Network != ir.NetworkNone || node.BuilderType != ir.BuilderOCIHermetic || !node.SourceReadOnly {
						t.Fatalf("pinned offline source producer changed: %+v", node)
					}
					cgo := false
					arch := ""
					for _, variable := range node.Env {
						if variable.Name == "CGO_ENABLED" && variable.Value == "1" {
							cgo = true
						}
						if variable.Name == "GOARCH" {
							arch = variable.Value
						}
					}
					if node.Platform != tc.platform || arch != target.ExecutableArch(tc.platform) {
						t.Fatalf("simulator build target differs from selection: platform=%s GOARCH=%s", node.Platform, arch)
					}
					wantCGO := componentName == "nico-sim"
					if cgo != wantCGO {
						t.Fatalf("unexpected CGO selection for %s: got %t, want %t", node.Component, cgo, wantCGO)
					}
					if !patched && len(node.Patches) != 0 {
						t.Fatal("native simulator must use the selected upstream source without patches")
					}
					command := strings.Join(node.Command, " ")
					makeCommand := "make build-static"
					if componentName == "nico-sim" && !strings.Contains(command, makeCommand) {
						t.Fatal("server must invoke the source-owned static Make target")
					}
					if componentName == "nico-sim-runtime-assets" {
						if cgo || node.OutputType != "tarball" {
							t.Fatal("runtime data is an archive, not another CGO executable")
						}
						for _, required := range []string{"set -o pipefail", "--sort=name", "--format=ustar", "SOURCE_DATE_EPOCH", "--owner=0 --group=0", "internal/seed/default-seed.yaml ui", "gzip -n"} {
							if !strings.Contains(command, required) {
								t.Fatalf("asset producer lacks deterministic pinned-source input %q: %s", required, command)
							}
						}
					}
				}
			}
			if fetches != 1 || builds != len(tc.components) {
				t.Fatalf("fetches=%d builds=%d, want one shared source and %d builds", fetches, builds, len(tc.components))
			}
		})
	}
}
