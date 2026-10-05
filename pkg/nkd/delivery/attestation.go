package delivery

import (
	"bytes"
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// AttestEvidence builds a new immutable release layout containing only bundled
// SBOM attestations. The callback owns cryptographic creation and verification;
// this module enforces exact document, subject, membership and byte closure.
func AttestEvidence(ctx context.Context, source, out string, attest func(context.Context, string, []byte) ([]byte, error)) (Result, error) {
	if attest == nil {
		return Result{}, e.New(e.CodeEngineCapability, "SBOM attestation adapter is required")
	}
	inspection, err := Inspect(ctx, source)
	if err != nil {
		return Result{}, err
	}
	if inspection.Root.Release == nil {
		return Result{}, e.New(e.CodeEngineCapability, "SBOM attestation requires release metadata")
	}
	sourceIndex, _, err := readIndex(ctx, source)
	if err != nil {
		return Result{}, err
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	root := inspection.Root
	root.Objects = append([]PublishedObject(nil), root.Objects...)
	var direct []Descriptor
	for i, object := range root.Objects {
		descriptor := Descriptor{MediaType: publishedManifestMediaType(object), Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
		if object.Evidence == nil {
			if err := writer.copyClosure(ctx, source, descriptor); err != nil {
				return Result{}, err
			}
			if object.Output == nil {
				for _, original := range sourceIndex.Manifests {
					if original.Digest == descriptor.Digest {
						direct = append(direct, original)
					}
				}
			}
			continue
		}
		if object.ArtifactType != "application/vnd.cyclonedx+json" {
			return Result{}, e.New(e.CodeVerifyFailed, "prepared evidence must be a CycloneDX document")
		}
		_, document, err := ReadObject(ctx, source, object.Name)
		if err != nil {
			return Result{}, err
		}
		bundle, err := attest(ctx, object.Evidence.SubjectDigest, document)
		if err != nil {
			return Result{}, err
		}
		decoded, err := sbom.AttestedDocument(bundle, object.Evidence.SubjectDigest)
		if err != nil {
			return Result{}, err
		}
		if !bytes.Equal(decoded, document) {
			return Result{}, e.New(e.CodeVerifyFailed, "attestation changed the SBOM document")
		}
		owner, err := evidenceOwner(root.Objects, object.Evidence)
		if err != nil {
			return Result{}, err
		}
		descriptor, blob, err := writer.addBytesObject(ctx, ObjectInput{Name: object.Name, Kind: object.Kind, ArtifactType: sbom.BundleMediaType, LayerMediaType: sbom.BundleMediaType,
			SemanticDigest: object.SemanticDigest, Bytes: bundle, annotations: map[string]string{"dev.sigstore.bundle.predicateType": sbom.CycloneDXPredicateType}, subject: &Descriptor{MediaType: publishedManifestMediaType(owner), Digest: owner.DescriptorDigest, Size: owner.Size}})
		if err != nil {
			return Result{}, err
		}
		root.Objects[i].DescriptorDigest = descriptor.Digest
		root.Objects[i].BlobDigest = blob
		root.Objects[i].Size = descriptor.Size
		root.Objects[i].ArtifactType = sbom.BundleMediaType
	}
	digest, semantic, err := writer.writePublicationMetadata(root, direct)
	if err != nil {
		return Result{}, err
	}
	if _, err := Inspect(ctx, stage); err != nil {
		return Result{}, err
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: digest, SemanticDigest: semantic}, nil
}

// EvidenceDocument returns the canonical CycloneDX document from either a
// private preparation layout or the final attestation-only release layout.
func EvidenceDocument(ctx context.Context, root, name string) (PublishedObject, []byte, error) {
	object, raw, err := ReadObject(ctx, root, name)
	if err != nil {
		return PublishedObject{}, nil, err
	}
	if object.Evidence == nil {
		return PublishedObject{}, nil, e.New(e.CodeVerifyFailed, "object is not SBOM evidence")
	}
	if object.ArtifactType == sbom.BundleMediaType {
		raw, err = sbom.AttestedDocument(raw, object.Evidence.SubjectDigest)
	}
	return object, raw, err
}

// RequireAttestedEvidence rejects raw preparation documents at production boundaries.
func RequireAttestedEvidence(inspection Inspection) error {
	if inspection.Root.Release == nil {
		return e.New(e.CodeVerifyFailed, "attested release metadata is required")
	}
	count := 0
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		count++
		if object.ArtifactType != sbom.BundleMediaType {
			return e.New(e.CodeProvenanceMissing, "release contains an unattested SBOM")
		}
	}
	if count == 0 {
		return e.New(e.CodeProvenanceMissing, "release contains no SBOM attestations")
	}
	return nil
}
