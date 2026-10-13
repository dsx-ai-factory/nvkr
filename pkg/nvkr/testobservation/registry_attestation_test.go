// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	containerregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

// Opaque local-key signatures test the transport's crypto seam only. Production
// accepts stock keyless Sigstore bundles; these private fixture bytes cannot pass
// that verifier and no public constructor can select this fixture.
type localBundleCryptoFixture struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func newLocalBundleCryptoFixture(t *testing.T) *localBundleCryptoFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &localBundleCryptoFixture{private: private, public: public}
}
func bundleFixtureMessage(subject sign.Subject, predicate []byte) ([]byte, error) {
	return canonical.Marshal(struct {
		Subject   sign.Subject `json:"subject"`
		Predicate []byte       `json:"predicate"`
	}{subject, predicate})
}
func (f *localBundleCryptoFixture) CreateTestObservationBundle(ctx context.Context, s sign.Subject, p []byte) ([]byte, error) {
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	raw, err := bundleFixtureMessage(s, p)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(f.private, raw), nil
}
func (f *localBundleCryptoFixture) VerifyTestObservationBundle(ctx context.Context, s sign.Subject, p, b []byte) error {
	if err := captureContext(ctx); err != nil {
		return err
	}
	raw, err := bundleFixtureMessage(s, p)
	if err != nil {
		return err
	}
	if !ed25519.Verify(f.public, raw, b) {
		return e.New(e.CodeSignatureInvalid, "fixture exact subject/predicate signature rejected")
	}
	return nil
}

func TestStandardObservationReferrerTransportRoundTrip(t *testing.T) {
	server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/observations"
	crypto := newLocalBundleCryptoFixture(t)
	options := RegistryOptions{Transport: delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}}
	authenticator := &stockObservationAuthenticator{signer: crypto, options: options}
	root := emitFixture(t, observationFixture(), passingJUnit)
	result, err := publishWith(t.Context(), root, repository, options.Transport, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Successful {
		t.Fatal("incomplete publication")
	}
	bundle, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	subject := sign.Subject{Ref: result.Locator.Reference, Digest: strings.SplitN(result.Locator.Reference, "@", 2)[1]}
	predicate, err := bundle.Canonical(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := authenticator.VerifyTestObservation(t.Context(), subject, predicate); err != nil {
		t.Fatal(err)
	}
	requireCode(t, authenticator.VerifyTestObservation(t.Context(), subject, []byte(`{}`)), e.CodeSignatureInvalid)
	missing := subject
	missing.Ref = repository + "@" + rawPin("missing")
	missing.Digest = rawPin("missing")
	requireCode(t, authenticator.VerifyTestObservation(t.Context(), missing, predicate), e.CodeUnavailable)
}

func TestObservationReferrerCarrierRejectsTampering(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*oci.Manifest, *oci.Descriptor)
	}{
		{"schema", func(m *oci.Manifest, _ *oci.Descriptor) { m.SchemaVersion = 1 }},
		{"media", func(m *oci.Manifest, _ *oci.Descriptor) { m.MediaType = oci.MediaTypeImageIndex }},
		{"artifact type", func(m *oci.Manifest, _ *oci.Descriptor) { m.ArtifactType = "wrong" }},
		{"missing subject", func(m *oci.Manifest, _ *oci.Descriptor) { m.Subject = nil }},
		{"wrong subject", func(m *oci.Manifest, _ *oci.Descriptor) {
			m.Subject.Digest = descriptor(oci.MediaTypeImageManifest, []byte("wrong")).Digest
		}},
		{"subject size", func(m *oci.Manifest, _ *oci.Descriptor) { m.Subject.Size++ }},
		{"extra layer", func(m *oci.Manifest, _ *oci.Descriptor) { m.Layers = append(m.Layers, m.Layers[0]) }},
		{"missing layer", func(m *oci.Manifest, _ *oci.Descriptor) { m.Layers = nil }},
		{"wrong predicate", func(m *oci.Manifest, _ *oci.Descriptor) { m.Annotations[attestationPredicateAnnotation] = "wrong" }},
		{"unknown annotation", func(m *oci.Manifest, _ *oci.Descriptor) { m.Annotations["unknown"] = "value" }},
		{"wrong config", func(m *oci.Manifest, _ *oci.Descriptor) { m.Config.MediaType = ObservationMediaType }},
		{"config data", func(m *oci.Manifest, _ *oci.Descriptor) { m.Config.Data = []byte("{}") }},
		{"external blob", func(m *oci.Manifest, _ *oci.Descriptor) {
			m.Layers[0].URLs = []string{"https://unadmitted.example/blob"}
		}},
		{"inline blob", func(m *oci.Manifest, _ *oci.Descriptor) { m.Layers[0].Data = []byte("bundle") }},
		{"wrong blob media", func(m *oci.Manifest, _ *oci.Descriptor) { m.Layers[0].MediaType = FileMediaType }},
		{"oversized blob", func(m *oci.Manifest, _ *oci.Descriptor) { m.Layers[0].Size = maxAttestationBytes + 1 }},
		{"descriptor media", func(_ *oci.Manifest, d *oci.Descriptor) { d.MediaType = oci.MediaTypeImageIndex }},
		{"descriptor oversized", func(_ *oci.Manifest, d *oci.Descriptor) { d.Size = maxManifestBytes + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
			defer server.Close()
			repo, err := observationRepository(strings.TrimPrefix(server.URL, "http://")+"/observations", delivery.RegistryOptions{PlainHTTP: true, Anonymous: true})
			if err != nil {
				t.Fatal(err)
			}
			target := descriptor(oci.MediaTypeImageManifest, []byte(`{}`))
			config := descriptor(oci.MediaTypeImageConfig, []byte(`{}`))
			blob := descriptor(AttestationMediaType, []byte("opaque fixture"))
			for _, member := range []struct {
				d   oci.Descriptor
				raw []byte
			}{{config, []byte(`{}`)}, {blob, []byte("opaque fixture")}} {
				if err := repo.Push(t.Context(), member.d, bytes.NewReader(member.raw)); err != nil {
					t.Fatal(err)
				}
			}
			manifest := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, ArtifactType: AttestationMediaType, Subject: &target, Config: config, Layers: []oci.Descriptor{blob}, Annotations: map[string]string{attestationPredicateAnnotation: PredicateType}}
			var changed oci.Descriptor
			tc.edit(&manifest, &changed)
			raw, err := canonical.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			candidate := descriptor(oci.MediaTypeImageManifest, raw)
			// OCI registry storage accepts arbitrary immutable documents; the owning
			// verifier, not successful upload, must authenticate the supported carrier.
			if err := repo.Push(t.Context(), candidate, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			candidate.ArtifactType = AttestationMediaType
			candidate.Annotations = map[string]string{attestationPredicateAnnotation: PredicateType}
			if changed.MediaType != "" {
				candidate.MediaType = changed.MediaType
			}
			if changed.Size != 0 {
				candidate.Size = changed.Size
			}
			got, err := fetchObservationAttestation(t.Context(), repo, descriptor(oci.MediaTypeImageManifest, []byte(`{}`)), candidate)
			requireCode(t, err, e.CodeVerifyFailed)
			if got != nil {
				t.Fatal("malformed carrier returned bundle bytes")
			}
		})
	}
}

func TestObservationManifestCarrierRejectsTampering(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*oci.Manifest)
	}{
		{"schema", func(m *oci.Manifest) { m.SchemaVersion = 1 }},
		{"artifact type", func(m *oci.Manifest) { m.ArtifactType = "wrong" }},
		{"unexpected subject", func(m *oci.Manifest) { m.Subject = &m.Config }},
		{"unexpected annotations", func(m *oci.Manifest) { m.Annotations = map[string]string{"secret": "unadmitted"} }},
		{"config media", func(m *oci.Manifest) { m.Config.MediaType = FileMediaType }},
		{"config external", func(m *oci.Manifest) { m.Config.URLs = []string{"https://other.example/blob"} }},
		{"missing layer", func(m *oci.Manifest) { m.Layers = nil }},
		{"extra layer", func(m *oci.Manifest) { m.Layers = append(m.Layers, m.Layers[0]) }},
		{"layer media", func(m *oci.Manifest) { m.Layers[0].MediaType = ObservationMediaType }},
		{"layer size", func(m *oci.Manifest) { m.Layers[0].Size++ }},
		{"layer digest", func(m *oci.Manifest) { m.Layers[0].Digest = m.Config.Digest }},
		{"layer name", func(m *oci.Manifest) { m.Layers[0].Annotations[oci.AnnotationTitle] = "other.xml" }},
		{"layer inline", func(m *oci.Manifest) { m.Layers[0].Data = []byte(passingJUnit) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := emitFixture(t, observationFixture(), passingJUnit)
			bundle, err := Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer bundle.Close()
			observation := bundle.Observation()
			control, err := bundle.Canonical(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			expected := Expected{ObservationDigest: bundle.Digest(), Inputs: observation.Inputs, Subjects: observation.Subjects, Suite: observation.Suite}
			server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
			defer server.Close()
			repository := strings.TrimPrefix(server.URL, "http://") + "/observations"
			transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
			repo, err := observationRepository(repository, transport)
			if err != nil {
				t.Fatal(err)
			}
			config := descriptor(ObservationMediaType, control)
			layer := descriptor(FileMediaType, []byte(passingJUnit))
			layer.Annotations = map[string]string{oci.AnnotationTitle: observation.Files[0].Name}
			for _, member := range []struct {
				d   oci.Descriptor
				raw []byte
			}{{config, control}, {layer, []byte(passingJUnit)}} {
				if err := repo.Push(t.Context(), member.d, bytes.NewReader(member.raw)); err != nil {
					t.Fatal(err)
				}
			}
			manifest := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, ArtifactType: ArtifactMediaType, Config: config, Layers: []oci.Descriptor{layer}}
			tc.edit(&manifest)
			raw, err := canonical.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			desc := descriptor(oci.MediaTypeImageManifest, raw)
			if err := repo.Push(t.Context(), desc, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			receipt, err := verifyOCIWith(t.Context(), repository+"@"+desc.Digest.String(), expected, transport, newLocalKeyAttester(t))
			if receipt != nil {
				t.Fatal("malformed carrier issued receipt")
			}
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestObservationAttestationMissingAndReferrerBudget(t *testing.T) {
	server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/observations"
	transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	repo, err := observationRepository(repository, transport)
	if err != nil {
		t.Fatal(err)
	}
	config := []byte("{}")
	cd := descriptor(oci.MediaTypeImageConfig, config)
	if err := repo.Push(t.Context(), cd, bytes.NewReader(config)); err != nil {
		t.Fatal(err)
	}
	targetManifest := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, Config: cd, Layers: []oci.Descriptor{}}
	raw, err := canonical.Marshal(targetManifest)
	if err != nil {
		t.Fatal(err)
	}
	target := descriptor(oci.MediaTypeImageManifest, raw)
	if err := repo.Push(t.Context(), target, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	authenticator := &stockObservationAuthenticator{signer: newLocalBundleCryptoFixture(t), options: RegistryOptions{Transport: transport}}
	subject := sign.Subject{Ref: repository + "@" + target.Digest.String(), Digest: target.Digest.String()}
	requireCode(t, authenticator.VerifyTestObservation(t.Context(), subject, []byte(`{}`)), e.CodeSignatureInvalid)
	for i := 0; i <= maxAttestationReferrers; i++ {
		blob := []byte{byte(i)}
		layer := descriptor(AttestationMediaType, blob)
		if err := repo.Push(t.Context(), layer, bytes.NewReader(blob)); err != nil {
			t.Fatal(err)
		}
		manifest := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, ArtifactType: AttestationMediaType, Subject: &target, Config: cd, Layers: []oci.Descriptor{layer}, Annotations: map[string]string{attestationPredicateAnnotation: PredicateType}}
		encoded, err := canonical.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Push(t.Context(), descriptor(oci.MediaTypeImageManifest, encoded), bytes.NewReader(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	requireCode(t, authenticator.VerifyTestObservation(t.Context(), subject, []byte(`{}`)), e.CodeVerifyFailed)
}
