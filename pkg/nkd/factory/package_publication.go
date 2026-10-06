package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func executePublicationPackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact, artifacts map[string]Artifact) error {
	evidence, err := publicationEvidenceObjects(ctx, plan, node, inputs, artifacts)
	if err != nil {
		return err
	}
	objects, cleanup, err := collectPublicationObjects(ctx, plan, output, inputs, evidence)
	if err != nil {
		return err
	}
	defer cleanup()
	metadata, err := bindPublicationMetadata(ctx, plan, node, artifacts, objects)
	if err != nil {
		return err
	}
	_, err = delivery.BuildPublicationRoot(ctx, output.Path, delivery.PublicationRequest{Release: metadata, Objects: objects})
	return err
}

func publicationEvidenceObjects(ctx context.Context, plan *Plan, node Node, inputs []Artifact, artifacts map[string]Artifact) ([]delivery.ObjectInput, error) {
	objects := make([]delivery.ObjectInput, 0, len(inputs))
	if len(plan.LifecycleOutputs) > 0 {
		evidence, err := releaseEvidenceObjects(ctx, plan, node, artifacts)
		if err != nil {
			return nil, err
		}
		objects = append(objects, evidence...)
	} else {
		evidenceNames := map[string]bool{}
		for _, input := range inputs {
			if input.Kind != "sbom-set" {
				continue
			}
			evidence, err := sbomPublicationObjects(ctx, plan, input, artifacts, func(path string) (string, error) { return path, nil })
			if err != nil {
				return nil, err
			}
			for _, object := range evidence {
				if !evidenceNames[object.Name] {
					objects = append(objects, object)
					evidenceNames[object.Name] = true
				}
			}
		}
	}
	return objects, nil
}

// collectPublicationObjects owns any temporary carriers until its returned cleanup
// runs. The caller retains them through metadata binding and publication assembly.
func collectPublicationObjects(ctx context.Context, plan *Plan, output Artifact, inputs []Artifact, objects []delivery.ObjectInput) ([]delivery.ObjectInput, func(), error) {
	var temporaryArchives []string
	cleanup := func() {
		for _, path := range temporaryArchives {
			_ = os.Remove(path)
		}
	}
	transferred := false
	defer func() {
		if !transferred {
			cleanup()
		}
	}()
	for _, input := range inputs {
		if input.BuildTarget != "" {
			continue
		}
		object := delivery.ObjectInput{Name: input.Name, Kind: input.Kind, Path: input.Path,
			ArtifactType: input.MediaType, LayerMediaType: input.MediaType}
		switch input.Kind {
		case PackageDiskImage:
			var expected LifecycleOutputPlan
			for _, candidate := range plan.LifecycleOutputs {
				if candidate.Name == input.Name && candidate.Kind == PackageDiskImage {
					expected = candidate
				}
			}
			disk, err := inspectFactoryDisk(ctx, plan, expected, input)
			if err != nil {
				return nil, nil, err
			}
			object.Layout, object.SemanticDigest = true, disk.ManifestDigest
			// Layout import authenticates its index carrier, not the QCOW2 layer.
			// The checked wrapper separately binds the exact raw payload subject.
			object.BlobDigest, err = digestFactoryPath(ctx, filepath.Join(input.Path, "index.json"))
			if err != nil {
				return nil, nil, err
			}
			object.OS, object.Arch = "linux", strings.TrimPrefix(disk.Platform, "linux/")
		case PackageImage:
			image, err := inspectFactoryImage(ctx, plan, input)
			if err != nil {
				return nil, nil, err
			}
			object.Layout, object.SemanticDigest, object.BlobDigest = true, image.ManifestDigest, image.RootDigest
			object.ArtifactType = image.MediaType
			object.OS, object.Arch = image.OS, image.Arch
		case PackageBinaryBundle:
			_, inspected, err := delivery.InspectBinaryBundle(ctx, input.Path)
			if err != nil {
				return nil, nil, err
			}
			object.Layout, object.SemanticDigest, object.BlobDigest = true, inspected.SemanticDigest, inspected.RootDigest
		case PackageHelm:
			_, payload, err := delivery.ReadArtifact(ctx, input.Path)
			if err != nil {
				return nil, nil, err
			}
			object.Layout, object.SemanticDigest = true, pipeline.FileDigest(payload)
			object.BlobDigest, err = digestFactoryPath(ctx, filepath.Join(input.Path, "index.json"))
			if err != nil {
				return nil, nil, err
			}
		case PackageOCI:
			inspected, err := delivery.Inspect(ctx, input.Path)
			if err != nil {
				return nil, nil, err
			}
			semantic, err := inspected.Root.Digest()
			if err != nil {
				return nil, nil, err
			}
			object.Layout, object.SemanticDigest, object.BlobDigest = true, semantic, inspected.RootDigest
		case PackageAPT:
			repository, inspectErr := delivery.InspectAPTRepository(ctx, input.Path)
			if inspectErr != nil {
				return nil, nil, inspectErr
			}
			archive := filepath.Join(filepath.Dir(output.Path), "."+input.Name+".apt.tar")
			if err := removeFactoryCarrier(archive); err != nil {
				return nil, nil, err
			}
			if err := transportarchive.Pack(ctx, archive, ".", []string{input.Path}); err != nil {
				return nil, nil, err
			}
			temporaryArchives = append(temporaryArchives, archive)
			digest, err := digestFactoryPath(ctx, archive)
			if err != nil {
				return nil, nil, err
			}
			object.Path, object.LayerMediaType = archive, "application/vnd.nkd.apt-repository.v1.tar"
			object.SemanticDigest, object.BlobDigest = repository.Digest, digest
		default:
			semantic, err := digestFactoryPath(ctx, input.Path)
			if err != nil {
				return nil, nil, err
			}
			object.SemanticDigest = semantic
		}
		objects = append(objects, object)
	}
	transferred = true
	return objects, cleanup, nil
}

func bindPublicationMetadata(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, objects []delivery.ObjectInput) (*delivery.ReleaseMetadata, error) {
	var metadata *delivery.ReleaseMetadata
	if len(plan.LifecycleOutputs) > 0 {
		outputs, err := lifecyclePublicationOutputs(ctx, plan, node, artifacts)
		if err != nil {
			return nil, err
		}
		contentSubject, err := delivery.ContentDigest(outputs)
		if err != nil {
			return nil, err
		}
		metadata = &delivery.ReleaseMetadata{DefinitionDigest: plan.DefinitionDigest, EnvironmentDigest: plan.EnvironmentDigest, ContentSubject: contentSubject}
		if PerOutputPublication(plan) {
			digest, err := plan.IdentityDigest()
			if err != nil {
				return nil, err
			}
			run, err := factoryRunContext(digest, time.Now().UTC(), time.Now().UTC())
			if err != nil {
				return nil, err
			}
			metadata.PlanDigest, metadata.InvocationID = digest, run.InvocationID()
		}
		outputByName := make(map[string]delivery.Output, len(outputs))
		transportPaths := make(map[string]string, len(outputs))
		for _, output := range outputs {
			outputByName[output.Name] = output
		}
		for _, expected := range plan.LifecycleOutputs {
			transportPaths[expected.Name] = artifacts[expected.ArtifactID].Path
		}
		for i := range objects {
			output, found := outputByName[objects[i].Name]
			if !found {
				continue
			}
			subjects, err := publicationBuildSubjects(ctx, output, node.Inputs, artifacts)
			if err != nil {
				return nil, err
			}
			objects[i].Output = &delivery.OutputBinding{Repository: output.Name, Format: output.Kind,
				BuildDigest: output.BuildDigest, Components: output.Components, BuildSubjects: subjects, Disk: output.Disk}
			if output.Kind == PackageAPT {
				objects[i].Output.TransportPath = transportPaths[output.Name]
			}
		}
	}
	return metadata, nil
}

// The exact Plan-to-observation join is complete before this projection. Bind
// each published output to its own declared producer records, even for equal
// build identities; a sibling's record cannot fill a missing component slot.
func publicationBuildSubjects(ctx context.Context, output delivery.Output, inputs []string, artifacts map[string]Artifact) ([]delivery.BuildSubject, error) {
	wanted := make(map[string]bool, len(output.Components))
	for _, component := range output.Components {
		wanted[component] = true
	}
	observed := make(map[string]string, len(wanted))
	for _, id := range inputs {
		input := artifacts[id]
		if input.Kind != "producer-result" || input.BuildTarget != output.Name || !wanted[input.Component] {
			continue
		}
		if _, exists := observed[input.Component]; exists {
			return nil, e.New(e.CodeVerifyFailed, "publication output %q has duplicate producer records", output.Name)
		}
		result, err := readProducerResult(ctx, input.Path)
		if err != nil {
			return nil, err
		}
		if result.Component != input.Component || !canonical.IsDigest(result.SHA256) {
			return nil, e.New(e.CodeVerifyFailed, "publication producer record differs from its exact component")
		}
		observed[input.Component] = result.SHA256
	}
	subjects := make([]delivery.BuildSubject, 0, len(output.Components))
	for _, component := range output.Components {
		digest, found := observed[component]
		if !found {
			return nil, e.New(e.CodeReleaseIncomplete, "publication output %q omits producer evidence for %q", output.Name, component)
		}
		subjects = append(subjects, delivery.BuildSubject{Component: component, Digest: digest})
	}
	return subjects, nil
}
