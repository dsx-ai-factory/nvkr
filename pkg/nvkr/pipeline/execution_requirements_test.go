// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestCurrentPipelineAdaptersRejectExecutionRequirements(t *testing.T) {
	toolchain := "example.test/toolchain@sha256:" + strings.Repeat("c", 64)
	fetch := testFetchNode("fetch-source", "https://example.test/source", testSourceCommit, testSourceTree,
		"source", toolchain, "")
	build := completeTestBuild(ir.Node{ID: "build-artifact", Kind: ir.KindBuild, Component: "artifact",
		Requirements: &execution.Requirements{
			Resources: &execution.ResourceRequest{
				CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
			},
			AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			Capabilities: []execution.CapabilityRequirement{{
				Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
			}},
			TimeoutSeconds: 7200,
		},
	}, fetch)
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "direct node", run: func() error {
			_, err := (Local{}).ExecNode(build, RunOptions{Ctx: context.Background(), DryRun: true})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); code(err) != nvkrerr.CodeEngineCapability {
				t.Fatalf("adapter returned %v, want %s", err, nvkrerr.CodeEngineCapability)
			}
		})
	}
}
