// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestProducerNativeHostedRouting(t *testing.T) {
	for _, tc := range []struct {
		name, platform, instruction, kind, explicit, arc, want string
	}{
		{name: "ARM fetch", platform: "linux/arm64", instruction: ir.KindFetch, kind: NodeFetch, want: "ubuntu-24.04-arm"},
		{name: "ARM acquisition", platform: "linux/arm64", instruction: ir.KindPull, kind: NodeAcquire, want: "ubuntu-24.04-arm"},
		{name: "ARM build", platform: "linux/arm64", instruction: ir.KindBuild, kind: NodeBuild, want: "ubuntu-24.04-arm"},
		{name: "AMD build", platform: "linux/amd64", instruction: ir.KindBuild, kind: NodeBuild, want: DefaultRunsOn},
		{name: "explicit ARM route", platform: "linux/arm64", instruction: ir.KindBuild, kind: NodeBuild, explicit: "ubuntu-24.04", want: "ubuntu-24.04"},
		{name: "ARC source bootstrap", platform: "linux/arm64", instruction: ir.KindFetch, kind: NodeFetch, arc: "native-kvm-v1", want: "ubuntu-24.04-arm"},
		{name: "ARC non-disk acquisition unchanged", platform: "linux/arm64", instruction: ir.KindPull, kind: NodeAcquire, arc: "native-kvm-v1", want: DefaultRunsOn},
		{name: "ARC non-disk build unchanged", platform: "linux/arm64", instruction: ir.KindBuild, kind: NodeBuild, arc: "native-kvm-v1", want: DefaultRunsOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Test only the scheduler's ephemeral projection. Complete authored Plan
			// admission and VM/ARC grouping remain covered by their contract tests.
			plan := Plan{
				Environment: definition.Environment{RunsOn: tc.explicit, GitHubARC: tc.arc},
				Builds: []BuildPlan{{Name: "output", Graph: &ir.Graph{
					Platform: tc.platform,
					Nodes:    []ir.Node{{ID: "producer", Kind: tc.instruction, Platform: tc.platform}},
				}}},
			}
			node, err := plan.ExecutionFor(Node{ID: "output/producer", IRNode: "producer", BuildTarget: "output", Kind: tc.kind})
			if err != nil {
				t.Fatal(err)
			}
			if node.RunsOn != tc.want || node.Platform != tc.platform {
				t.Fatalf("route = %s/%s, want %s/%s", node.RunsOn, node.Platform, tc.want, tc.platform)
			}
		})
	}
}
