// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
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
	return inspectMetadataProgress(ctx, root, allowPayloads, "", nil)
}

func inspectMetadataProgress(ctx context.Context, root string, allowPayloads bool, expectedRoot string, progress func(MetadataProgress)) (Inspection, error) {
	ctx = withMetadataReads(ctx)
	if err := deliveryContext(ctx); err != nil {
		return Inspection{}, err
	}
	index, raw, err := readIndex(ctx, root)
	if err != nil {
		return Inspection{}, err
	}
	if expectedRoot != "" && digestBytes(raw) != expectedRoot {
		return Inspection{}, e.New(e.CodeVerifyFailed, "cached metadata root identity differs")
	}
	if index.artifactType() != MediaPublicationRoot || len(index.Manifests) != 1 || index.Manifests[0].ArtifactType != MediaPublicationRoot || index.Manifests[0].MediaType != MediaOCIManifest {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata release requires one root manifest")
	}
	representation, err := inspectOCIRepresentation(index)
	if err != nil {
		return Inspection{}, err
	}
	manifest, err := readPublicationManifest(ctx, root, index.Manifests[0])
	if err != nil {
		return Inspection{}, err
	}
	if err := validateRootRepresentation(representation, manifest); err != nil {
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
	publication.ociRepresentation = representation
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
	if index.Annotations["dev.nvkr.semantic.digest"] != semantic {
		return Inspection{}, e.New(e.CodeVerifyFailed, "release metadata semantic identity differs")
	}
	visited := map[string]bool{index.Manifests[0].Digest: true, manifest.Config.Digest: true}
	optionalPayloads := map[string]bool{}
	total := index.Manifests[0].Size + manifest.Config.Size
	done := 0
	report := func(object *PublishedObject) {
		if object != nil {
			done++
		}
		if progress != nil {
			progress(metadataProgressUpdate(digestBytes(raw), done, len(publication.Objects), object))
		}
	}
	report(nil)
	for _, object := range publication.Objects {
		if object.Output != nil && object.Output.ImageIndex != nil {
			controls, payloads, err := imageIndexMetadata(ctx, root, object)
			if err != nil {
				return Inspection{}, err
			}
			for _, d := range controls {
				if !visited[d.Digest] {
					total += d.Size
					visited[d.Digest] = true
				}
			}
			if total < 0 || total > maxMetadataBytes {
				return Inspection{}, e.New(e.CodeVerifyFailed, "metadata exceeds total size bound")
			}
			if allowPayloads {
				for _, d := range payloads {
					optionalPayloads[d.Digest] = true
				}
			}
			report(&object)
			continue
		}
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
		} else if object.Output != nil && object.Output.Format == outputFormatBundle {
			if err := inspectBundleMetadata(ctx, root, m, &object); err != nil {
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
		report(&object)
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
	sources := map[string]PublicationSource{}
	if err := verifyReleaseEvidenceSources(ctx, root, publication, sources); err != nil {
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
	return Inspection{Root: publication, RootDigest: digestBytes(raw), RootConfig: manifest.Config, sources: sources}, nil
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
		o := Output{Name: object.Name, Kind: b.Format, Digest: object.DescriptorDigest, BuildDigest: b.BuildDigest, Components: append([]string(nil), b.Components...), Disk: cloneDiskOutput(b.Disk), ImageIndex: b.ImageIndex.Clone()}
		o.OS, o.Arch = object.OS, object.Arch
		if b.Format == "apt-repository" || b.Format == outputFormatDebian {
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
// complete control-document closure and requiring attested evidence. Raw
// preparation documents cannot reach the visitor, which owns cryptographic policy.
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
	if err := RequireAttestedEvidence(inspected); err != nil {
		return err
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
	return buildMetadataRoot(ctx, out, root, sources, "", "")
}

// BuildMetadataRootWithRepresentation binds an explicit construction choice to
// a new aggregate bootstrap, without converting any identified member.
func BuildMetadataRootWithRepresentation(ctx context.Context, out string, root PublicationRoot, sources map[string]string, representation OCIRepresentation) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if err := admitOCIRepresentation(representation); err != nil {
		return Result{}, err
	}
	root.ociRepresentation = representation
	return buildMetadataRoot(ctx, out, root, sources, "", "")
}

func buildMetadataRoot(ctx context.Context, out string, root PublicationRoot, sources map[string]string, expectedRoot, bootstrapSource string) (Result, error) {
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
		if o.Output != nil && o.Output.ImageIndex != nil {
			controls, _, err := imageIndexMetadata(ctx, source, o)
			if err != nil {
				return Result{}, err
			}
			for _, d := range controls {
				raw, err := readBlob(ctx, source, d, 8<<20)
				if err != nil {
					return Result{}, err
				}
				if _, err := writer.addBlobBytes(raw, d.MediaType); err != nil {
					return Result{}, err
				}
			}
			continue
		}
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
	var digest, semantic string
	if bootstrapSource == "" {
		digest, semantic, err = writer.writePublicationMetadata(root, nil)
	} else {
		digest, err = writer.copyPublicationBootstrap(ctx, bootstrapSource, expectedRoot)
		if err == nil {
			semantic, err = root.Digest()
		}
	}
	if err != nil {
		return Result{}, err
	}
	if expectedRoot != "" && digest != expectedRoot {
		return Result{}, e.New(e.CodeVerifyFailed, "exported publication metadata differs from expected root")
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
	return exportMetadata(ctx, source, out, "")
}

// ExportMetadataWithExpectedRoot exports bounded controls only after both the
// complete source and the generated metadata match the exact published root.
// It commits no destination on an identity mismatch and issues no payload receipt.
func ExportMetadataWithExpectedRoot(ctx context.Context, source, out, expectedRoot string) (Result, error) {
	if !canonical.IsDigest(expectedRoot) {
		return Result{}, e.New(e.CodeUsage, "metadata export requires an exact expected root digest")
	}
	return exportMetadata(ctx, source, out, expectedRoot)
}

// CopyMetadataWithExpectedRoot preserves the exact bootstrap of a metadata-only
// or full source after bounded metadata inspection and expected-root binding.
// It copies controls and evidence only, never issuing payload or trust authority.
func CopyMetadataWithExpectedRoot(ctx context.Context, source, out, expectedRoot string) (Result, error) {
	if !canonical.IsDigest(expectedRoot) {
		return Result{}, e.New(e.CodeUsage, "metadata copy requires an exact expected root digest")
	}
	inspection, err := inspectMetadataProgress(ctx, source, true, expectedRoot, nil)
	if err != nil {
		return Result{}, err
	}
	sources := map[string]string{}
	for _, object := range inspection.Root.Objects {
		sources[object.Name] = source
	}
	return buildMetadataRoot(ctx, out, inspection.Root, sources, expectedRoot, source)
}

func exportMetadata(ctx context.Context, source, out, expectedRoot string) (Result, error) {
	inspection, err := Inspect(ctx, source)
	if err != nil {
		return Result{}, err
	}
	if expectedRoot != "" && inspection.RootDigest != expectedRoot {
		return Result{}, e.New(e.CodeVerifyFailed, "publication changed before metadata export")
	}
	sources := map[string]string{}
	for _, o := range inspection.Root.Objects {
		sources[o.Name] = source
	}
	return buildMetadataRoot(ctx, out, inspection.Root, sources, inspection.RootDigest, source)
}

// Copy only the pinned, previously inspected release bootstrap, never rebuild
// identified metadata using the current constructor's serialization defaults.
func (writer layoutWriter) copyPublicationBootstrap(ctx context.Context, source, expectedRoot string) (string, error) {
	index, raw, err := readIndex(ctx, source)
	if err != nil {
		return "", err
	}
	if digestBytes(raw) != expectedRoot || index.artifactType() != MediaPublicationRoot || len(index.Manifests) != 1 {
		return "", e.New(e.CodeVerifyFailed, "publication bootstrap differs from its inspected root")
	}
	if _, err := readPublicationManifest(ctx, source, index.Manifests[0]); err != nil {
		return "", err
	}
	if err := writer.copyClosure(ctx, source, index.Manifests[0]); err != nil {
		return "", err
	}
	return writer.writeIndexBytes(raw)
}

func metadataManifest(ctx context.Context, root string, object PublishedObject) (ociManifest, error) {
	manifestBytes, err := readBlob(ctx, root, Descriptor{Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return ociManifest{}, err
	}
	if err := canonical.ValidateJSONDocument(manifestBytes); err != nil {
		return ociManifest{}, err
	}
	if object.ArtifactType == MediaBundle {
		if err := validateBundleManifestDocument(manifestBytes); err != nil {
			return ociManifest{}, err
		}
	}
	if object.ArtifactType == MediaAICRArtifact {
		if _, err := admitBundleManifest(manifestBytes); err != nil {
			return ociManifest{}, err
		}
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
	if err := m.validateArtifactConfig(); err != nil {
		return ociManifest{}, err
	}
	if !types.MediaType(object.ArtifactType).IsImage() && m.artifactType() != object.ArtifactType {
		return ociManifest{}, e.New(e.CodeVerifyFailed, "metadata member artifact type differs from its release binding")
	}
	if m.Config.MediaType == MediaOctetStream {
		if err := verifyPublishedObjectBinding(ctx, root, object, Descriptor{MediaType: m.MediaType, Digest: object.DescriptorDigest, Size: object.Size}, nil); err != nil {
			return ociManifest{}, err
		}
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
