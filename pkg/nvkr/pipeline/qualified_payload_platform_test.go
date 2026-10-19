// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/profile"
)

func TestQualifiedDarwinRefusalPrecedesFetchRuntimeAndResults(t *testing.T) {
	for _, output := range []string{"host-package", "tarball"} {
		for _, terminal := range []bool{false, true} {
			name := output + "/execution"
			if terminal {
				name = output + "/terminal"
			}
			t.Run(name, func(t *testing.T) {
				work := filepath.Join(t.TempDir(), "absent-work")
				node, opts, _ := qualifiedResultOptions(t, filepath.Join(work, "src"))
				node.Platform, node.ToolchainPlatform, node.OutputType = "darwin/arm64", "linux/arm64", output
				node.Env = testBuildEnv("arm64")
				for i := range node.Env {
					if node.Env[i].Name == "GOOS" {
						node.Env[i].Value = "darwin"
					}
				}
				fetch := testFetchNode(node.Needs[0], "https://example.test/source", node.SourceCommit, node.SourceTreeHash, node.SourceDest, node.Toolchain, node.VendorSpecDigest)
				graph := testSourceGraph(fetch, node)
				graph.Platform = node.Platform
				if err := graph.Validate(); err != nil {
					t.Fatalf("refusal fixture must be a valid diagnostic graph: %v", err)
				}
				opts.Qualification.Profile.Repin(profile.FieldTargetArch, "arm64", true)
				host, divergences, err := opts.Qualification.Profile.VerifyHost(t.Context(), map[string]string{profile.FieldTargetArch: "arm64"})
				if err != nil || len(divergences) != 0 {
					t.Fatalf("fixture host admission: %v %v", err, divergences)
				}
				opts.Qualification.HostReceipt = host
				calls := 0
				opts.Qualification.Observer = pipelineExecutionObserverFunc(func(context.Context, ExecutionObservationRequest) (profile.ExecutionObservation, error) {
					calls++
					return profile.ExecutionObservation{}, errors.New("must not observe")
				})
				opts.Runtime, opts.Ctx = filepath.Join(work, "absent-runtime"), t.Context()
				digest := mustGraphDigest(t, graph)
				if terminal {
					_, err = (Local{}).VerifyResults(graph, digest, filepath.Join(work, "absent-results"), opts)
				} else {
					_, err = (Local{}).Run(graph, digest, opts)
				}
				if code(err) != e.CodeEngineCapability || calls != 0 {
					t.Fatalf("qualified Darwin performed work or returned wrong error: %v, observer calls=%d", err, calls)
				}
				if _, err := os.Stat(work); !os.IsNotExist(err) {
					t.Fatal("qualified refusal created execution state")
				}
			})
		}
	}
}
