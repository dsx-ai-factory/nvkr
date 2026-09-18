package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestProducerExecutionBindsInstructionWorkspaceAndResult(t *testing.T) {
	for _, tc := range []struct {
		name, runtime string
		mutate        func(*pipeline.ArtifactResult) *pipeline.ArtifactResult
		code          e.Code
	}{
		{name: "docker", runtime: "docker"},
		{name: "podman", runtime: "podman"},
		{name: "missing", runtime: "docker", mutate: func(*pipeline.ArtifactResult) *pipeline.ArtifactResult { return nil }, code: e.CodeVerifyFailed},
		{name: "wrong component", runtime: "docker", mutate: func(r *pipeline.ArtifactResult) *pipeline.ArtifactResult { r.Component = "sibling"; return r }, code: e.CodeVerifyFailed},
		{name: "wrong format", runtime: "docker", mutate: func(r *pipeline.ArtifactResult) *pipeline.ArtifactResult { r.OutputType = "other"; return r }, code: e.CodeVerifyFailed},
		{name: "wrong path", runtime: "docker", mutate: func(r *pipeline.ArtifactResult) *pipeline.ArtifactResult { r.AbsPath = ".nkd/other"; return r }, code: e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, err := relocateFactoryPlan(githubTestPlan(t), ".nkd/alternate")
			if err != nil {
				t.Fatal(err)
			}
			var selected Node
			for _, node := range plan.Graph.Nodes {
				if node.Kind == NodeBuild {
					selected = node
					break
				}
			}
			_, instruction, err := plan.producerInstruction(selected)
			if err != nil {
				t.Fatal(err)
			}
			_, artifacts := planIndex(plan)
			planned := artifacts["artifact/"+selected.BuildTarget+"/"+instruction.Component]
			resultPath := artifacts["result/"+selected.BuildTarget+"/"+instruction.Component].Path
			called := false
			run := func(node ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
				called = true
				if node.ID != instruction.ID || node.Toolchain != instruction.Toolchain || node.Network != instruction.Network ||
					!options.Launch || options.Runtime != tc.runtime || options.Jobs != 3 || filepath.ToSlash(options.SourceRoot) != ".nkd/alternate/worker/src" {
					t.Errorf("producer execution lost exact instruction or options: %+v %+v", node, options)
				}
				r := &pipeline.ArtifactResult{Component: node.Component, OutputType: node.OutputType, AbsPath: planned.Path,
					Qualification: pipeline.ArtifactUnqualified, SHA256: "sha256:" + strings.Repeat("a", 64)}
				if tc.mutate != nil {
					r = tc.mutate(r)
				}
				return r, nil
			}
			err = executeProducer(context.Background(), plan, selected, tc.runtime, 3, io.Discard, io.Discard, run)
			if !called {
				t.Fatal("producer was not invoked")
			}
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				if _, err := os.Stat(resultPath); !os.IsNotExist(err) {
					t.Fatalf("rejected result was persisted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(resultPath)
			if err != nil {
				t.Fatal(err)
			}
			var got pipeline.ArtifactResult
			if err := canonical.UnmarshalExact(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Component != instruction.Component {
				t.Fatal("wrong result slot")
			}
		})
	}
}
