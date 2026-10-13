// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
)

const AttestationMediaType = sign.TestObservationBundleMediaType
const attestationPredicateAnnotation = "dev.sigstore.bundle.predicateType"
const maxAttestationBytes = 8 << 20
const maxAttestationReferrers = 32

type observationBundleCrypto interface {
	CreateTestObservationBundle(context.Context, sign.Subject, []byte) ([]byte, error)
	VerifyTestObservationBundle(context.Context, sign.Subject, []byte, []byte) error
}

// This production bridge keeps every registry operation in the guarded ORAS
// owner. Cosign receives a fixed statement or opaque bundle through held inputs;
// it neither resolves the registry nor receives its username/password.
type stockObservationAuthenticator struct {
	signer  observationBundleCrypto
	options RegistryOptions
}

func (a *stockObservationAuthenticator) repository(ctx context.Context, subject sign.Subject) (*remote.Repository, oci.Descriptor, error) {
	if err := captureContext(ctx); err != nil {
		return nil, oci.Descriptor{}, err
	}
	if err := subject.Validate(); err != nil {
		return nil, oci.Descriptor{}, err
	}
	parsed, err := pinnedReference(subject.Ref)
	if err != nil {
		return nil, oci.Descriptor{}, err
	}
	if err := validateCredentialScope(parsed.Registry, a.options); err != nil {
		return nil, oci.Descriptor{}, err
	}
	repo, err := observationRepository(parsed.Registry+"/"+parsed.Repository, a.options.Transport)
	if err != nil {
		return nil, oci.Descriptor{}, err
	}
	repo.MaxMetadataBytes = maxManifestBytes
	resolved, err := repo.Resolve(ctx, subject.Digest)
	if err != nil {
		return nil, oci.Descriptor{}, registryError(ctx, err, "resolving observation attachment subject")
	}
	if resolved.Digest.String() != subject.Digest || resolved.MediaType != oci.MediaTypeImageManifest || resolved.Size < 0 || resolved.Size > maxManifestBytes {
		return nil, oci.Descriptor{}, invalid("observation attachment subject identity or type differs")
	}
	// The standard referrer's subject carries only immutable transport identity.
	return repo, oci.Descriptor{MediaType: resolved.MediaType, Digest: resolved.Digest, Size: resolved.Size}, nil
}

func (a *stockObservationAuthenticator) AttestTestObservation(ctx context.Context, subject sign.Subject, predicate []byte) error {
	if a == nil || a.signer == nil {
		return e.New(e.CodeTrustPolicyMissing, "observation bundle signer unavailable")
	}
	if err := captureContext(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	repo, target, err := a.repository(ctx, subject)
	if err != nil {
		return err
	}
	raw, err := a.signer.CreateTestObservationBundle(ctx, subject, predicate)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxAttestationBytes {
		return invalid("observation Sigstore bundle exceeds byte budget")
	}
	if err := a.signer.VerifyTestObservationBundle(ctx, subject, predicate, raw); err != nil {
		return err
	}
	// OCI1.1 Sigstore bundle carrier matches stock cosign3.1.3 WriteReferrer:
	// an empty OCI config, one standard bundle layer, exact subject descriptor,
	// artifact media type and predicate annotation. ORAS owns referrers indexing.
	config := []byte("{}")
	configDescriptor := descriptor(oci.MediaTypeImageConfig, config)
	layer := descriptor(AttestationMediaType, raw)
	for _, member := range []struct {
		desc oci.Descriptor
		raw  []byte
	}{{configDescriptor, config}, {layer, raw}} {
		if err := repo.Push(ctx, member.desc, bytes.NewReader(member.raw)); err != nil {
			return registryError(ctx, err, "uploading observation attestation content")
		}
	}
	manifest := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, ArtifactType: AttestationMediaType, Subject: &target, Config: configDescriptor, Layers: []oci.Descriptor{layer}, Annotations: map[string]string{attestationPredicateAnnotation: PredicateType}}
	encoded, err := canonical.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := repo.Push(ctx, descriptor(oci.MediaTypeImageManifest, encoded), bytes.NewReader(encoded)); err != nil {
		return registryError(ctx, err, "uploading standard observation attestation referrer")
	}
	return a.VerifyTestObservation(ctx, subject, predicate)
}

func (a *stockObservationAuthenticator) VerifyTestObservation(ctx context.Context, subject sign.Subject, predicate []byte) error {
	if a == nil || a.signer == nil {
		return e.New(e.CodeTrustPolicyMissing, "observation bundle verifier unavailable")
	}
	if err := captureContext(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	repo, target, err := a.repository(ctx, subject)
	if err != nil {
		return err
	}
	candidates := make([]oci.Descriptor, 0)
	err = repo.Referrers(ctx, target, AttestationMediaType, func(page []oci.Descriptor) error {
		if len(page) > maxAttestationReferrers-len(candidates) {
			return invalid("observation exceeds attestation referrer budget")
		}
		candidates = append(candidates, page...)
		return nil
	})
	if err != nil {
		return registryError(ctx, err, "listing observation attestation referrers")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Digest.String() < candidates[j].Digest.String() })
	var last error
	for i, candidate := range candidates {
		if i > 0 && candidate.Digest == candidates[i-1].Digest {
			return invalid("duplicate observation attestation referrer")
		}
		if candidate.Annotations[attestationPredicateAnnotation] != PredicateType {
			continue
		}
		raw, err := fetchObservationAttestation(ctx, repo, target, candidate)
		if err == nil {
			err = a.signer.VerifyTestObservationBundle(ctx, subject, predicate, raw)
		}
		if err == nil {
			return captureContext(ctx)
		}
		if canceled := captureContext(ctx); canceled != nil {
			return canceled
		}
		last = err
	}
	if last != nil {
		return last
	}
	return e.New(e.CodeSignatureInvalid, "required observation Sigstore bundle referrer is missing")
}

func fetchObservationAttestation(ctx context.Context, repo *remote.Repository, target, candidate oci.Descriptor) ([]byte, error) {
	if candidate.MediaType != oci.MediaTypeImageManifest || candidate.ArtifactType != AttestationMediaType || !canonical.IsDigest(candidate.Digest.String()) || candidate.Size < 0 || candidate.Size > maxManifestBytes || len(candidate.URLs) != 0 || len(candidate.Data) != 0 || candidate.Platform != nil {
		return nil, invalid("unsupported observation attestation referrer descriptor")
	}
	if err := validateAttestationAnnotations(candidate.Annotations); err != nil {
		return nil, err
	}
	raw, err := fetchExact(ctx, repo, candidate, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	// OCI carrier bytes are blob identities, not NVKR structured identities. Stock
	// manifests need not be canonically serialized, but duplicate/unknown fields
	// are rejected by the established codec/stdlib decoder before interpretation.
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, err
	}
	var manifest oci.Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding observation attestation manifest")
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != oci.MediaTypeImageManifest || manifest.ArtifactType != AttestationMediaType || manifest.Subject == nil || manifest.Subject.Digest != target.Digest || manifest.Subject.MediaType != target.MediaType || manifest.Subject.Size != target.Size || len(manifest.Layers) != 1 {
		return nil, invalid("observation attestation carrier does not bind the exact subject")
	}
	if err := validateDescriptor(*manifest.Subject, target.MediaType, "", maxManifestBytes); err != nil {
		return nil, err
	}
	if err := validateAttestationAnnotations(manifest.Annotations); err != nil {
		return nil, err
	}
	wantConfig := descriptor(oci.MediaTypeImageConfig, []byte("{}"))
	if err := validateDescriptor(manifest.Config, oci.MediaTypeImageConfig, "", 2); err != nil {
		return nil, err
	}
	if manifest.Config.Digest != wantConfig.Digest || manifest.Config.Size != wantConfig.Size {
		return nil, invalid("observation attestation config is not the standard empty config")
	}
	if _, err := fetchExact(ctx, repo, manifest.Config, 2); err != nil {
		return nil, err
	}
	layer := manifest.Layers[0]
	if err := validateDescriptor(layer, AttestationMediaType, "", maxAttestationBytes); err != nil {
		return nil, err
	}
	return fetchExact(ctx, repo, layer, maxAttestationBytes)
}

func validateAttestationAnnotations(annotations map[string]string) error {
	if annotations[attestationPredicateAnnotation] != PredicateType {
		return invalid("observation attestation predicate annotation differs")
	}
	for name, value := range annotations {
		switch name {
		case attestationPredicateAnnotation:
		case oci.AnnotationCreated:
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return invalid("invalid standard attestation creation annotation")
			}
		default:
			return invalid("unsupported observation attestation annotation")
		}
	}
	return nil
}
