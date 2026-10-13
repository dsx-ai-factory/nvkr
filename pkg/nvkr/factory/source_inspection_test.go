// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestInspectRestoredSourceRejectsUnboundSelectionBeforeWork(t *testing.T) {
	for _, scenario := range []string{"unknown node", "non fetch", "duplicate node", "missing receipt", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			plan := githubTestPlan(t)
			nodeID := "unknown"
			ctx := t.Context()
			code := e.CodeInvalidCatalog
			for _, node := range plan.Graph.Nodes {
				if (scenario == "non fetch" && node.Kind != NodeFetch) || (scenario != "non fetch" && node.Kind == NodeFetch) {
					nodeID = node.ID
					break
				}
			}
			switch scenario {
			case "unknown node":
				nodeID = "unknown"
			case "duplicate node":
				plan.Graph.Nodes = append(plan.Graph.Nodes, plan.Graph.Nodes[0])
			case "missing receipt":
				code = e.CodeFetchFailed
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			}
			t.Chdir(t.TempDir())
			_, err := InspectRestoredSource(ctx, plan, nodeID)
			assertFactoryErrorCode(t, err, code)
			files, err := os.ReadDir(".")
			if err != nil || len(files) != 0 {
				t.Fatalf("rejected inspection wrote output: %v / %v", files, err)
			}
		})
	}
}
