package factory

import (
	"context"
	"io"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// localVMPackerResolver realizes the exact runtime, daemon and already-local
// toolchain-image closure selected by one validated, relocated Plan. Command
// and Make producers remain closed until they have their own held-output adapter.
type localVMPackerResolver struct{}

type localVMPackerExecutor struct {
	inner       *pipeline.VMImagePackerExecutor
	subjects    map[string]struct{}
	sourceRoots map[string]string
	stdout      io.Writer
	stderr      io.Writer
}

func (localVMPackerResolver) Admit(
	ctx context.Context,
	plan *Plan,
	session *pipeline.NodeSession,
	config vmDriverRunConfig,
) (vmDriverExecutor, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "local Packer resolver requires a context")
	}
	if plan == nil || session == nil {
		return nil, e.New(e.CodeEngineCapability,
			"local Packer resolver requires a validated Plan and producer session")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	_, artifacts := planIndex(plan)
	selections := make([]pipeline.VMImagePackerSelection, 0)
	subjects := make(map[string]struct{})
	sourceRoots := make(map[string]string)
	for _, selected := range topologicalFactoryNodes(plan.Graph) {
		if selected.Kind != NodeBuild || selected.IRNode == "" {
			continue
		}
		build, instruction, err := plan.producerInstruction(selected)
		if err != nil {
			return nil, err
		}
		if instruction.BuilderType != ir.BuilderVMImage {
			continue
		}
		if instruction.Packer == nil || len(instruction.Command) != 0 {
			return nil, e.New(e.CodeEngineCapability,
				"vm-image command/Make execution requires an unimplemented held-output adapter")
		}
		if instruction.Disk == nil || instruction.Disk.Encryption != "none" {
			return nil, e.New(e.CodeEngineCapability,
				"vm-image execution requires a typed non-public encryption secret channel")
		}
		request, err := localVMRequestForPlan(plan, selected.ID)
		if err != nil {
			return nil, err
		}
		result, ok := artifacts["result/"+selected.BuildTarget+"/"+instruction.Component]
		if !ok || result.Path == "" {
			return nil, e.New(e.CodePipelineDrift,
				"VM-image producer %q has no planned result route", selected.ID)
		}
		if _, duplicate := subjects[request.Subject]; duplicate {
			return nil, e.New(e.CodePipelineDrift,
				"VM-image producer subject %q is selected more than once", request.Subject)
		}
		subjects[request.Subject] = struct{}{}
		sourceRoot, err := plannedPackerSourceRoot(build, selected, instruction, artifacts)
		if err != nil {
			return nil, err
		}
		sourceRoots[request.Subject] = sourceRoot
		selections = append(selections, pipeline.VMImagePackerSelection{Subject: request.Subject, Node: instruction})
	}
	if len(selections) == 0 {
		return nil, e.New(e.CodeEngineCapability,
			"local Packer resolver found no direct-Packer VM-image producer")
	}
	networkAdmission, err := pipeline.AuthorizeVMImagePackerNetwork(
		plan.Environment, plan.EnvironmentDigest, selections,
	)
	if err != nil {
		return nil, err
	}
	admitted, err := session.AdmitVMImagePacker(ctx, selections, networkAdmission, config.Runtime)
	if err != nil {
		return nil, err
	}
	return &localVMPackerExecutor{
		inner: admitted, subjects: subjects, sourceRoots: sourceRoots,
		stdout: config.Stdout, stderr: config.Stderr,
	}, nil
}

// plannedPackerSourceRoot resolves the source route already authored into the
// validated Plan. It does not reconstruct that route from the result-directory
// layout, so changing an unrelated result path cannot redirect source access.
func plannedPackerSourceRoot(build BuildPlan, selected Node, instruction ir.Node, artifacts map[string]Artifact) (string, error) {
	if build.Graph == nil {
		return "", e.New(e.CodePipelineDrift, "VM-image producer %q has no build graph", selected.ID)
	}
	byID := make(map[string]ir.Node, len(build.Graph.Nodes))
	for _, node := range build.Graph.Nodes {
		byID[node.ID] = node
	}
	var sourceID string
	for _, dependency := range instruction.Needs {
		if byID[dependency].Kind != ir.KindFetch {
			continue
		}
		if sourceID != "" {
			return "", e.New(e.CodePipelineDrift,
				"VM-image producer %q has more than one planned source route", selected.ID)
		}
		sourceID = "source/" + selected.BuildTarget + "/" + dependency
	}
	if sourceID == "" {
		return "", e.New(e.CodePipelineDrift,
			"VM-image producer %q has no planned source route", selected.ID)
	}
	source, ok := artifacts[sourceID]
	suffix := "/" + instruction.SourceDest
	if !ok || source.Path == "" || !strings.HasSuffix(source.Path, suffix) {
		return "", e.New(e.CodePipelineDrift,
			"VM-image producer %q source route does not bind destination %q", selected.ID, instruction.SourceDest)
	}
	root := strings.TrimSuffix(source.Path, suffix)
	if root == "" {
		return "", e.New(e.CodePipelineDrift,
			"VM-image producer %q has an empty planned source root", selected.ID)
	}
	return root, nil
}

func (executor *localVMPackerExecutor) RunPacker(
	ctx context.Context,
	runtime *VMRuntime,
	subject string,
	node ir.Node,
	materials *vmPackerMaterials,
) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
	if executor == nil || executor.inner == nil || runtime == nil || materials == nil ||
		materials.manifest == nil {
		return nil, nil, e.New(e.CodeEngineCapability,
			"local Packer execution requires admitted runtime and material capabilities")
	}
	if _, ok := executor.subjects[subject]; !ok || subject == "" {
		return nil, nil, e.New(e.CodePipelineDrift,
			"local Packer subject %q was not admitted", subject)
	}
	sourceRoot, ok := executor.sourceRoots[subject]
	if !ok || sourceRoot == "" {
		return nil, nil, e.New(e.CodePipelineDrift,
			"local Packer subject %q has no admitted source route", subject)
	}
	return executor.inner.Run(ctx, subject, node, pipeline.VMImagePackerRun{
		SourceRoot: sourceRoot,
		Manifest:   materials.manifest,
		Materials:  materials.inputs,
		Runtime:    runtime.inner,
		Stdout:     executor.stdout,
		Stderr:     executor.stderr,
	})
}

func (executor *localVMPackerExecutor) Close() error {
	if executor == nil || executor.inner == nil {
		return nil
	}
	return executor.inner.Close()
}
