// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestLocalNodeLabel(t *testing.T) {
	instruction := ir.Node{ID: "fetch-source-" + strings.Repeat("a", 64), Kind: ir.KindFetch, Repo: "https://example.invalid/nvke.git", Commit: strings.Repeat("b", 40), TreeHash: strings.Repeat("c", 40)}
	node := Node{ID: "nvkectl/" + instruction.ID, Kind: NodeFetch, BuildTarget: "nvkectl", IRNode: instruction.ID}
	plan := &Plan{Builds: []BuildPlan{{Name: "nvkectl", Graph: &ir.Graph{Nodes: []ir.Node{instruction}}, ResolvedComponents: []catalog.NamedComponent{{Name: "nvkectl", Component: catalog.Component{Repo: instruction.Repo, Commit: instruction.Commit, TreeHash: instruction.TreeHash, Tag: "v0.23.0"}}}}}}
	for _, test := range []struct {
		name    string
		verbose bool
		want    string
	}{
		{"readable", false, "nvkectl: fetch nvke@v0.23.0"},
		{"verbose", true, node.ID + " (" + node.Kind + ")"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := localNodeLabel(plan, node, test.verbose); got != test.want {
				t.Fatalf("label=%q, want %q", got, test.want)
			}
			if plan.Builds[0].Graph.Nodes[0].ID != instruction.ID {
				t.Fatal("display changed node identity")
			}
		})
	}
}

func TestLocalHeartbeatStopsAndJoins(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "cancel"}[cancelFirst], func(t *testing.T) {
			var out bytes.Buffer
			writer := lockedLocalWriter{mu: &sync.Mutex{}, Writer: &out}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stop := startLocalHeartbeat(ctx, writer, "vendor dependencies", time.Millisecond)
			deadline := time.Now().Add(time.Second)
			for {
				writer.mu.Lock()
				seen := strings.Contains(out.String(), "[working] vendor dependencies")
				writer.mu.Unlock()
				if seen {
					break
				}
				if time.Now().After(deadline) {
					stop()
					t.Fatal("no heartbeat")
				}
				time.Sleep(time.Millisecond)
			}
			if cancelFirst {
				cancel()
			}
			stop()
			stop() // idempotent join
			before := out.String()
			time.Sleep(3 * time.Millisecond)
			if out.String() != before {
				t.Fatal("heartbeat wrote after join")
			}
		})
	}
}
