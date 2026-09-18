package factory

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// Local and hosted execution use this same operation. The Plan selects the exact
// instruction, workspace and result slot; pipeline owns launching and collecting it.
func executeProducer(ctx context.Context, plan *Plan, node Node, runtime string, jobs int, stdout, stderr io.Writer,
	run func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error)) error {
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		return err
	}
	_, artifacts := planIndex(plan)
	var sourceRoot string
	if instruction.Kind == ir.KindFetch {
		source := artifacts["source/"+node.BuildTarget+"/"+node.IRNode]
		suffix := "/" + instruction.Dest
		if !strings.HasSuffix(source.Path, suffix) {
			return e.New(e.CodePipelineDrift, "source output for %q does not bind its destination", node.ID)
		}
		sourceRoot = strings.TrimSuffix(source.Path, suffix)
	} else {
		result := artifacts["result/"+node.BuildTarget+"/"+instruction.Component]
		if result.Path == "" {
			return e.New(e.CodePipelineDrift, "producer %q has no result slot", node.ID)
		}
		sourceRoot = filepath.Join(filepath.Dir(filepath.Dir(result.Path)), "src")
	}
	result, err := run(instruction, pipeline.RunOptions{
		Ctx: ctx, SourceRoot: sourceRoot, Runtime: runtime, Jobs: jobs, Launch: true, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		return err
	}
	if instruction.Kind == ir.KindFetch {
		if result != nil {
			return e.New(e.CodeVerifyFailed, "fetch %q returned an artifact result", node.ID)
		}
		return nil
	}
	if result == nil || result.Component != instruction.Component || result.OutputType != instruction.OutputType {
		return e.New(e.CodeVerifyFailed, "producer %q returned a missing or misbound result", node.ID)
	}
	expected := artifacts["artifact/"+node.BuildTarget+"/"+instruction.Component]
	actual := result.AbsPath
	if result.TransportPath != "" {
		actual = result.TransportPath
	}
	actualAbs, err := filepath.Abs(actual)
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "resolving result path")
	}
	expectedAbs, err := filepath.Abs(expected.Path)
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "resolving planned artifact path")
	}
	if actual == "" || actualAbs != expectedAbs {
		return e.New(e.CodePipelineDrift, "producer %q materialized outside its planned output", node.ID)
	}
	raw, err := canonical.Marshal(result)
	if err != nil {
		return err
	}
	return writeFactoryFile(ctx, artifacts["result/"+node.BuildTarget+"/"+instruction.Component].Path, raw, 0o644)
}
