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
)

func TestLaptopNICoSimSourceBuild(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "inputs/catalogs/nicosim")))
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
		{"foreign platform fails closed", "linux/amd64", []string{"nico-sim"}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := Resolve(cat, Selection{Components: tc.components, Platform: tc.platform, OutputFormat: "executable"})
			if tc.wantCode != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.wantCode {
					t.Fatalf("wanted %s, got %v", tc.wantCode, err)
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
					if node.SourceCommit != "6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4" || node.Network != ir.NetworkNone || node.BuilderType != ir.BuilderOCIHermetic || !node.SourceReadOnly {
						t.Fatalf("pinned offline source producer changed: %+v", node)
					}
					cgo := false
					for _, variable := range node.Env {
						if variable.Name == "CGO_ENABLED" && variable.Value == "1" {
							cgo = true
						}
					}
					if node.Component != "nico-sim-runtime-assets" && !cgo {
						t.Fatal("SQLite simulator requires a CGO-capable build")
					}
					command := strings.Join(node.Command, " ")
					if node.Component == "nico-sim" && !strings.Contains(command, "make -C controller/nico-sim build-static") {
						t.Fatal("server must invoke the source-owned static Make target")
					}
					if node.Component == "nico-simctl" && !strings.Contains(command, "-extldflags -static") {
						t.Fatal("CLI must be portable to the Linux demo runtime")
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
