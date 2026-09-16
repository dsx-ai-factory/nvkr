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
	graph := testSourceGraph("requirements", fetch, build)
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
		{name: "job lowering", run: func() error {
			_, err := LowerJobs(graph)
			return err
		}},
		{name: "generated pipeline", run: func() error {
			_, err := GeneratedPipelineBytes(context.Background(), "test", digest, graph.RecipeID, graph, []byte("body\n"))
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
