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
	maxProducerMaterialBytes            = pipeline.MaxBuildMaterialBytes
	maxProducerMaterialTotalBytes       = pipeline.MaxBuildMaterialBytes
	maxProducerMaterials                = 256
	maxProducerManifestBytes      int64 = 64 << 20
)

type producerMaterialLimits struct {
	files    int
	perFile  int64
	total    int64
	manifest int64
}

var productionProducerMaterialLimits = producerMaterialLimits{
	files: maxProducerMaterials, perFile: maxProducerMaterialBytes,
	total: maxProducerMaterialTotalBytes, manifest: maxProducerManifestBytes,
}

// observedProducerMaterials is issued only from an exact, validated Plan node.
// It owns immutable snapshots of every selected producer payload and the
// canonical manifest passed to the selected consumer.
type observedProducerMaterials struct {
	manifest *heldfile.File
	inputs   []pipeline.BuildMaterialInput
}

func (materials *observedProducerMaterials) Close() error {
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

// vmPackerMaterials is the thin direct-Packer view over the generic observed
// producer-material capability. Its only additional behavior is selecting the
// producer-owned manifest projection during observation.
type vmPackerMaterials struct {
	*observedProducerMaterials
}

func observeVMPackerMaterials(
	ctx context.Context,
	plan *Plan,
	factoryNode Node,
) (result *vmPackerMaterials, returnErr error) {
	return observeVMPackerMaterialsWithLimits(ctx, plan, factoryNode, productionProducerMaterialLimits)
}

func observeVMPackerMaterialsWithLimits(
	ctx context.Context,
	plan *Plan,
	factoryNode Node,
	limits producerMaterialLimits,
) (result *vmPackerMaterials, returnErr error) {
	observed, err := observeProducerMaterialsWithLimits(ctx, plan, factoryNode, limits, true)
	if err != nil {
		return nil, err
	}
	return &vmPackerMaterials{observedProducerMaterials: observed}, nil
}

func observeBuildMaterials(
	ctx context.Context,
	plan *Plan,
	factoryNode Node,
) (result *observedProducerMaterials, returnErr error) {
	return observeProducerMaterialsWithLimits(ctx, plan, factoryNode, productionProducerMaterialLimits, false)
}

func observeProducerMaterialsWithLimits(
	ctx context.Context,
	plan *Plan,
	factoryNode Node,
	limits producerMaterialLimits,
	requirePacker bool,
) (result *observedProducerMaterials, returnErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "observing producer materials requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "observing producer materials")
	}
	if plan == nil {
		return nil, e.New(e.CodeUsage, "observing producer materials requires a Plan")
	}
	build, instruction, err := plan.producerInstruction(factoryNode)
	if err != nil {
		return nil, err
	}
	var projection *material.Projection
	if requirePacker {
		if instruction.BuilderType != ir.BuilderVMImage || instruction.Packer == nil {
			return nil, e.New(e.CodeEngineCapability,
				"VM producer %q is not a direct Packer build", instruction.ID)
		}
		projection = instruction.Packer.MaterialManifest
	} else if instruction.BuilderType != ir.BuilderOCIHermetic || instruction.Packer != nil || len(instruction.Materials) == 0 {
		return nil, e.New(e.CodeEngineCapability,
			"producer %q is not an OCI-hermetic material consumer", instruction.ID)
	}
	if limits.files <= 0 || limits.perFile <= 0 || limits.total <= 0 || limits.manifest <= 0 {
		return nil, e.New(e.CodeInternal, "producer material limits are incomplete")
	}
	if len(instruction.Materials) > limits.files {
		return nil, e.New(e.CodeEngineCapability,
			"producer %q exceeds the %d-material bound", instruction.ID, limits.files)
	}
	architecture := strings.TrimPrefix(instruction.Platform, "linux/")
	if architecture != "amd64" && architecture != "arm64" {
		return nil, e.New(e.CodeEngineCapability,
			"producer %q has unsupported target architecture %q", instruction.ID, architecture)
	}

	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		if _, duplicate := artifacts[artifact.ID]; duplicate {
			return nil, e.New(e.CodePipelineDrift,
				"producer material Plan contains duplicate artifact %q", artifact.ID)
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
				"producer material build contains duplicate producer %q", producer.ID)
		}
		producers[producer.ID] = producer
	}

	result = &observedProducerMaterials{inputs: make([]pipeline.BuildMaterialInput, 0, len(instruction.Materials))}
	owned := result
	defer func() {
		if returnErr != nil {
			returnErr = joinFactoryCleanupError(returnErr, owned.Close(), "closing rejected producer materials")
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
				"material %q has no exact producer %q", binding.Binding, binding.Producer)
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
				"material %q is not bound to its exact planned payload and result", binding.Binding)
		}
		observed, err := pipeline.AuthenticateResultForConsumption(ctx, producer, payload.Path, record.Path)
		if err != nil {
			return nil, err
		}
		if observed.AbsPath == "" || observed.TransportPath != "" {
			return nil, e.New(e.CodeEngineCapability,
				"material %q is not one regular-file producer output", binding.Binding)
		}
		held, err := heldfile.OpenContext(ctx, observed.AbsPath, heldfile.Options{
			Label: "producer-material-" + binding.Binding, MaxBytes: limits.perFile,
			ExpectedDigest: observed.SHA256,
		})
		if err != nil {
			return nil, e.Wrap(e.CodeUnverifiedRef, err,
				"holding producer material %q", binding.Binding)
		}
		if held.Size() > limits.total-totalBytes {
			_ = held.Close()
			return nil, e.New(e.CodeEngineCapability,
				"producer material closure exceeds the %d-byte bound", limits.total)
		}
		totalBytes += held.Size()
		path, err := material.StagingPath(binding.Binding)
		if err != nil {
			_ = held.Close()
			return nil, err
		}
		result.inputs = append(result.inputs, pipeline.BuildMaterialInput{
			Path: path, SHA256: held.Digest(), Content: held,
		})
		observations = append(observations, material.Observation{
			Binding: binding.Binding, Component: binding.Component, Kind: producer.OutputType,
			Architecture: architecture, Digest: held.Digest(),
		})
	}
	raw, err := material.MarshalProjected(observations, projection)
	if err != nil {
		return nil, err
	}
	manifestDigest := pipeline.FileDigest(raw)
	if !canonical.IsDigest(manifestDigest) {
		return nil, e.New(e.CodeInternal, "producer material manifest has no exact byte digest")
	}
	result.manifest, err = heldfile.FromBytesContext(ctx, raw, heldfile.Options{
		Label: "producer-material-manifest", MaxBytes: limits.manifest,
		ExpectedDigest: manifestDigest,
	})
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "holding producer material manifest")
	}
	return result, nil
}
