// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeGoRequiresExplicitHostAndNonHermeticContract(t *testing.T) {
	for _, scenario := range []string{"exact", "container compiler", "claimed read-only", "claimed offline", "shell command", "OCI identity", "wrong host"} {
		t.Run(scenario, func(t *testing.T) {
			node := sample().Nodes[0]
			node.BuilderType = BuilderNativeGo
			node.Command = []string{"go", "build", "-o", "out/kubelet", "./cmd/kubelet"}
			node.Toolchain = "sha256:" + strings.Repeat("d", 64)
			node.ToolchainPlatform = node.Platform
			node.Network = NetworkDefault
			node.SourceReadOnly = false
			want := e.CodeEngineCapability
			switch scenario {
			case "container compiler":
				node.BuilderType = BuilderOCIHermetic
				want = e.CodeInvalidCatalog
			case "claimed read-only":
				node.SourceReadOnly = true
			case "claimed offline":
				node.Network = NetworkNone
				want = e.CodeInvalidCatalog
			case "shell command":
				node.Command[0] = "bash"
			case "OCI identity":
				node.Toolchain = "golang@" + node.Toolchain
			case "wrong host":
				node.ToolchainPlatform = "darwin/arm64"
			}
			err := node.Validate()
			if scenario == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertIRCode(t, err, want)
		})
	}
}
