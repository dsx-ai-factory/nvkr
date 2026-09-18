package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type lifecycleContent struct {
	Artifacts []lifecycleContentArtifact `json:"artifacts"`
}

type lifecycleContentArtifact struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Digest      string   `json:"digest"`
	BuildDigest string   `json:"buildDigest"`
	Components  []string `json:"components,omitempty"`
}

// LifecyclePayloadObservation is an independently reopened and reproduced
// lifecycle package. It is descriptive only; callers still bind it to a Run
// or signed publication through the owning lifecycle module.
type LifecyclePayloadObservation struct {
	Digest     string
	Components []string
}

// ObserveLifecyclePayload verifies one compiled lifecycle output against its
// exact package inputs and producer results. Artifact paths must name the bytes
// in the execution workspace being observed.
func ObserveLifecyclePayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifacts map[string]Artifact) (LifecyclePayloadObservation, error) {
	artifact, ok := artifacts[expected.ArtifactID]
	if !ok || artifact.Kind != lifecyclePackageKind(expected.Kind) {
		return LifecyclePayloadObservation{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no typed payload", expected.Name)
	}
	node := planNode(plan, artifact.Producer)
	if node.ID == "" {
		return LifecyclePayloadObservation{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no package producer", expected.Name)
	}
	components := append([]string(nil), expected.Components...)
	buildName := ""
	for _, build := range plan.Builds {
		if build.Name == expected.Name && build.BuildDigest == expected.BuildDigest {
			if buildName != "" {
				return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle output %q has an ambiguous build identity", expected.Name)
			}
			buildName = build.Name
		}
	}
	if buildName == "" {
		return LifecyclePayloadObservation{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no exact build", expected.Name)
	}
	verifyRawResults := func() error {
		seen := map[string]bool{}
		for _, inputID := range node.Inputs {
			input := artifacts[inputID]
			if input.BuildTarget == "" || input.Component == "" || input.Kind == "producer-result" {
				continue
			}
			if input.BuildTarget != buildName {
				return e.New(e.CodeVerifyFailed, "lifecycle output %q carries foreign producer %q", expected.Name, input.BuildTarget)
			}
			if seen[input.Component] {
				return e.New(e.CodeVerifyFailed, "lifecycle output %q has ambiguous producer bytes for %q", expected.Name, input.Component)
			}
			seen[input.Component] = true
			digest, err := digestFactoryPath(ctx, input.Path)
			info, statErr := os.Stat(input.Path)
			if err != nil || statErr != nil || !info.Mode().IsRegular() {
				return e.New(e.CodeVerifyFailed, "lifecycle output %q has no exact producer bytes for %q", expected.Name, input.Component)
			}
			matches := 0
			for _, candidate := range artifacts {
				if candidate.Kind != "producer-result" || candidate.BuildTarget != input.BuildTarget || candidate.Component != input.Component {
					continue
				}
				result, readErr := readProducerResult(ctx, candidate.Path)
				if readErr != nil {
					return readErr
				}
				if result.Component != input.Component || result.SHA256 != digest || result.Size != info.Size() {
					return e.New(e.CodeVerifyFailed, "lifecycle output %q component %q differs from its producer result", expected.Name, input.Component)
				}
				matches++
			}
			if matches != 1 {
				return e.New(e.CodeReleaseIncomplete, "lifecycle output %q component %q has no unique producer result", expected.Name, input.Component)
			}
		}
		if len(seen) != len(expected.Components) {
			return e.New(e.CodeVerifyFailed, "lifecycle output %q producer membership differs from its plan binding", expected.Name)
		}
		for _, component := range expected.Components {
			if !seen[component] {
				return e.New(e.CodeVerifyFailed, "lifecycle output %q omits producer bytes for %q", expected.Name, component)
			}
		}
		return nil
	}
	switch artifact.Kind {
	case PackageImage:
		if err := verifyRawResults(); err != nil {
			return LifecyclePayloadObservation{}, err
		}
		image, err := inspectFactoryImage(ctx, plan, artifact)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		expectedDigest, err := reproduceLifecyclePackage(ctx, plan, artifact, artifacts)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		if expectedDigest != image.ManifestDigest {
			return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "image output %q differs from its observed producer archive", expected.Name)
		}
		return LifecyclePayloadObservation{Digest: image.ManifestDigest, Components: components}, nil
	case PackageBinaryBundle:
		bundle, _, err := delivery.InspectBinaryBundle(ctx, artifact.Path)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		components = components[:0]
		for _, entry := range bundle.Entries {
			components = append(components, entry.Name)
			matches := 0
			for _, candidate := range artifacts {
				if candidate.Kind != "producer-result" || candidate.BuildTarget != buildName || candidate.Component != entry.Name {
					continue
				}
				resultRecord, readErr := readProducerResult(ctx, candidate.Path)
				if readErr != nil {
					return LifecyclePayloadObservation{}, readErr
				}
				if resultRecord.Component == entry.Name && resultRecord.SHA256 == entry.SHA256 && resultRecord.Size == entry.Size {
					matches++
				}
			}
			if matches != 1 {
				return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle output %q component %q has no exact producer result", expected.Name, entry.Name)
			}
		}
		if !equalFactoryStrings(components, expected.Components) {
			return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle output %q members differ from its plan binding", expected.Name)
		}
		descriptor, err := delivery.PayloadDescriptor(ctx, artifact.Path)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		return LifecyclePayloadObservation{Digest: descriptor.Digest, Components: components}, nil
	case PackageAPT:
		repository, err := delivery.InspectAPTRepository(ctx, artifact.Path)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		if err := verifyRawResults(); err != nil {
			return LifecyclePayloadObservation{}, err
		}
		expectedDigest, err := reproduceLifecyclePackage(ctx, plan, artifact, artifacts)
		if err != nil || expectedDigest != repository.Digest {
			return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle APT output %q differs from independently reproduced package bytes", expected.Name)
		}
		return LifecyclePayloadObservation{Digest: repository.Digest, Components: components}, nil
	case PackageHelm:
		if len(expected.Components) != 0 {
			if err := verifyRawResults(); err != nil {
				return LifecyclePayloadObservation{}, err
			}
		}
		descriptor, err := delivery.PayloadDescriptor(ctx, artifact.Path)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		expectedDigest, err := reproduceLifecyclePackage(ctx, plan, artifact, artifacts)
		if err != nil || expectedDigest != descriptor.Digest {
			return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle Helm output %q differs from independently reproduced package (%s != %s)", expected.Name, descriptor.Digest, expectedDigest)
		}
		return LifecyclePayloadObservation{Digest: descriptor.Digest, Components: components}, nil
	default:
		return LifecyclePayloadObservation{}, e.New(e.CodeEngineCapability, "package kind %q cannot be observed for lifecycle output", artifact.Kind)
	}
}

func lifecyclePublicationBinding(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) (LifecycleBinding, error) {
	declared := make(map[string]bool, len(node.Inputs))
	for _, input := range node.Inputs {
		declared[input] = true
	}
	binding := LifecycleBinding{SchemaVersion: LifecycleBindingSchema, DefinitionDigest: plan.DefinitionDigest,
		EnvironmentDigest: plan.EnvironmentDigest}
	for _, expected := range plan.LifecycleOutputs {
		artifact, ok := artifacts[expected.ArtifactID]
		if !ok || artifact.Kind != lifecyclePackageKind(expected.Kind) {
			return LifecycleBinding{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no typed payload", expected.Name)
		}
		visible := make(map[string]Artifact, len(declared)+1)
		visible[artifact.ID] = artifact
		for id := range declared {
			visible[id] = artifacts[id]
		}
		observation, err := ObserveLifecyclePayload(ctx, plan, expected, visible)
		if err != nil {
			return LifecycleBinding{}, err
		}
		components := observation.Components
		payloadDigest := observation.Digest
		if !equalFactoryStrings(components, expected.Components) || payloadDigest == "" || expected.BuildDigest == "" {
			return LifecycleBinding{}, e.New(e.CodeVerifyFailed, "lifecycle output %q payload differs from its exact plan binding", expected.Name)
		}
		bound := LifecycleBoundOutput{Name: expected.Name, Kind: expected.Kind,
			Digest: payloadDigest, BuildDigest: expected.BuildDigest, Components: append([]string(nil), components...)}
		if artifact.Kind == PackageAPT {
			bound.TransportPath = artifact.Path
		}
		binding.Outputs = append(binding.Outputs, bound)
	}
	sort.Slice(binding.Outputs, func(i, j int) bool { return binding.Outputs[i].Name < binding.Outputs[j].Name })
	if err := ValidateLifecycleOutputs(plan.LifecycleOutputs, binding.Outputs); err != nil {
		return LifecycleBinding{}, err
	}
	content := lifecycleContent{}
	for _, output := range binding.Outputs {
		content.Artifacts = append(content.Artifacts, lifecycleContentArtifact{Name: output.Name,
			Kind: output.Kind, Digest: output.Digest, BuildDigest: output.BuildDigest, Components: append([]string(nil), output.Components...)})
	}
	contentSubject, err := canonical.Digest(canonical.KindReleaseContent, content)
	if err != nil {
		return LifecycleBinding{}, err
	}
	binding.ContentSubject = contentSubject
	return binding, nil
}

func reproduceLifecyclePackage(ctx context.Context, plan *Plan, artifact Artifact, artifacts map[string]Artifact) (string, error) {
	stage, err := os.MkdirTemp(filepath.Dir(artifact.Path), ".lifecycle-reproduce-*")
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "creating lifecycle package reproduction stage")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	clone := make(map[string]Artifact, len(artifacts))
	for id, value := range artifacts {
		clone[id] = value
	}
	reproduced := artifact
	reproduced.Path = filepath.Join(stage, "output")
	clone[artifact.ID] = reproduced
	node := planNode(plan, artifact.Producer)
	if err := executePackage(ctx, plan, node, clone, io.Discard); err != nil {
		return "", err
	}
	switch artifact.Kind {
	case PackageImage:
		image, err := inspectFactoryImage(ctx, plan, reproduced)
		if err != nil {
			return "", err
		}
		return image.ManifestDigest, nil
	case PackageAPT:
		result, err := delivery.InspectAPTRepository(ctx, reproduced.Path)
		if err != nil {
			return "", err
		}
		return result.Digest, nil
	case PackageHelm:
		result, err := delivery.PayloadDescriptor(ctx, reproduced.Path)
		if err != nil {
			return "", err
		}
		return result.Digest, nil
	default:
		return "", e.New(e.CodeEngineCapability, "package kind %q cannot be reproduced for lifecycle binding", artifact.Kind)
	}
}

func planNode(plan *Plan, id string) Node {
	for _, node := range plan.Graph.Nodes {
		if node.ID == id {
			return node
		}
	}
	return Node{}
}

// ValidateLifecycleOutputs owns the exact join from compiled output requirements
// to observed public payloads, including each output name and build identity.
func ValidateLifecycleOutputs(expected []LifecycleOutputPlan, observed []LifecycleBoundOutput) error {
	outputs := make(map[string]LifecycleBoundOutput, len(observed))
	for _, output := range observed {
		if _, exists := outputs[output.Name]; exists || lifecyclePackageKind(output.Kind) == "" || !canonical.IsDigest(output.Digest) ||
			!canonical.IsDigest(output.BuildDigest) || (output.Kind != "helm-oci" && len(output.Components) == 0) {
			return e.New(e.CodeVerifyFailed, "lifecycle outputs are not unique exact payload bindings")
		}
		outputs[output.Name] = output
	}
	if len(outputs) != len(expected) {
		return e.New(e.CodeReleaseIncomplete, "lifecycle output observation count differs from its plan")
	}
	for _, want := range expected {
		got, ok := outputs[want.Name]
		if !ok || got.Kind != want.Kind || got.BuildDigest != want.BuildDigest || !equalFactoryStrings(got.Components, want.Components) {
			return e.New(e.CodeVerifyFailed, "lifecycle output %q changes its build or component binding", want.Name)
		}
	}
	return nil
}

func equalFactoryStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
