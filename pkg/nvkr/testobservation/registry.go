// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	ArtifactMediaType    = "application/vnd.nvkr.test-observation.v1"
	ObservationMediaType = "application/vnd.nvkr.test-observation.v1+json"
	FileMediaType        = "application/vnd.nvkr.test-observation.file.v1"
	LocatorSchemaVersion = "nvkr.dev/test-observation-locator/v1"
	maxManifestBytes     = 1 << 20
)

// Locator supplies only transport coordinates, never consumer expectations or trust.
type Locator struct {
	SchemaVersion     string `json:"schemaVersion"`
	Reference         string `json:"reference"`
	ObservationDigest string `json:"observationDigest"`
}

func (l Locator) Validate() error {
	if l.SchemaVersion != LocatorSchemaVersion || !canonical.IsDigest(l.ObservationDigest) {
		return invalid("unknown locator schema or observation identity")
	}
	_, err := pinnedReference(l.Reference)
	return err
}

func DecodeLocator(raw []byte) (Locator, error) {
	var l Locator
	if len(raw) > 16384 {
		return l, invalid("locator exceeds byte budget")
	}
	if err := canonical.UnmarshalExact(raw, &l); err != nil {
		return l, err
	}
	return l, l.Validate()
}

// RegistryOptions selects an explicitly admitted transport and independent signer policy.
// There is no public fake verifier or command-runner selector at this authority seam.
type RegistryOptions struct {
	Transport    delivery.RegistryOptions
	CosignBinary string
	CosignDigest string
	Identity     string
	Issuer       string
	Credential   sign.Credential
}

type observationAuthenticator interface {
	AttestTestObservation(context.Context, sign.Subject, []byte) error
	VerifyTestObservation(context.Context, sign.Subject, []byte) error
}

func admittedAuthenticator(ctx context.Context, options RegistryOptions) (observationAuthenticator, error) {
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	if err := options.Transport.Validate(); err != nil {
		return nil, err
	}
	if !safetext.Valid(options.Identity, 4096, false) || !safetext.Valid(options.Issuer, 4096, false) || strings.TrimSpace(options.Identity) != options.Identity || strings.TrimSpace(options.Issuer) != options.Issuer || !canonical.IsDigest(options.CosignDigest) || options.CosignBinary == "" {
		return nil, e.New(e.CodeTrustPolicyMissing, "observation authentication requires independently selected identity, issuer and cosign executable pin")
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "observation authentication requires Linux pinned cosign execution")
	}
	// Executable capability/pin admission precedes bundle, registry and output work.
	preflight, err := sign.NewPinnedCosignSigner(options.CosignBinary, options.CosignDigest)
	if err != nil {
		return nil, err
	}
	if err := preflight.Preflight(ctx); err != nil {
		return nil, err
	}
	return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary, BinaryDigest: options.CosignDigest,
		Identity: "^" + regexp.QuoteMeta(options.Identity) + "$", Issuer: options.Issuer, Credential: options.Credential})
}

// Published is diagnostic. A failed attachment/readback may leave immutable OCI
// content; Successful is set only after independent complete-bundle verification.
type Published struct {
	Locator    Locator `json:"locator"`
	Successful bool    `json:"successful"`
}

func Publish(ctx context.Context, root, repository string, options RegistryOptions) (*Published, error) {
	authenticator, err := admittedAuthenticator(ctx, options)
	if err != nil {
		return nil, err
	}
	return publishWith(ctx, root, repository, options.Transport, authenticator)
}

func publishWith(ctx context.Context, root, repository string, transport delivery.RegistryOptions, authenticator observationAuthenticator) (*Published, error) {
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	if authenticator == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "observation attester is unavailable")
	}
	repo, err := observationRepository(repository, transport)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	bundle, err := Open(ctx, root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = bundle.Close() }()
	raw, err := bundle.Canonical(ctx)
	if err != nil {
		return nil, err
	}
	observation := bundle.Observation()
	manifest := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, ArtifactType: ArtifactMediaType,
		Config: descriptor(ObservationMediaType, raw), Layers: make([]oci.Descriptor, 0, len(observation.Files))}
	if err := repo.Push(ctx, manifest.Config, bytes.NewReader(raw)); err != nil {
		return nil, registryError(ctx, err, "uploading observation")
	}
	for _, file := range observation.Files {
		payload, err := bundle.Read(ctx, file.Name)
		if err != nil {
			return nil, err
		}
		desc := descriptor(FileMediaType, payload)
		desc.Annotations = map[string]string{oci.AnnotationTitle: file.Name}
		if desc.Digest.String() != file.Digest || desc.Size != file.Size {
			return nil, invalid("captured payload diverges from signed inventory")
		}
		if err := repo.Push(ctx, desc, bytes.NewReader(payload)); err != nil {
			return nil, registryError(ctx, err, "uploading observation file")
		}
		manifest.Layers = append(manifest.Layers, desc)
	}
	encoded, err := canonical.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	manifestDescriptor := descriptor(oci.MediaTypeImageManifest, encoded)
	result := &Published{Locator: Locator{SchemaVersion: LocatorSchemaVersion, Reference: repository + "@" + manifestDescriptor.Digest.String(), ObservationDigest: bundle.Digest()}}
	if err := repo.Push(ctx, manifestDescriptor, bytes.NewReader(encoded)); err != nil {
		return result, registryError(ctx, err, "uploading observation manifest")
	}
	subject := sign.Subject{Ref: result.Locator.Reference, Digest: manifestDescriptor.Digest.String()}
	if err := authenticator.AttestTestObservation(ctx, subject, raw); err != nil {
		return result, err
	}
	expected := Expected{ObservationDigest: bundle.Digest(), Inputs: observation.Inputs, Subjects: observation.Subjects, Suite: observation.Suite}
	verified, err := verifyOCIWith(ctx, result.Locator.Reference, expected, transport, authenticator)
	if err != nil {
		return result, err
	}
	if err := verified.Close(); err != nil {
		return result, err
	}
	if err := captureContext(ctx); err != nil {
		return result, err
	}
	result.Successful = true
	return result, nil
}

// Verified owns an authenticated observation and held covered bytes. Exported
// descriptions never recreate it; closure and request cancellation revoke access.
type Verified struct {
	mu        sync.Mutex
	bundle    *Bundle
	ctx       context.Context
	reference string
	closed    bool
	identity  string
	issuer    string
}

// Signer returns the exact independently admitted certificate identity/issuer.
// Stock authentication matched the certificate to these literal values first.
func (v *Verified) Signer(ctx context.Context) (string, string, error) {
	if v == nil {
		return "", "", invalid("authenticated observation is unavailable")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.check(ctx); err != nil {
		return "", "", err
	}
	if v.identity == "" || v.issuer == "" {
		return "", "", invalid("authenticated signer description is unavailable")
	}
	return v.identity, v.issuer, nil
}

func (v *Verified) Read(ctx context.Context, name string) ([]byte, error) {
	if v == nil {
		return nil, invalid("authenticated observation is unavailable")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.check(ctx); err != nil {
		return nil, err
	}
	raw, err := v.bundle.Read(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := v.check(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (v *Verified) Reference(ctx context.Context) (string, error) {
	if v == nil {
		return "", invalid("authenticated observation is unavailable")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.check(ctx); err != nil {
		return "", err
	}
	return v.reference, nil
}

func (v *Verified) Observation(ctx context.Context) (Observation, error) {
	if v == nil {
		return Observation{}, invalid("authenticated observation is unavailable")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.check(ctx); err != nil {
		return Observation{}, err
	}
	return v.bundle.Observation(), nil
}

func (v *Verified) Description(ctx context.Context) (Verification, error) {
	if v == nil {
		return Verification{}, invalid("authenticated observation is unavailable")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.check(ctx); err != nil {
		return Verification{}, err
	}
	return Verification{ObservationDigest: v.bundle.Digest(), SelfConsistent: true, Passing: v.bundle.Passing(), SignerAuthenticated: true}, nil
}

func (v *Verified) check(ctx context.Context) error {
	if err := captureContext(ctx); err != nil {
		return err
	}
	if v.closed || v.bundle == nil {
		return invalid("authenticated observation was closed")
	}
	return captureContext(v.ctx)
}

func (v *Verified) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	if v.bundle == nil {
		return nil
	}
	return v.bundle.Close()
}

func VerifyOCI(ctx context.Context, reference string, expected Expected, options RegistryOptions) (*Verified, error) {
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	authenticator, err := admittedAuthenticator(ctx, options)
	if err != nil {
		return nil, err
	}
	verified, err := verifyOCIWith(ctx, reference, expected, options.Transport, authenticator)
	if err != nil {
		return nil, err
	}
	verified.identity = options.Identity
	verified.issuer = options.Issuer
	return verified, nil
}

func verifyOCIWith(ctx context.Context, reference string, expected Expected, transport delivery.RegistryOptions, authenticator observationAuthenticator) (*Verified, error) {
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	if authenticator == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "observation verifier is unavailable")
	}
	parsed, err := pinnedReference(reference)
	if err != nil {
		return nil, err
	}
	repo, err := observationRepository(parsed.Registry+"/"+parsed.Repository, transport)
	if err != nil {
		return nil, err
	}
	lifetime := ctx
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	desc, err := repo.Resolve(ctx, parsed.Reference)
	if err != nil {
		return nil, registryError(ctx, err, "resolving observation manifest")
	}
	if desc.Digest.String() != parsed.Reference || desc.MediaType != oci.MediaTypeImageManifest {
		return nil, invalid("resolved observation manifest identity/type differs")
	}
	rawManifest, err := fetchExact(ctx, repo, desc, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	var manifest oci.Manifest
	if err := canonical.UnmarshalExact(rawManifest, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != oci.MediaTypeImageManifest || manifest.ArtifactType != ArtifactMediaType || manifest.Subject != nil || len(manifest.Annotations) != 0 || len(manifest.Layers) >= MaxFiles {
		return nil, invalid("unsupported observation OCI manifest")
	}
	if err := validateDescriptor(manifest.Config, ObservationMediaType, "", MaxObservationBytes); err != nil {
		return nil, err
	}
	rawObservation, err := fetchExact(ctx, repo, manifest.Config, MaxObservationBytes)
	if err != nil {
		return nil, err
	}
	observation, err := Decode(rawObservation)
	if err != nil {
		return nil, err
	}
	if err := expected.Check(observation); err != nil {
		return nil, err
	}
	if len(manifest.Layers) != len(observation.Files) {
		return nil, invalid("OCI inventory membership differs")
	}
	for i, layer := range manifest.Layers {
		file := observation.Files[i]
		if err := validateDescriptor(layer, FileMediaType, file.Name, MaxFileBytes); err != nil {
			return nil, err
		}
		if layer.Digest.String() != file.Digest || layer.Size != file.Size {
			return nil, invalid("OCI descriptor differs from covered file")
		}
	}
	if err := authenticator.VerifyTestObservation(ctx, sign.Subject{Ref: reference, Digest: parsed.Reference}, rawObservation); err != nil {
		return nil, err
	}
	// No materialization occurs before schema/bindings/signature admission.
	workspace, err := os.MkdirTemp("", "nvkr-observation-consumer-*")
	if err != nil {
		return nil, registryError(ctx, err, "creating private observation workspace")
	}
	defer os.RemoveAll(workspace)
	if err := os.Chmod(workspace, 0o700); err != nil {
		return nil, registryError(ctx, err, "sealing observation workspace")
	}
	workspaceRoot, _, err := securepath.OpenDirectory(workspace)
	if err != nil {
		return nil, registryError(ctx, err, "holding observation workspace")
	}
	defer workspaceRoot.Close()
	if err := writeMember(ctx, workspaceRoot, ObservationFileName, rawObservation); err != nil {
		return nil, registryError(ctx, err, "writing authenticated observation")
	}
	for i, layer := range manifest.Layers {
		payload, err := fetchExact(ctx, repo, layer, MaxFileBytes)
		if err != nil {
			return nil, err
		}
		if err := writeMember(ctx, workspaceRoot, "files/"+observation.Files[i].Name, payload); err != nil {
			return nil, registryError(ctx, err, "writing authenticated observation member")
		}
	}
	bundle, err := Open(ctx, workspace)
	if err != nil {
		return nil, err
	}
	if err := expected.Check(bundle.Observation()); err != nil {
		_ = bundle.Close()
		return nil, err
	}
	if err := captureContext(ctx); err != nil {
		_ = bundle.Close()
		return nil, err
	}
	return &Verified{bundle: bundle, ctx: lifetime, reference: reference}, nil
}

func pinnedReference(reference string) (registry.Reference, error) {
	parsed, err := registry.ParseReference(reference)
	if err != nil || !canonical.IsDigest(parsed.Reference) || parsed.Registry+"/"+parsed.Repository+"@"+parsed.Reference != reference {
		return registry.Reference{}, e.New(e.CodeUnpinnedInput, "observation reference must be an exact canonical repository@sha256 digest")
	}
	return parsed, nil
}

func observationRepository(reference string, options delivery.RegistryOptions) (*remote.Repository, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	parsed, err := registry.ParseReference(reference)
	if err != nil || parsed.Registry+"/"+parsed.Repository != reference || parsed.Reference != "" {
		return nil, e.New(e.CodeUsage, "observation publication requires a canonical untagged repository")
	}
	if options.PlainHTTP {
		host := (&url.URL{Host: parsed.Registry}).Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, e.New(e.CodeUsage, "plain HTTP observation transport is restricted to explicit loopback fixtures")
		}
	}
	repo, err := remote.NewRepository(reference)
	if err != nil {
		return nil, e.Wrap(e.CodeUsage, err, "admitting observation repository")
	}
	repo.PlainHTTP = options.PlainHTTP
	scheme := "https"
	if options.PlainHTTP {
		scheme = "http"
	}
	client := &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != parsed.Registry || req.URL.Scheme != scheme || len(via) >= 5 {
			return e.New(e.CodeVerifyFailed, "observation registry redirect changes host")
		}
		return nil
	}}
	credential := auth.Credential{Username: options.Username, Password: options.Password}
	if !options.Anonymous && options.Password == "" {
		nameRepo, err := name.NewRepository(reference, name.StrictValidation)
		if err != nil {
			return nil, e.Wrap(e.CodeUsage, err, "admitting registry keychain host")
		}
		resolved, err := authn.DefaultKeychain.Resolve(nameRepo)
		if err != nil {
			return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "resolving observation registry credentials")
		}
		config, err := resolved.Authorization()
		if err != nil {
			return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "loading observation registry credentials")
		}
		credential = auth.Credential{Username: config.Username, Password: config.Password, RefreshToken: config.IdentityToken, AccessToken: config.RegistryToken}
	}
	repo.Client = &auth.Client{Client: client, Cache: auth.NewCache(), Credential: auth.StaticCredential(parsed.Registry, credential)}
	return repo, nil
}

func descriptor(mediaType string, raw []byte) oci.Descriptor {
	return oci.Descriptor{MediaType: mediaType, Digest: digest.Digest(blobDigest(raw)), Size: int64(len(raw))}
}

func validateDescriptor(desc oci.Descriptor, mediaType, title string, maxBytes int64) error {
	if desc.MediaType != mediaType || !canonical.IsDigest(desc.Digest.String()) || desc.Size < 0 || desc.Size > maxBytes || len(desc.URLs) != 0 || len(desc.Data) != 0 || desc.Platform != nil || desc.ArtifactType != "" {
		return invalid("unsupported or oversized observation descriptor")
	}
	if title == "" && len(desc.Annotations) != 0 || title != "" && (len(desc.Annotations) != 1 || desc.Annotations[oci.AnnotationTitle] != title) {
		return invalid("observation member name differs from inventory")
	}
	return nil
}

func fetchExact(ctx context.Context, repo *remote.Repository, desc oci.Descriptor, maxBytes int64) ([]byte, error) {
	if desc.Size < 0 || desc.Size > maxBytes || !canonical.IsDigest(desc.Digest.String()) {
		return nil, invalid("observation descriptor exceeds byte budget")
	}
	reader, err := repo.Fetch(ctx, desc)
	if err != nil {
		return nil, registryError(ctx, err, "fetching observation content")
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, desc.Size+1))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, registryError(ctx, err, "reading observation content")
	}
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	if int64(len(raw)) != desc.Size || blobDigest(raw) != desc.Digest.String() {
		return nil, invalid("observation content digest or length differs")
	}
	return raw, nil
}

func registryError(ctx context.Context, err error, operation string) error {
	if canceled := captureContext(ctx); canceled != nil {
		return canceled
	}
	return e.Wrap(e.CodeUnavailable, err, "%s", operation)
}

// VerifyLocator checks the locator only as a coordinate. Expected remains independent.
func VerifyLocator(ctx context.Context, l Locator, expected Expected, options RegistryOptions) (*Verified, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if l.ObservationDigest != expected.ObservationDigest {
		return nil, invalid("locator differs from independent observation expectation")
	}
	return VerifyOCI(ctx, l.Reference, expected, options)
}
