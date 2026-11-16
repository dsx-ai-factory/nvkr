// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

func TestBuildToolEvidenceUsesSelectedNativePin(t *testing.T) {
	component := catalog.NamedComponent{Name: "consumer", Component: catalog.Component{FromSource: true,
		BuildTool: map[string]string{"linux/amd64": "nfpm-amd64", "linux/arm64": "nfpm-arm64"},
		BuildToolPins: map[string]catalog.ToolPin{
			"linux/amd64": {ArchiveDigest: "amd64-archive", ExecutableDigest: "amd64-executable"},
			"linux/arm64": {ArchiveDigest: "arm64-archive", ExecutableDigest: "arm64-executable"},
		},
	}}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			got := componentForArch(component, "", arch)
			props := map[string]string{}
			for _, property := range got.Properties {
				props[property.Name] = property.Value
			}
			for name, want := range map[string]string{"nvkr:lane": "wrapped-build", "nvkr:fromSource": "true",
				"nvkr:tool": "nvkr:tool/nfpm-" + arch, "nvkr:toolArchive": arch + "-archive", "nvkr:toolExecutable": arch + "-executable"} {
				if props[name] != want {
					t.Fatalf("%s: got %q, want %q", name, props[name], want)
				}
			}
		})
	}
}
