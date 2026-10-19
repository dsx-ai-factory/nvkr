// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const (
	// Discovery includes unrelated evidence and historical attempts. It has a
	// separate bound from the 32 candidates admitted for each required type.
	maxOCIDiscoveryCandidates       = 256
	maxOCIReferrersBytes      int64 = 4 << 20
	maxOCIManifestBytes       int64 = 1 << 20
	maxOCIBundleSearchBytes   int64 = 64 << 20
	ociPredicateAnnotation          = "dev.sigstore.bundle.predicateType"
	ociEmptyConfigMediaType         = "application/vnd.oci.empty.v1+json"
)

type ociBundleResult struct {
	raw []byte
	err error
}

// ociBundleSource owns one operation's descriptive discovery and exact raw
// bytes. It grants no authority; claims and pinned cosign authenticate every
// accepted bundle separately. Failed candidates are cached too, so another
// evidence type cannot turn the same operation into a second registry scan.
type ociBundleSource struct {
	descriptors []v1.Descriptor
	load        func(context.Context, v1.Descriptor) ([]byte, error)
	cache       map[string]ociBundleResult
	bytes       int64
	close       func()
	check       func() error
}

func (s *ociBundleSource) failure() error {
	if s.check != nil {
		if err := s.check(); err != nil {
			return e.Wrap(e.CodeSignatureInvalid, err, "OCI verification transport failed")
		}
	}
	return nil
}

func (s *ociBundleSource) candidates(predicateType string) []v1.Descriptor {
	out := append([]v1.Descriptor(nil), s.descriptors...)
	sort.Slice(out, func(i, j int) bool {
		iHint := out[i].Annotations[ociPredicateAnnotation] == predicateType
		jHint := out[j].Annotations[ociPredicateAnnotation] == predicateType
		if iHint != jHint {
			return iHint
		}
		return out[i].Digest.String() < out[j].Digest.String()
	})
	return out
}

func (s *ociBundleSource) bundle(ctx context.Context, descriptor v1.Descriptor) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := s.failure(); err != nil {
		return nil, err
	}
	key := descriptor.Digest.String()
	if result, ok := s.cache[key]; ok {
		return result.raw, result.err
	}
	raw, err := s.load(ctx, descriptor)
	if failure := s.failure(); failure != nil {
		return nil, failure
	}
	if err == nil && (int64(len(raw)) > maxCosignOutputBytes || s.bytes+int64(len(raw)) > maxOCIBundleSearchBytes) {
		err = e.New(e.CodeSignatureInvalid, "OCI bundle discovery exceeds byte limit")
	}
	if err != nil {
		raw = nil
	} else {
		s.bytes += int64(len(raw))
	}
	if s.cache == nil {
		s.cache = make(map[string]ociBundleResult)
	}
	s.cache[key] = ociBundleResult{raw: raw, err: err}
	return raw, err
}

// Registry credentials are scoped before any I/O. An empty credential is
// explicitly anonymous; neither a Docker keychain nor an ambient helper runs.
func admitOCIRegistry(subject Subject, credential Credential) (name.Digest, error) {
	if err := subject.Validate(); err != nil {
		return name.Digest{}, err
	}
	if err := credential.validate(); err != nil {
		return name.Digest{}, err
	}
	ref, err := name.NewDigest(subject.Ref, name.StrictValidation)
	if err != nil || ref.Name() != subject.Ref {
		return name.Digest{}, e.New(e.CodeUnverifiedRef, "OCI bundle discovery requires a canonical digest reference")
	}
	if !credential.empty() && credential.Registry != ref.Context().RegistryStr() {
		return name.Digest{}, e.New(e.CodeUsage, "registry credential does not match the OCI subject registry")
	}
	return ref, nil
}

func newRegistryBundleSource(ctx context.Context, subject Subject, credential Credential) (*ociBundleSource, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	ref, err := admitOCIRegistry(subject, credential)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	budget := &ociReadBudget{remaining: maxOCIBundleSearchBytes}
	source := &ociBundleSource{close: base.CloseIdleConnections, check: budget.failure}
	auth := authn.Anonymous
	if !credential.empty() {
		auth = authn.FromConfig(authn.AuthConfig{Username: credential.Username, Password: credential.Password})
	}
	puller, err := remote.NewPuller(remote.WithContext(ctx), remote.WithAuth(auth),
		remote.WithTransport(ociBundleTransport{base: base, budget: budget}), remote.WithUserAgent("nvkr/sign"),
		// This is the standard OCI referrers tag schema, never cosign's legacy
		// .sig or .att attachment lookup.
		remote.WithReferrersTagFallback(true))
	if err != nil {
		source.close()
		return nil, ociBundleFetchError(ctx, err)
	}
	index, err := remote.Referrers(ref, remote.Reuse(puller), remote.WithContext(ctx))
	if closeErr := budget.failure(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		source.close()
		return nil, ociBundleFetchError(ctx, err)
	}
	raw, err := index.RawManifest()
	if err == nil {
		err = canonical.ValidateJSONDocument(raw)
	}
	var manifest v1.IndexManifest
	if err == nil {
		err = json.Unmarshal(raw, &manifest)
	}
	if err != nil || int64(len(raw)) > maxOCIReferrersBytes || manifest.SchemaVersion != 2 || manifest.MediaType != types.OCIImageIndex || len(manifest.Manifests) > maxOCIDiscoveryCandidates {
		source.close()
		return nil, e.New(e.CodeSignatureInvalid, "invalid or oversized OCI referrers index")
	}
	seen := make(map[string]bool)
	for _, descriptor := range manifest.Manifests {
		if err := admitOCIBundleDescriptor(descriptor, maxOCIManifestBytes); err != nil {
			source.close()
			return nil, err
		}
		if seen[descriptor.Digest.String()] {
			source.close()
			return nil, e.New(e.CodeSignatureInvalid, "duplicate OCI referrer descriptor")
		}
		seen[descriptor.Digest.String()] = true
		// Artifact type is descriptive filtering of the OCI profile, not proof
		// of a predicate or signer. Every selected manifest is checked again.
		if descriptor.ArtifactType == sbom.BundleMediaType && descriptor.MediaType == types.OCIManifestSchema1 {
			source.descriptors = append(source.descriptors, descriptor)
		}
	}
	source.load = func(ctx context.Context, descriptor v1.Descriptor) ([]byte, error) {
		raw, err := loadRegistryBundle(ctx, puller, ref, descriptor)
		if closeErr := budget.failure(); closeErr != nil {
			return nil, ociBundleFetchError(ctx, errors.Join(err, closeErr))
		}
		return raw, err
	}
	return source, nil
}

func admitOCIBundleDescriptor(descriptor v1.Descriptor, limit int64) error {
	if descriptor.MediaType == "" || !canonical.IsDigest(descriptor.Digest.String()) || descriptor.Size <= 0 || descriptor.Size > limit || len(descriptor.URLs) != 0 || len(descriptor.Data) != 0 {
		return e.New(e.CodeSignatureInvalid, "invalid OCI bundle descriptor")
	}
	return nil
}

func loadRegistryBundle(ctx context.Context, puller *remote.Puller, subject name.Digest, descriptor v1.Descriptor) ([]byte, error) {
	if err := admitOCIBundleDescriptor(descriptor, maxOCIManifestBytes); err != nil {
		return nil, err
	}
	fetched, err := puller.Get(ctx, subject.Context().Digest(descriptor.Digest.String()))
	if err != nil {
		return nil, ociBundleFetchError(ctx, err)
	}
	raw := fetched.Manifest
	if int64(len(raw)) != descriptor.Size || digestBytes(raw) != descriptor.Digest.String() {
		return nil, e.New(e.CodeSignatureInvalid, "OCI bundle manifest does not match its descriptor")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "invalid OCI bundle manifest JSON")
	}
	var manifest v1.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "decoding OCI bundle manifest")
	}
	config := manifest.Config
	if manifest.SchemaVersion != 2 || manifest.MediaType != types.OCIManifestSchema1 || manifest.ArtifactType != sbom.BundleMediaType || len(manifest.Layers) != 1 ||
		manifest.Subject == nil || manifest.Subject.Digest.String() != subject.DigestStr() {
		return nil, e.New(e.CodeSignatureInvalid, "OCI bundle manifest has an invalid profile or subject")
	}
	if err := admitOCIBundleDescriptor(*manifest.Subject, 1<<63-1); err != nil {
		return nil, err
	}
	if config.MediaType != ociEmptyConfigMediaType || config.Digest.String() != digestBytes([]byte("{}")) || config.Size != 2 || len(config.URLs) != 0 || (config.ArtifactType != "" && config.ArtifactType != sbom.BundleMediaType) || (len(config.Data) != 0 && !bytes.Equal(config.Data, []byte("{}"))) {
		return nil, e.New(e.CodeSignatureInvalid, "OCI bundle manifest requires the standard empty config")
	}
	layer := manifest.Layers[0]
	if err := admitOCIBundleDescriptor(layer, maxCosignOutputBytes); err != nil {
		return nil, err
	}
	if layer.MediaType != sbom.BundleMediaType {
		return nil, e.New(e.CodeSignatureInvalid, "OCI bundle layer has an unsupported media type")
	}
	blob, err := puller.Layer(ctx, subject.Context().Digest(layer.Digest.String()))
	if err != nil {
		return nil, ociBundleFetchError(ctx, err)
	}
	reader, err := blob.Compressed()
	if err != nil {
		return nil, ociBundleFetchError(ctx, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, layer.Size+1))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, ociBundleFetchError(ctx, err)
	}
	if int64(len(raw)) != layer.Size || digestBytes(raw) != layer.Digest.String() {
		return nil, e.New(e.CodeSignatureInvalid, "OCI bundle layer does not match its descriptor")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "invalid OCI bundle JSON")
	}
	return raw, nil
}

func ociBundleFetchError(ctx context.Context, err error) error {
	if ctxErr := contextError(ctx); ctxErr != nil {
		return ctxErr
	}
	return e.Wrap(e.CodeSignatureInvalid, err, "reading OCI verification bundle")
}

// The library's manifest limit is larger than this owner's metadata contract.
// Limit response bodies before it buffers them, and refuse pagination instead
// of silently accepting a partial discovery index (the library does not follow
// referrers Link headers). Blob redirects retain the library's auth scoping.
type ociBundleTransport struct {
	base   http.RoundTripper
	budget *ociReadBudget
}

type ociReadBudget struct {
	mu        sync.Mutex
	remaining int64
	closeErr  error
	limitErr  error
}

func (b *ociReadBudget) failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return errors.Join(b.closeErr, b.limitErr)
}

func (t ociBundleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.budget.mu.Lock()
	exhausted := t.budget.remaining <= 0
	if exhausted && t.budget.limitErr == nil {
		t.budget.limitErr = fmt.Errorf("OCI verification discovery exceeds total byte limit")
	}
	t.budget.mu.Unlock()
	if exhausted {
		return nil, fmt.Errorf("OCI verification discovery exceeds total byte limit")
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	limit := maxCosignOutputBytes
	var refusal error
	switch {
	case strings.Contains(request.URL.Path, "/referrers/"):
		limit = maxOCIReferrersBytes
		if response.Header.Get("Link") != "" {
			refusal = fmt.Errorf("paginated OCI referrers discovery is unsupported")
		}
	case strings.Contains(request.URL.Path, "/manifests/"):
		limit = maxOCIReferrersBytes
		if strings.Contains(request.URL.Path, "/manifests/sha256:") {
			limit = maxOCIManifestBytes
		}
	}
	// Track cleanup even when headers refuse the body before its first read.
	response.Body = &ociBoundedBody{ReadCloser: response.Body, remaining: limit, budget: t.budget}
	if response.ContentLength > limit {
		refusal = fmt.Errorf("OCI verification response exceeds byte limit")
	}
	if refusal != nil {
		return nil, errors.Join(refusal, response.Body.Close())
	}
	return response, nil
}

type ociBoundedBody struct {
	io.ReadCloser
	remaining int64
	budget    *ociReadBudget
	closeOnce sync.Once
	closeErr  error
}

func (b *ociBoundedBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		if b.closeErr != nil {
			b.budget.mu.Lock()
			b.budget.closeErr = errors.Join(b.budget.closeErr, b.closeErr)
			b.budget.mu.Unlock()
		}
	})
	return b.closeErr
}

func (b *ociBoundedBody) Read(p []byte) (int, error) {
	// Charge every read, including invalid candidates and failed/truncated
	// responses. The shared budget is owned by this operation, never global.
	b.budget.mu.Lock()
	defer b.budget.mu.Unlock()
	if b.remaining < 0 || b.budget.remaining < 0 {
		return 0, fmt.Errorf("OCI verification response exceeds byte limit")
	}
	remaining := min(b.remaining, b.budget.remaining)
	if int64(len(p)) > remaining+1 {
		p = p[:remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	b.budget.remaining -= int64(n)
	if b.budget.remaining < 0 && b.budget.limitErr == nil {
		b.budget.limitErr = fmt.Errorf("OCI verification discovery exceeds total byte limit")
	}
	if b.remaining < 0 || b.budget.remaining < 0 {
		return n, fmt.Errorf("OCI verification response exceeds byte limit")
	}
	return n, err
}
