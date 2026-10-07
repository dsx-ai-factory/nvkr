// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const maxPinImageMetadataBytes = 8 << 20

type pinImageSource struct {
	index     v1.ImageIndex
	image     v1.Image
	transport *pinImageTransport
}

type pinImageReader func(context.Context, name.Reference) (pinImageSource, error)

func readPinImage(ctx context.Context, ref name.Reference) (pinImageSource, error) {
	return readPinImageWithTransport(ctx, ref, remote.DefaultTransport)
}

// The private seam lets registry contract tests retain the production HTTPS and
// metadata bounds without changing a process-global transport or ambient auth.
func readPinImageWithTransport(ctx context.Context, ref name.Reference, transport http.RoundTripper) (pinImageSource, error) {
	bounded := &pinImageTransport{RoundTripper: transport}
	d, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous),
		remote.WithTransport(bounded), remote.WithUserAgent("nkdctl/image-pin"))
	if err != nil {
		return pinImageSource{}, err
	}
	switch d.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		index, err := d.ImageIndex()
		return pinImageSource{index: index, transport: bounded}, err
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		image, err := d.Image()
		return pinImageSource{image: image, transport: bounded}, err
	default:
		return pinImageSource{}, fmt.Errorf("unsupported image manifest media type %q", d.MediaType)
	}
}

func resolveImageBindingWith(ctx context.Context, ref string, arches []string, read pinImageReader, observed *pinImageSelection) (map[string]string, error) {
	if ctx == nil || read == nil || len(arches) == 0 {
		return nil, e.New(e.CodeUsage, "image pin resolution requires a context, reader and architectures")
	}
	for _, arch := range arches {
		if !target.ValidArch(arch) {
			return nil, e.New(e.CodeUsage, "unsupported image pin architecture %q", arch)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	pins, err := selectPinImageDigestsAndParent(ctx, ref, arches, read, observed)
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "image pin resolution canceled")
	}
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "resolve native image pins for %s", ref)
	}
	return pins, nil
}

type pinImageSelection struct {
	parent    v1.Hash
	images    map[string]v1.Image
	transport *pinImageTransport
}

func selectPinImageDigestsAndParent(ctx context.Context, ref string, arches []string, read pinImageReader, observed *pinImageSelection) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := name.ParseReference(strings.TrimPrefix(ref, "oci://"))
	if err != nil {
		return nil, err
	}
	source, err := read(ctx, parsed)
	if err != nil {
		return nil, err
	}
	var parent v1.Hash
	images := make(map[string]v1.Image, len(arches))
	pins := make(map[string]string, len(arches))
	if source.index != nil && source.image == nil {
		raw, err := source.index.RawManifest()
		if err != nil {
			return nil, err
		}
		parent, err = pinMetadataDigest(raw)
		if err != nil {
			return nil, err
		}
		manifest, err := v1.ParseIndexManifest(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		media, err := source.index.MediaType()
		if err != nil {
			return nil, err
		}
		if (media != types.OCIImageIndex && media != types.DockerManifestList) || manifest.MediaType != media || manifest.SchemaVersion != 2 || manifest.ArtifactType != "" || manifest.Subject != nil {
			return nil, fmt.Errorf("unsupported native image index profile")
		}
		for _, arch := range arches {
			var selected *v1.Descriptor
			for i := range manifest.Manifests {
				candidate := &manifest.Manifests[i]
				if !pinNativePlatform(candidate.Platform, arch) {
					continue
				}
				if selected != nil {
					return nil, fmt.Errorf("ambiguous linux/%s image manifests", arch)
				}
				selected = candidate
			}
			if selected == nil {
				return nil, fmt.Errorf("image has no linux/%s native manifest", arch)
			}
			if !pinNativeMedia(selected.MediaType) || selected.Size <= 0 || selected.Size > maxPinImageMetadataBytes || (selected.ArtifactType != "" && !pinConfigMedia(types.MediaType(selected.ArtifactType))) || len(selected.URLs) != 0 {
				return nil, fmt.Errorf("unsupported linux/%s native descriptor", arch)
			}
			image, err := source.index.Image(selected.Digest)
			if err != nil {
				return nil, err
			}
			digest, size, media, err := authenticatePinImage(image, arch)
			if err != nil {
				return nil, err
			}
			if digest != selected.Digest || size != selected.Size || media != selected.MediaType {
				return nil, fmt.Errorf("linux/%s native manifest differs from index descriptor", arch)
			}
			if selected.ArtifactType != "" {
				raw, err := image.RawManifest()
				if err != nil {
					return nil, err
				}
				native, err := v1.ParseManifest(bytes.NewReader(raw))
				if err != nil || selected.ArtifactType != string(native.Config.MediaType) {
					return nil, fmt.Errorf("linux/%s descriptor artifact type differs from native config", arch)
				}
			}
			pins[arch] = digest.String()
			images[arch] = image
		}
	} else if source.image != nil && source.index == nil {
		for _, arch := range arches {
			digest, _, _, err := authenticatePinImage(source.image, arch)
			if err != nil {
				return nil, err
			}
			parent = digest
			images[arch] = source.image
			pins[arch] = digest.String()
		}
	} else {
		return nil, fmt.Errorf("image reader returned an ambiguous or empty source")
	}
	if requested, ok := parsed.(name.Digest); ok && requested.DigestStr() != parent.String() {
		return nil, fmt.Errorf("image bytes differ from requested digest")
	}
	if observed != nil {
		*observed = pinImageSelection{parent: parent, images: images, transport: source.transport}
	}
	return pins, nil
}

func authenticatePinImage(image v1.Image, arch string) (v1.Hash, int64, types.MediaType, error) {
	raw, err := image.RawManifest()
	if err != nil {
		return v1.Hash{}, 0, "", err
	}
	digest, err := pinMetadataDigest(raw)
	if err != nil {
		return v1.Hash{}, 0, "", err
	}
	manifest, err := v1.ParseManifest(bytes.NewReader(raw))
	if err != nil {
		return v1.Hash{}, 0, "", err
	}
	media, err := image.MediaType()
	if err != nil {
		return v1.Hash{}, 0, "", err
	}
	if !pinNativeMedia(media) || manifest.MediaType != media || manifest.SchemaVersion != 2 || manifest.ArtifactType != "" || manifest.Subject != nil || !pinConfigMedia(manifest.Config.MediaType) {
		return v1.Hash{}, 0, "", fmt.Errorf("unsupported native image profile")
	}
	config, err := image.RawConfigFile()
	if err != nil {
		return v1.Hash{}, 0, "", err
	}
	configDigest, err := pinMetadataDigest(config)
	if err != nil {
		return v1.Hash{}, 0, "", err
	}
	if configDigest != manifest.Config.Digest || int64(len(config)) != manifest.Config.Size {
		return v1.Hash{}, 0, "", fmt.Errorf("native image config differs from manifest descriptor")
	}
	parsedConfig, err := v1.ParseConfigFile(bytes.NewReader(config))
	if err != nil {
		return v1.Hash{}, 0, "", err
	}
	if !pinNativePlatform(parsedConfig.Platform(), arch) {
		return v1.Hash{}, 0, "", fmt.Errorf("native image config does not select linux/%s", arch)
	}
	return digest, int64(len(raw)), media, nil
}

func pinMetadataDigest(raw []byte) (v1.Hash, error) {
	if len(raw) == 0 || len(raw) > maxPinImageMetadataBytes {
		return v1.Hash{}, fmt.Errorf("image metadata exceeds %d bytes", maxPinImageMetadataBytes)
	}
	digest, _, err := v1.SHA256(bytes.NewReader(raw))
	return digest, err
}

func pinNativeMedia(media types.MediaType) bool {
	return media == types.OCIManifestSchema1 || media == types.DockerManifestSchema2
}

func pinConfigMedia(media types.MediaType) bool {
	return media == types.OCIConfigJSON || media == types.DockerConfigJSON
}

// OCI arm64 v8 is the baseline AArch64 spelling, not a separately supported
// target. Other variants or OS-specific constraints cannot alias a catalog pin.
func pinNativePlatform(platform *v1.Platform, arch string) bool {
	return platform != nil && platform.OS == "linux" && platform.Architecture == arch &&
		(platform.Variant == "" || (arch == "arm64" && platform.Variant == "v8")) &&
		platform.OSVersion == "" && len(platform.OSFeatures) == 0 && len(platform.Features) == 0
}

// Metadata is bounded before decoding. Only descriptors in the authenticated
// selected native manifest can widen a blob response, and GCR additionally wraps
// that compressed stream with its descriptor-size/hash verifier.
type pinImageTransport struct {
	http.RoundTripper
	mu        sync.Mutex
	blobs     map[string]int64
	redirects map[string]int64
}

func (transport *pinImageTransport) admitImageLayers(image v1.Image) error {
	raw, err := image.RawManifest()
	if err != nil {
		return err
	}
	manifest, err := v1.ParseManifest(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	bounds := make(map[string]int64, len(manifest.Layers))
	for _, layer := range manifest.Layers {
		// Foreign URLs are not part of this registry-owned acquisition profile.
		if layer.Size < 0 || !canonical.IsDigest(layer.Digest.String()) || len(layer.URLs) != 0 ||
			!(layer.MediaType == types.OCILayer || layer.MediaType == types.OCIUncompressedLayer ||
				layer.MediaType == types.OCILayerZStd || layer.MediaType == types.DockerLayer || layer.MediaType == types.DockerUncompressedLayer) {
			return fmt.Errorf("unsupported selected image layer descriptor")
		}
		if previous, exists := bounds[layer.Digest.String()]; exists && previous != layer.Size {
			return fmt.Errorf("conflicting selected layer size")
		}
		bounds[layer.Digest.String()] = layer.Size
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.blobs = bounds
	transport.redirects = make(map[string]int64)
	return nil
}

func (transport *pinImageTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateHelmHTTPS(request.URL.String()); err != nil {
		return nil, err
	}
	limit := int64(maxPinImageMetadataBytes)
	transport.mu.Lock()
	selected := false
	if bound, ok := transport.redirects[request.URL.String()]; ok {
		limit, selected = bound, true
	} else if at := strings.LastIndex(request.URL.Path, "/blobs/"); at >= 0 {
		if bound, ok := transport.blobs[request.URL.Path[at+len("/blobs/"):]]; ok {
			limit, selected = bound, true
		}
	}
	transport.mu.Unlock()
	response, err := transport.RoundTripper.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	// Redirect responses are metadata. The destination only receives the blob
	// allowance when it is a validated HTTPS continuation of this exact request.
	if selected && response.StatusCode >= 300 && response.StatusCode < 400 {
		if location, err := request.URL.Parse(response.Header.Get("Location")); err == nil && response.Header.Get("Location") != "" {
			if err := validateHelmHTTPS(location.String()); err != nil {
				_ = response.Body.Close()
				return nil, err
			}
			transport.mu.Lock()
			previous, exists := transport.redirects[location.String()]
			if exists && previous != limit {
				transport.mu.Unlock()
				_ = response.Body.Close()
				return nil, fmt.Errorf("ambiguous selected blob redirect")
			}
			transport.redirects[location.String()] = limit
			transport.mu.Unlock()
		}
		limit = maxPinImageMetadataBytes
	}
	label := "image pin metadata"
	if selected && limit != maxPinImageMetadataBytes {
		label = "authenticated image layer"
	}
	if response.ContentLength > limit {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%s response exceeds size bound", label)
	}
	response.Body = &helmBoundedBody{ReadCloser: response.Body, remaining: limit, label: label}
	return response, nil
}
