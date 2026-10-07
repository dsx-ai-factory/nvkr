package delivery

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const maxMetadataBytes int64 = 512 << 20

// InspectMetadata validates the bounded control documents and evidence, not
// output payloads. It deliberately does not return a Publication or payload
// capability. Callers must authenticate the publisher before trusting its claims.
func InspectMetadata(ctx context.Context, root string) (Inspection, error) {
	return inspectMetadata(ctx, root, false)
}

// allowPayloads is used only for browsing an existing full layout. It permits
// declared output blobs to be present, without opening or verifying their bytes.
func inspectMetadata(ctx context.Context, root string, allowPayloads bool) (Inspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return Inspection{}, err
	}
	index, raw, err := readIndex(ctx, root)
	if err != nil {
		return Inspection{}, err
	}
	if index.ArtifactType != MediaPublicationRoot || len(index.Manifests) != 1 || index.Manifests[0].ArtifactType != MediaPublicationRoot || index.Manifests[0].MediaType != MediaOCIManifest {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata release requires one root manifest")
	}
	manifest, err := readPublicationManifest(ctx, root, index.Manifests[0])
	if err != nil {
		return Inspection{}, err
	}
	config, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return Inspection{}, err
	}
	var publication PublicationRoot
	if err := canonical.UnmarshalExact(config, &publication); err != nil {
		return Inspection{}, e.Wrap(e.CodeVerifyFailed, err, "decode release metadata")
	}
	if err := publication.Validate(); err != nil {
		return Inspection{}, err
	}
	if publication.Release == nil || publication.Qualification != QualificationDiagnostic {
		return Inspection{}, e.New(e.CodeEngineCapability, "metadata transport requires pipeline release metadata")
	}
	semantic, err := publication.Digest()
	if err != nil {
		return Inspection{}, err
	}
	if index.Annotations["dev.nkd.semantic.digest"] != semantic {
		return Inspection{}, e.New(e.CodeVerifyFailed, "release metadata semantic identity differs")
	}
	visited := map[string]bool{index.Manifests[0].Digest: true, manifest.Config.Digest: true}
	optionalPayloads := map[string]bool{}
	total := index.Manifests[0].Size + manifest.Config.Size
	for _, object := range publication.Objects {
		m, err := metadataManifest(ctx, root, object)
		if err != nil {
			return Inspection{}, err
		}
		if allowPayloads && object.Output != nil {
			for _, layer := range m.Layers {
				optionalPayloads[layer.Digest] = true
			}
		}
		for _, d := range []Descriptor{{Digest: object.DescriptorDigest, Size: object.Size}, m.Config} {
			if !visited[d.Digest] {
				total += d.Size
				visited[d.Digest] = true
			}
		}
		if object.Evidence != nil && len(m.Layers) == 1 && !visited[m.Layers[0].Digest] {
			total += m.Layers[0].Size
			visited[m.Layers[0].Digest] = true
		}
		if total < 0 || total > maxMetadataBytes {
			return Inspection{}, e.New(e.CodeVerifyFailed, "metadata exceeds total size bound")
		}
		if object.Output != nil && object.Output.Format == "oci" {
			if _, err := inspectImageMetadata(ctx, root, m); err != nil {
				return Inspection{}, err
			}
		} else if object.Output != nil && object.Output.Format == "disk-image" {
			if _, err := inspectDiskOutputMetadata(ctx, root, object, m); err != nil {
				return Inspection{}, err
			}
		} else if _, err := readBlob(ctx, root, m.Config, 8<<20); err != nil {
			return Inspection{}, err
		}
		if object.Evidence != nil {
			if _, err := metadataEvidence(ctx, root, object); err != nil {
				return Inspection{}, err
			}
		}
	}
	if allowPayloads {
		if err := verifyMetadataInventory(ctx, root, visited, optionalPayloads); err != nil {
			return Inspection{}, err
		}
	} else {
		if err := verifyBlobInventory(root, visited); err != nil {
			return Inspection{}, err
		}
	}
	if err := verifyReleaseEvidence(ctx, root, publication); err != nil {
		return Inspection{}, err
	}
	outputs, err := MetadataOutputs(publication)
	if err != nil {
		return Inspection{}, err
	}
	subject, err := ContentDigest(outputs)
	if err != nil || subject != publication.Release.ContentSubject {
		return Inspection{}, e.New(e.CodeVerifyFailed, "release metadata content subject differs")
	}
	return Inspection{Root: publication, RootDigest: digestBytes(raw), RootConfig: manifest.Config}, nil
}

// MetadataOutputs projects descriptions only. It never observes payload bytes.
func MetadataOutputs(root PublicationRoot) ([]Output, error) {
	if err := root.Validate(); err != nil {
		return nil, err
	}
	var outputs []Output
	for _, object := range root.Objects {
		if object.Output == nil {
			continue
		}
		b := object.Output
		o := Output{Name: object.Name, Kind: b.Format, Digest: object.DescriptorDigest, BuildDigest: b.BuildDigest, Components: append([]string(nil), b.Components...), Disk: cloneDiskOutput(b.Disk)}
		if b.Format == "apt-repository" {
			o.Digest = object.SemanticDigest
		}
		if err := o.Validate(); err != nil {
			return nil, err
		}
		outputs = append(outputs, o)
	}
	return outputs, nil
}

func metadataEvidence(ctx context.Context, root string, object PublishedObject) ([]byte, error) {
	m, err := flatManifest(ctx, root, object)
	if err != nil {
		return nil, err
	}
	if object.Evidence == nil || len(m.Layers) != 1 {
		return nil, e.New(e.CodeVerifyFailed, "metadata object is not evidence")
	}
	raw, err := readBlob(ctx, root, m.Layers[0], sbom.MaxBundleBytes)
	if err != nil {
		return nil, err
	}
	if digestBytes(raw) != object.BlobDigest {
		return nil, e.New(e.CodeVerifyFailed, "evidence blob differs")
	}
	return raw, nil
}

// VisitMetadataEvidence visits each exact subject/bundle after validating the
// complete control-document closure once. The visitor owns cryptographic policy.
func VisitMetadataEvidence(ctx context.Context, root, expectedRootDigest string, visit func(context.Context, string, []byte) error) error {
	if visit == nil || !canonical.IsDigest(expectedRootDigest) {
		return e.New(e.CodeUsage, "metadata evidence requires a visitor")
	}
	inspected, err := InspectMetadata(ctx, root)
	if err != nil {
		return err
	}
	if inspected.RootDigest != expectedRootDigest {
		return e.New(e.CodeVerifyFailed, "metadata evidence root changed")
	}
	for _, object := range inspected.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		raw, err := metadataEvidence(ctx, root, object)
		if err != nil {
			return err
		}
		if err := visit(ctx, object.Evidence.SubjectDigest, raw); err != nil {
			return err
		}
	}
	return nil
}

// BuildMetadataRoot writes descriptions and evidence from independently admitted
// source roots. It issues no trust receipt and never copies output layers.
func BuildMetadataRoot(ctx context.Context, out string, root PublicationRoot, sources map[string]string) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if err := root.Validate(); err != nil {
		return Result{}, err
	}
	if root.Release == nil || root.Qualification != QualificationDiagnostic {
		return Result{}, e.New(e.CodeEngineCapability, "metadata transport requires pipeline release metadata")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	for _, o := range root.Objects {
		source := sources[o.Name]
		d := Descriptor{MediaType: publishedManifestMediaType(o), Digest: o.DescriptorDigest, Size: o.Size, ArtifactType: o.ArtifactType}
		raw, err := readBlob(ctx, source, d, 8<<20)
		if err != nil {
			return Result{}, err
		}
		if _, err = writer.addBlobBytes(raw, d.MediaType); err != nil {
			return Result{}, err
		}
		m, err := flatManifest(ctx, source, o)
		if err != nil {
			return Result{}, err
		}
		config, err := readBlob(ctx, source, m.Config, 8<<20)
		if err != nil {
			return Result{}, err
		}
		if _, err = writer.addBlobBytes(config, m.Config.MediaType); err != nil {
			return Result{}, err
		}
		if o.Evidence != nil {
			raw, err := metadataEvidence(ctx, source, o)
			if err != nil {
				return Result{}, err
			}
			if _, err = writer.addBlobBytes(raw, m.Layers[0].MediaType); err != nil {
				return Result{}, err
			}
		}
	}
	digest, semantic, err := writer.writePublicationMetadata(root, nil)
	if err != nil {
		return Result{}, err
	}
	if _, err := InspectMetadata(ctx, stage); err != nil {
		return Result{}, err
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: digest, SemanticDigest: semantic}, nil
}

// ExportMetadata is called only after a full output-local publication check.
func ExportMetadata(ctx context.Context, source, out string) (Result, error) {
	inspection, err := Inspect(ctx, source)
	if err != nil {
		return Result{}, err
	}
	sources := map[string]string{}
	for _, o := range inspection.Root.Objects {
		sources[o.Name] = source
	}
	return BuildMetadataRoot(ctx, out, inspection.Root, sources)
}

func metadataManifest(ctx context.Context, root string, object PublishedObject) (ociManifest, error) {
	manifestBytes, err := readBlob(ctx, root, Descriptor{Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return ociManifest{}, err
	}
	if err := canonical.ValidateJSONDocument(manifestBytes); err != nil {
		return ociManifest{}, err
	}
	if types.MediaType(object.ArtifactType).IsImage() {
		if err := validateImageSchemaNames(manifestBytes, reflect.TypeFor[v1.Manifest]()); err != nil {
			return ociManifest{}, err
		}
	}
	var m ociManifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return ociManifest{}, e.Wrap(e.CodeVerifyFailed, err, "decoding metadata manifest")
	}
	if m.SchemaVersion != 2 || m.MediaType != publishedManifestMediaType(object) {
		return ociManifest{}, e.New(e.CodeVerifyFailed, "metadata member has an invalid manifest schema/media type")
	}
	if !types.MediaType(object.ArtifactType).IsImage() && m.ArtifactType != object.ArtifactType {
		return ociManifest{}, e.New(e.CodeVerifyFailed, "metadata member artifact type differs from its release binding")
	}
	return m, nil
}

// inspectDiskOutputMetadata binds the exact disk wrapper and config to the
// release's output description without opening the QCOW2 payload.
func inspectDiskOutputMetadata(ctx context.Context, root string, object PublishedObject, manifest ociManifest) (publicationPlatform, error) {
	if object.Output == nil || object.Output.Disk == nil {
		return publicationPlatform{}, e.New(e.CodeVerifyFailed, "disk metadata requires a release binding")
	}
	m, config, err := inspectDiskMetadata(ctx, root, Descriptor{MediaType: manifest.MediaType, Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}, object.OS+"/"+object.Arch, maxDeliveryFileBytes)
	if err != nil {
		return publicationPlatform{}, err
	}
	disk := object.Output.Disk
	if m.Layers[0].Digest.String() != disk.PayloadDigest || m.Layers[0].Size != disk.Size || m.Annotations[diskEncryptionAnnotation] != disk.Encryption {
		return publicationPlatform{}, e.New(e.CodeVerifyFailed, "disk metadata differs from its release binding")
	}
	return publicationPlatform{OS: config.OS, Arch: config.Architecture}, nil
}
