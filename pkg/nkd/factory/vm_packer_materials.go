package factory

import (
	"context"
	"errors"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

const (
	maxVMPackerMaterialBytes            = pipeline.MaxVMImagePackerInputBytes
	maxVMPackerMaterialTotalBytes       = pipeline.MaxVMImagePackerInputBytes
	maxVMPackerMaterials                = 256
	maxVMPackerManifestBytes      int64 = 64 << 20
)

type vmPackerMaterialLimits struct {
	files    int
	perFile  int64
	total    int64
	manifest int64
}

var productionVMPackerMaterialLimits = vmPackerMaterialLimits{
	files: maxVMPackerMaterials, perFile: maxVMPackerMaterialBytes,
	total: maxVMPackerMaterialTotalBytes, manifest: maxVMPackerManifestBytes,
}

// vmPackerMaterials is issued only from an exact, validated Plan node. It owns
// immutable snapshots of every selected producer payload and of the canonical
// manifest passed to the source-owned Packer project.
type vmPackerMaterials struct {
	manifest *heldfile.File
	inputs   []pipeline.PackerMaterialInput
}

func (materials *vmPackerMaterials) Close() error {
	if materials == nil {
		return nil
	}
	errs := make([]error, 0, len(materials.inputs)+1)
	if materials.manifest != nil {
		errs = append(errs, materials.manifest.Close())
		materials.manifest = nil
	}
	for index := range materials.inputs {
		if materials.inputs[index].Content != nil {
			errs = append(errs, materials.inputs[index].Content.Close())
			materials.inputs[index].Content = nil
		}
	}
	materials.inputs = nil
	return errors.Join(errs...)
}

func observeVMPackerMaterials(
	ctx context.Context,
	plan *Plan,
	factoryNode Node,
) (result *vmPackerMaterials, returnErr error) {
	return observeVMPackerMaterialsWithLimits(ctx, plan, factoryNode, productionVMPackerMaterialLimits)
}

func observeVMPackerMaterialsWithLimits(
	ctx context.Context,
	plan *Plan,
	factoryNode Node,
	limits vmPackerMaterialLimits,
) (result *vmPackerMaterials, returnErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "observing VM Packer materials requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "observing VM Packer materials")
	}
	if plan == nil {
		return nil, e.New(e.CodeUsage, "observing VM Packer materials requires a Plan")
	}
	build, instruction, err := plan.producerInstruction(factoryNode)
	if err != nil {
		return nil, err
	}
	if instruction.BuilderType != ir.BuilderVMImage || instruction.Packer == nil {
		return nil, e.New(e.CodeEngineCapability,
			"VM producer %q is not a direct Packer build", instruction.ID)
	}
	if limits.files <= 0 || limits.perFile <= 0 || limits.total <= 0 || limits.manifest <= 0 {
		return nil, e.New(e.CodeInternal, "VM Packer material limits are incomplete")
	}
	if len(instruction.Materials) > limits.files {
		return nil, e.New(e.CodeEngineCapability,
			"VM producer %q exceeds the %d-material bound", instruction.ID, limits.files)
	}
	architecture := strings.TrimPrefix(instruction.Platform, "linux/")
	if architecture != "amd64" && architecture != "arm64" {
		return nil, e.New(e.CodeEngineCapability,
			"VM producer %q has unsupported target architecture %q", instruction.ID, architecture)
	}

	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		if _, duplicate := artifacts[artifact.ID]; duplicate {
			return nil, e.New(e.CodePipelineDrift,
				"VM Packer Plan contains duplicate artifact %q", artifact.ID)
		}
		artifacts[artifact.ID] = artifact
	}
	inputs := make(map[string]bool, len(factoryNode.Inputs))
	for _, input := range factoryNode.Inputs {
		inputs[input] = true
	}
	producers := make(map[string]ir.Node, len(build.Graph.Nodes))
	for _, producer := range build.Graph.Nodes {
		if _, duplicate := producers[producer.ID]; duplicate {
			return nil, e.New(e.CodePipelineDrift,
				"VM Packer build contains duplicate producer %q", producer.ID)
		}
		producers[producer.ID] = producer
	}

	result = &vmPackerMaterials{inputs: make([]pipeline.PackerMaterialInput, 0, len(instruction.Materials))}
	owned := result
	defer func() {
		if returnErr != nil {
			returnErr = joinFactoryCleanupError(returnErr, owned.Close(), "closing rejected VM Packer materials")
			result = nil
		}
	}()
	observations := make([]material.Observation, 0, len(instruction.Materials))
	var totalBytes int64
	for _, binding := range instruction.Materials {
		producer, ok := producers[binding.Producer]
		if !ok || producer.Component != binding.Component || producer.Platform != instruction.Platform ||
			(producer.Kind != ir.KindBuild && producer.Kind != ir.KindPull) {
			return nil, e.New(e.CodePipelineDrift,
				"VM material %q has no exact producer %q", binding.Binding, binding.Producer)
		}
		payloadID := "artifact/" + factoryNode.BuildTarget + "/" + binding.Component
		resultID := "result/" + factoryNode.BuildTarget + "/" + binding.Component
		payload, payloadOK := artifacts[payloadID]
		record, recordOK := artifacts[resultID]
		producerID := factoryNode.BuildTarget + "/" + producer.ID
		if !payloadOK || !recordOK || !inputs[payloadID] || !inputs[resultID] ||
			payload.Producer != producerID || record.Producer != producerID ||
			payload.BuildTarget != factoryNode.BuildTarget || record.BuildTarget != factoryNode.BuildTarget ||
			payload.Component != binding.Component || record.Component != binding.Component ||
			payload.Kind != producer.OutputType || record.Kind != "producer-result" {
			return nil, e.New(e.CodePipelineDrift,
				"VM material %q is not bound to its exact planned payload and result", binding.Binding)
		}
		observed, err := pipeline.ObserveResult(ctx, producer, payload.Path, record.Path)
		if err != nil {
			return nil, err
		}
		if observed.AbsPath == "" || observed.TransportPath != "" {
			return nil, e.New(e.CodeEngineCapability,
				"VM material %q is not one regular-file producer output", binding.Binding)
		}
		held, err := heldfile.OpenContext(ctx, observed.AbsPath, heldfile.Options{
			Label: "vm-packer-material-" + binding.Binding, MaxBytes: limits.perFile,
			ExpectedDigest: observed.SHA256,
		})
		if err != nil {
			return nil, e.Wrap(e.CodeUnverifiedRef, err,
				"holding VM Packer material %q", binding.Binding)
		}
		if held.Size() > limits.total-totalBytes {
			_ = held.Close()
			return nil, e.New(e.CodeEngineCapability,
				"VM Packer material closure exceeds the %d-byte bound", limits.total)
		}
		totalBytes += held.Size()
		path, err := material.StagingPath(binding.Binding)
		if err != nil {
			_ = held.Close()
			return nil, err
		}
		result.inputs = append(result.inputs, pipeline.PackerMaterialInput{
			Path: path, SHA256: held.Digest(), Content: held,
		})
		observations = append(observations, material.Observation{
			Binding: binding.Binding, Component: binding.Component, Kind: producer.OutputType,
			Architecture: architecture, Digest: held.Digest(),
		})
	}
	raw, err := material.MarshalProjected(observations, instruction.Packer.MaterialManifest)
	if err != nil {
		return nil, err
	}
	manifestDigest := pipeline.FileDigest(raw)
	if !canonical.IsDigest(manifestDigest) {
		return nil, e.New(e.CodeInternal, "VM Packer manifest has no exact byte digest")
	}
	result.manifest, err = heldfile.FromBytesContext(ctx, raw, heldfile.Options{
		Label: "vm-packer-material-manifest", MaxBytes: limits.manifest,
		ExpectedDigest: manifestDigest,
	})
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "holding VM Packer material manifest")
	}
	return result, nil
}
