// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// PullMetadata fetches bounded manifests, configs and evidence, never output
// layers. Its result is an integrity description, not a verified publication.
func PullMetadata(ctx context.Context, reference, out string, options RegistryOptions) (Inspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return Inspection{}, err
	}
	if err := options.Validate(); err != nil {
		return Inspection{}, err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil {
		return Inspection{}, e.Wrap(e.CodeUnpinnedSource, err, "parse metadata reference")
	}
	remoteOptions := registryOptions(ctx, options)
	// Reuse repository authentication only within this complete metadata pull.
	// Independent calls retain independent credentials, contexts and admission.
	puller, err := remote.NewPuller(remoteOptions...)
	if err != nil {
		return Inspection{}, metadataFetchError(ctx, err, "create metadata puller")
	}
	fetched, err := puller.Get(ctx, ref)
	if err != nil {
		return Inspection{}, metadataFetchError(ctx, err, "fetch metadata index")
	}
	if len(fetched.Manifest) > 8<<20 || digestBytes(fetched.Manifest) != ref.DigestStr() {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata index identity/size differs")
	}
	var index ociIndex
	if err := json.Unmarshal(fetched.Manifest, &index); err != nil || index.ArtifactType != MediaPublicationRoot || len(index.Manifests) != 1 {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata index must contain one root")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Inspection{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	var total int64
	put := func(raw []byte, d Descriptor, max int64) error {
		total += int64(len(raw))
		if int64(len(raw)) > max || total > maxMetadataBytes || d.Size != int64(len(raw)) || digestBytes(raw) != d.Digest {
			return e.New(e.CodeVerifyFailed, "metadata object exceeds bounds or changes identity")
		}
		_, err := writer.addBlobBytes(raw, d.MediaType)
		return err
	}
	manifest := func(repo name.Repository, d Descriptor) (ociManifest, error) {
		got, err := puller.Get(ctx, repo.Digest(d.Digest))
		if err != nil {
			return ociManifest{}, metadataFetchError(ctx, err, "fetch metadata manifest")
		}
		if err := put(got.Manifest, d, 8<<20); err != nil {
			return ociManifest{}, err
		}
		var m ociManifest
		if err := json.Unmarshal(got.Manifest, &m); err != nil {
			return m, e.Wrap(e.CodeVerifyFailed, err, "decode metadata manifest")
		}
		return m, nil
	}
	blob := func(repo name.Repository, d Descriptor, max int64) error {
		if d.Size < 0 || d.Size > max {
			return e.New(e.CodeVerifyFailed, "oversized metadata blob")
		}
		layer, err := puller.Layer(ctx, repo.Digest(d.Digest))
		if err != nil {
			return metadataFetchError(ctx, err, "fetch metadata blob")
		}
		reader, err := layer.Compressed()
		if err != nil {
			return metadataFetchError(ctx, err, "open metadata blob")
		}
		defer reader.Close()
		raw, err := io.ReadAll(io.LimitReader(reader, max+1))
		if err != nil {
			return metadataFetchError(ctx, err, "read metadata blob")
		}
		return put(raw, d, max)
	}
	m, err := manifest(ref.Context(), index.Manifests[0])
	if err != nil {
		return Inspection{}, err
	}
	if m.ArtifactType != MediaPublicationRoot || len(m.Layers) != 0 || m.Config.MediaType != MediaPublicationRootConfig {
		return Inspection{}, e.New(e.CodeVerifyFailed, "invalid metadata root")
	}
	if err := blob(ref.Context(), m.Config, 8<<20); err != nil {
		return Inspection{}, err
	}
	config, err := readBlob(ctx, stage, m.Config, 8<<20)
	if err != nil {
		return Inspection{}, err
	}
	var root PublicationRoot
	if err := canonical.UnmarshalExact(config, &root); err != nil {
		return Inspection{}, e.Wrap(e.CodeVerifyFailed, err, "decode metadata root")
	}
	if err := root.Validate(); err != nil {
		return Inspection{}, err
	}
	if root.Release == nil {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata release context missing")
	}
	refs, err := publicationReferences(ref, root, options.PlainHTTP)
	if err != nil {
		return Inspection{}, err
	}
	for _, o := range root.Objects {
		child, ok := refs[o.Name]
		if !ok {
			return Inspection{}, e.New(e.CodeVerifyFailed, "metadata member has no repository")
		}
		d := Descriptor{MediaType: publishedManifestMediaType(o), Digest: o.DescriptorDigest, Size: o.Size, ArtifactType: o.ArtifactType}
		m, err := manifest(child.Context(), d)
		if err != nil {
			return Inspection{}, err
		}
		if o.Output != nil && o.Output.ImageIndex != nil {
			// Fetch only the bounded children named by the release binding.
			// InspectMetadata subsequently matches them to the exact index bytes.
			for _, native := range o.Output.ImageIndex.Children {
				descriptor := Descriptor{MediaType: MediaOCIManifest, Digest: native.ManifestDigest, Size: native.Size}
				image, err := manifest(child.Context(), descriptor)
				if err != nil {
					return Inspection{}, err
				}
				if err := blob(child.Context(), image.Config, 8<<20); err != nil {
					return Inspection{}, err
				}
			}
			continue
		}
		if err := blob(child.Context(), m.Config, 8<<20); err != nil {
			return Inspection{}, err
		}
		if o.Evidence != nil {
			if len(m.Layers) != 1 {
				return Inspection{}, e.New(e.CodeVerifyFailed, "evidence requires one bounded layer")
			}
			if err := blob(child.Context(), m.Layers[0], sbom.MaxBundleBytes); err != nil {
				return Inspection{}, err
			}
		}
	}
	got, err := writer.writeIndex(index)
	if err != nil {
		return Inspection{}, err
	}
	if got != ref.DigestStr() {
		return Inspection{}, e.New(e.CodeVerifyFailed, "noncanonical metadata index")
	}
	inspection, err := InspectMetadata(ctx, stage)
	if err != nil {
		return Inspection{}, err
	}
	if err := commit(); err != nil {
		return Inspection{}, err
	}
	return inspection, nil
}

// metadataFetchError preserves ADR-03 retryability without treating certificate
// or checksum/size verification errors or definitive registry rejections as outages.
func metadataFetchError(ctx context.Context, err error, operation string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", operation)
	}
	// url.Error reports net.Error even for permanent protocol or request-policy
	// failures. Classify its underlying network cause, retaining the full error
	// chain for all other checks and for the returned diagnostic.
	networkCause := err
	var requestErr *url.Error
	for errors.As(networkCause, &requestErr) {
		networkCause = requestErr.Err
	}
	code := e.CodeFetchFailed
	var coded *e.Error
	var registryErr *transport.Error
	var certificateErr *tls.CertificateVerificationError
	var invalidCertificateErr x509.CertificateInvalidError
	var hostnameErr x509.HostnameError
	var unknownAuthorityErr x509.UnknownAuthorityError
	var systemRootsErr x509.SystemRootsError
	var networkErr net.Error
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = e.CodeTimeout
	case errors.As(err, &coded):
		code = coded.Code
	case errors.As(err, &certificateErr), errors.As(err, &invalidCertificateErr),
		errors.As(err, &hostnameErr), errors.As(err, &unknownAuthorityErr), errors.As(err, &systemRootsErr):
		// HTTP's url.Error also implements net.Error for deterministic trust
		// failures. Keep these nonretryable, including direct X.509 errors
		// returned by TLS verification callbacks.
		code = e.CodeFetchFailed
	case errors.As(err, &registryErr):
		if registryErr.StatusCode >= 500 && registryErr.StatusCode < 600 ||
			registryErr.StatusCode == http.StatusRequestTimeout || registryErr.StatusCode == http.StatusTooManyRequests {
			code = e.CodeUnavailable
		}
	case errors.As(networkCause, &networkErr):
		code = e.CodeUnavailable
		if networkErr.Timeout() {
			code = e.CodeTimeout
		}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		code = e.CodeUnavailable
	}
	return e.Wrap(code, err, "%s", operation)
}

// PushMetadata publishes only the release index/config after checking every
// referenced manifest exists. Caller-owned signature/provenance gates authorize
// release assembly; manifest existence alone is not acceptance.
func PushMetadata(ctx context.Context, source, repository string, options RegistryOptions) (string, error) {
	return PushMetadataWithOptions(ctx, source, repository, PushOptions{Registry: options})
}

// PushMetadataWithOptions binds optional prior metadata admission to the exact
// root being published. Metadata publication cannot advance payload aliases.
func PushMetadataWithOptions(ctx context.Context, source, repository string, request PushOptions) (string, error) {
	options := request.Registry
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	if err := request.Validate(); err != nil {
		return "", err
	}
	if len(request.ImageTags) != 0 {
		return "", e.New(e.CodeUsage, "metadata publication cannot advance image tags")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
	}
	inspection, err := InspectMetadata(ctx, source)
	if err != nil {
		return "", err
	}
	if request.ExpectedRootDigest != "" && inspection.RootDigest != request.ExpectedRootDigest {
		return "", e.New(e.CodeVerifyFailed, "publication root changed after metadata admission")
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	repo, err := name.NewRepository(repository, parse...)
	if err != nil || repo.Name() != repository {
		return "", e.New(e.CodeUsage, "metadata publication requires canonical repository")
	}
	ref := repo.Digest(inspection.RootDigest)
	refs, err := publicationReferences(ref, inspection.Root, options.PlainHTTP)
	if err != nil {
		return "", err
	}
	for _, o := range inspection.Root.Objects {
		child, ok := refs[o.Name]
		if !ok {
			return "", e.New(e.CodeVerifyFailed, "metadata member lacks repository")
		}
		if _, err := ResolveDigest(ctx, child.Name(), options); err != nil {
			return "", err
		}
	}
	path, err := layout.FromPath(source)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "open metadata layout")
	}
	index, err := path.ImageIndex()
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "open metadata index")
	}
	digest, err := index.Digest()
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "digest metadata index")
	}
	if digest.String() != inspection.RootDigest {
		return "", e.New(e.CodeVerifyFailed, "metadata index changed after membership inspection")
	}
	if err := remote.WriteIndex(ref, index, registryOptions(ctx, options)...); err != nil {
		return "", classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "publish metadata root"))
	}
	return ref.Name(), nil
}
