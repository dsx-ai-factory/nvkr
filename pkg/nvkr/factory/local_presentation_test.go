// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestLocalNodeLabel(t *testing.T) {
	fetch := ir.Node{ID: "fetch-source-" + strings.Repeat("a", 64), Kind: ir.KindFetch, Repo: "https://example.invalid/nvke.git", Commit: strings.Repeat("b", 40), TreeHash: strings.Repeat("c", 40)}
	vendor := fetch
	vendor.VendorCmd = []string{"go", "mod", "vendor"}
	build := ir.Node{ID: "build-hello", Kind: ir.KindBuild, Component: "hello", SourceDest: "source"}
	pull := ir.Node{ID: "pull-hello", Kind: ir.KindPull, Component: "hello"}
	controlRepo := fetch
	controlRepo.Repo = "https://example.invalid/nvke\x1b[31m.git"
	controlComponent := build
	controlComponent.Component = "hello\r\nforged\tline"
	for _, test := range []struct {
		name        string
		instruction ir.Node
		tags        []string
		verbose     bool
		want        string
	}{
		{"tagged fetch", fetch, []string{"v0.23.0"}, false, "nvkectl: fetch nvke@v0.23.0"},
		{"verbose", fetch, nil, true, "nvkectl/" + fetch.ID + " (fetch)"},
		{"vendor", vendor, []string{"v0.23.0"}, false, "nvkectl: fetch nvke@v0.23.0 (including vendor dependencies)"},
		{"snapshot", build, nil, false, "nvkectl: build hello (including source snapshot)"},
		{"acquire", pull, nil, false, "nvkectl: acquire hello"},
		{"ambiguous tags", fetch, []string{"v0.23.0", "v0.24.0"}, false, "nvkectl: fetch nvke"},
		{"repository control", controlRepo, nil, false, `nvkectl: fetch nvke\x1b[31m`},
		{"tag control", fetch, []string{"v1\n\x1b[2J"}, false, `nvkectl: fetch nvke@v1\n\x1b[2J`},
		{"component control", controlComponent, nil, false, `nvkectl: build hello\r\nforged\tline (including source snapshot)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := Node{ID: "nvkectl/" + test.instruction.ID, Kind: map[string]string{ir.KindFetch: NodeFetch, ir.KindBuild: NodeBuild, ir.KindPull: NodeAcquire}[test.instruction.Kind], BuildTarget: "nvkectl", IRNode: test.instruction.ID}
			plan := &Plan{Builds: []BuildPlan{{Name: "nvkectl", Graph: &ir.Graph{Nodes: []ir.Node{test.instruction}}}}}
			for _, tag := range test.tags {
				plan.Builds[0].ResolvedComponents = append(plan.Builds[0].ResolvedComponents, catalog.NamedComponent{Name: "nvkectl", Component: catalog.Component{Repo: test.instruction.Repo, Commit: test.instruction.Commit, TreeHash: test.instruction.TreeHash, Tag: tag}})
			}
			before, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if got := localNodeLabel(plan, node, test.verbose); got != test.want {
				t.Fatalf("label=%q, want %q", got, test.want)
			}
			after, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("display changed serialized Plan")
			}
		})
	}
	for _, test := range []struct {
		name    string
		node    Node
		verbose bool
		want    string
	}{
		{"utility", Node{ID: "tool/go", Kind: NodeAcquire}, false, "tool/go: acquire"},
		{"target control", Node{ID: "build", Kind: NodeBuild, BuildTarget: "output\nforged"}, false, `output\nforged: build`},
		{"verbose control", Node{ID: "tool/\x1b[31m", Kind: NodeAcquire}, true, `tool/\x1b[31m (acquire)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := localNodeLabel(&Plan{}, test.node, test.verbose); got != test.want {
				t.Fatalf("label=%q, want %q", got, test.want)
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
