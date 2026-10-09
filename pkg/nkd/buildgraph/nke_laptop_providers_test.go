// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestLaptopCAPNICoSourceBuild(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "definitions", "nke-local-dev-arm64", "capnico-catalog")))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		platform string
		wantCode e.Code
	}{
		{name: "native source build", platform: "linux/arm64"},
		{name: "no implicit foreign target", platform: "linux/amd64", wantCode: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := Resolve(cat, Selection{Components: []string{"capnico-controller"}, Platform: tc.platform, OutputFormat: "oci"})
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
			builds := 0
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindPull {
					t.Fatal("CAPNICo must not fall back to an acquired controller image")
				}
				if node.Kind != ir.KindBuild {
					continue
				}
				builds++
				if node.SourceCommit != "30882520888ab7bb75d125cb7bb9aa89e48acd6a" || node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || node.OutputType != "oci-image" {
					t.Fatalf("source-owned wrapped producer changed: %+v", node)
				}
				command := strings.Join(node.Command, " ")
				if !strings.Contains(command, "--opt platform=linux/arm64") || !strings.Contains(command, "--local dockerfile=/src") || !strings.Contains(command, "capnico-controller-arm64.oci.tar") {
					t.Fatalf("incorrect source-owned Dockerfile invocation: %s", command)
				}
			}
			if builds != 1 {
				t.Fatalf("build nodes=%d want 1", builds)
			}
		})
	}
}
