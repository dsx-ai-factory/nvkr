// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// TestExecNodeLaunchOrchestratesContainer proves helper-orchestrated dispatch: with
// --launch, exec-node orchestrates the pinned toolchain container itself with
// --network=none (the Local.Run path), whereas the default runs the build directly in an
// assumed engine-provided container. Uses dry-run so no container is actually started.
func TestExecNodeLaunchOrchestratesContainer(t *testing.T) {
	build := sampleGraph().Nodes[0]
	if build.Kind != ir.KindBuild || build.Network != ir.NetworkNone {
		t.Fatalf("fixture changed: want an isolated build node, got kind=%q network=%q", build.Kind, build.Network)
	}
	run := func(launch bool) string {
		var out bytes.Buffer
		if _, err := (Local{}).ExecNode(build, RunOptions{
			Ctx: context.Background(), DryRun: true, Launch: launch, Runtime: "podman",
			Stdout: &out, Stderr: &out,
		}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if launched := run(true); !strings.Contains(launched, "run "+build.Toolchain+" with --network=none") {
		t.Fatalf("--launch must orchestrate the toolchain container with --network=none, got:\n%s", launched)
	}
	if direct := run(false); !strings.Contains(direct, "sandbox=") {
		t.Fatalf("default (no --launch) must use the in-container direct path, got:\n%s", direct)
	}
}
