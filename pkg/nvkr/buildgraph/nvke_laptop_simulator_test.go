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
	testNICoSimSourceBuild(t, "nicosim", true)
}

func TestNativeNICoSimSourceBuild(t *testing.T) {
	testNICoSimSourceBuild(t, "tools/native", false)
}

func testNICoSimSourceBuild(t *testing.T, directory string, patched bool) {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "inputs/catalogs", directory)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, platform string
		components     []string
		wantCode       e.Code
	}{
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantCode := tc.wantCode
			if patched && (tc.platform == "darwin/arm64" || tc.platform == "linux/amd64") {
				wantCode = e.CodeEngineCapability
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
					wantCGO := node.Component == "nico-sim" || (patched && node.Component == "nico-simctl")
					if cgo != wantCGO {
						t.Fatalf("unexpected CGO selection for %s: got %t, want %t", node.Component, cgo, wantCGO)
					}
					if patched && node.Component != "nico-sim-runtime-assets" {
						if len(node.Patches) != 1 || node.Patches[0].Content == "" || node.Patches[0].SHA256 != "sha256:186d3ca32819bdb0f299732414de8982e67e0b5561b9914c16a26f11641b6cde" {
							t.Fatal("NICoSim version interface must be a byte-bound source patch")
						}
						stamps := map[string]string{}
						for _, variable := range node.Env {
							stamps[variable.Name] = variable.Value
						}
						if stamps["NICO_SIM_VERSION"] != "v"+node.Version || stamps["NICO_SIM_COMMIT"] != node.SourceCommit || stamps["NICO_SIM_TREE"] != "patched" {
							t.Fatal("NICoSim must report its exact base revision and declared patched state")
						}
					}
					if !patched && len(node.Patches) != 0 {
						t.Fatal("native simulator must use the selected upstream source without patches")
					}
					command := strings.Join(node.Command, " ")
					makeCommand := "make build-static"
					if patched {
						makeCommand = "make -C controller/nico-sim build-static"
					}
					if node.Component == "nico-sim" && !strings.Contains(command, makeCommand) {
						t.Fatal("server must invoke the source-owned static Make target")
					}
					if patched && node.Component == "nico-simctl" && !strings.Contains(command, "-extldflags -static") {
						t.Fatal("qualified-lane CLI must retain the declared static Linux link")
					}
					if node.Component == "nico-sim-runtime-assets" {
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
