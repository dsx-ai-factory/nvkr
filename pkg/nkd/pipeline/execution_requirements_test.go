// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
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
	graph := testSourceGraph(fetch, build)
	digest := mustGraphDigest(t, graph)

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "direct node", run: func() error {
			_, err := (Local{}).ExecNode(build, RunOptions{Ctx: context.Background(), DryRun: true})
			return err
		}},
		{name: "local graph", run: func() error {
			_, err := (Local{}).Run(graph, digest, RunOptions{Ctx: context.Background(), DryRun: true})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); code(err) != nkderr.CodeEngineCapability {
				t.Fatalf("adapter returned %v, want %s", err, nkderr.CodeEngineCapability)
			}
		})
	}
}

func TestLocalGraphValidatesBeforeCapabilityAdmission(t *testing.T) {
	toolchain := "example.test/toolchain@sha256:" + strings.Repeat("c", 64)
	fetch := testFetchNode("fetch-source", "https://example.test/source", testSourceCommit, testSourceTree,
		"source", toolchain, "")
	build := completeTestBuild(ir.Node{ID: "build-artifact", Kind: ir.KindBuild, Component: "artifact"}, fetch)
	graph := testSourceGraph(fetch, build)
	foundFetch := false
	for i := range graph.Nodes {
		if graph.Nodes[i].Kind == ir.KindFetch {
			graph.Nodes[i].Requirements = &execution.Requirements{
				Resources: &execution.ResourceRequest{MemoryMiB: 4096},
			}
			foundFetch = true
			break
		}
	}
	if !foundFetch {
		t.Fatal("test graph has no fetch node")
	}

	for _, test := range []struct {
		name     string
		graph    *ir.Graph
		wantText string
	}{
		{name: "nil graph", wantText: "pipeline requires an IR graph"},
		{name: "requirements on fetch", graph: graph, wantText: "forbidden field requirements"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Local{}).Run(test.graph, "sha256:"+strings.Repeat("a", 64), RunOptions{Ctx: context.Background(), DryRun: true})
			if code(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("invalid graph returned %v, want %s containing %q",
					err, nkderr.CodeInvalidCatalog, test.wantText)
			}
		})
	}
}

func TestTerminalVerificationRejectsExecutionRequirementsBeforeQualificationOrPathAccess(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	node, opts, _ := qualifiedResultOptions(t, filepath.Join(work, "src"))
	node.Requirements = &execution.Requirements{
		Resources:            &execution.ResourceRequest{MemoryMiB: 4096},
		AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
	}
	graph, digest := qualifiedResultGraph(t, node)
	results := filepath.Join(work, "must-not-be-read")

	if _, err := (Local{}).VerifyResults(graph, digest, results, opts); code(err) != nkderr.CodeEngineCapability {
		t.Fatalf("terminal verifier returned %v, want %s", err, nkderr.CodeEngineCapability)
	}
	if _, err := os.Stat(results); !os.IsNotExist(err) {
		t.Fatalf("terminal verifier touched result path before capability admission: %v", err)
	}
}
