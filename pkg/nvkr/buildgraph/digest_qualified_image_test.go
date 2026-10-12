// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"errors"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestResolvePrebuiltImageBinding(t *testing.T) {
	for _, architecture := range []struct{ arch, pin, other string }{{"amd64", digAmd, digArm}, {"arm64", digArm, digAmd}} {
		for _, test := range []struct {
			name, ref, want string
			code            e.Code
		}{
			{"same digest", "registry.example/tool@" + architecture.pin, "registry.example/tool@" + architecture.pin, ""},
			{"same tag-and-digest", "registry.example/tool:latest@" + architecture.pin, "registry.example/tool@" + architecture.pin, ""},
			{"same explicit-scheme", "oci://registry.example/tool@" + architecture.pin, "registry.example/tool@" + architecture.pin, ""},
			{"tag", "registry.example/tool:1.2.3", "registry.example/tool:1.2.3", ""},
			{"different digest", "registry.example/tool@" + architecture.other, "", e.CodeUnverifiedRef},
			{"different tag-and-digest", "registry.example/tool:latest@" + architecture.other, "", e.CodeUnverifiedRef},
			{"different explicit-scheme", "oci://registry.example/tool@" + architecture.other, "", e.CodeUnverifiedRef},
		} {
			t.Run(architecture.arch+"/"+test.name, func(t *testing.T) {
				cat := &catalog.Catalog{Components: map[string]catalog.Component{"tool": {
					Platforms: []string{"linux/" + architecture.arch}, OutputType: "oci-image", Tag: "selection",
					Prebuilt: &catalog.Prebuilt{Image: test.ref, Digests: map[string]string{architecture.arch: architecture.pin}, Evidence: &catalog.ExternalEvidence{None: true}},
				}}}
				resolved, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/" + architecture.arch})
				if test.code != "" {
					var coded *e.Error
					if resolved != nil || !errors.As(err, &coded) || coded.Code != test.code {
						t.Fatalf("unsupported authored digest admitted: resolved=%+v error=%v", resolved, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				graph, err := Compile(resolved)
				if err != nil || graph == nil {
					t.Fatal(err)
				}
				node := graph.Nodes[0]
				if node.Kind != ir.KindPull || node.Artifact != test.want || node.Digest != architecture.pin || node.Platform != "linux/"+architecture.arch {
					t.Fatalf("wrong native subject or transport: %+v", node)
				}
			})
		}
	}
}
