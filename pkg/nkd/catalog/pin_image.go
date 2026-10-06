package catalog

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	index v1.ImageIndex
	image v1.Image
}

type pinImageReader func(context.Context, name.Reference) (pinImageSource, error)

// The runtime argument remains source-compatible with the maintenance API. Image
// pins use registry-native bytes, not a daemon's manifest JSON projection.
func resolveImageDigests(ctx context.Context, runtime string, ref string, arches []string) (map[string]string, error) {
	if err := validateAcquiredImageReference("image pin", ref); err != nil {
		return nil, err
	}
	if err := validatePinRuntime(runtime); err != nil {
		return nil, err
	}
	return resolveImageDigestsWith(ctx, ref, arches, readPinImage)
}

func readPinImage(ctx context.Context, ref name.Reference) (pinImageSource, error) {
	return readPinImageWithTransport(ctx, ref, remote.DefaultTransport)
}

// The private seam lets registry contract tests retain the production HTTPS and
// metadata bounds without changing a process-global transport or ambient auth.
func readPinImageWithTransport(ctx context.Context, ref name.Reference, transport http.RoundTripper) (pinImageSource, error) {
	d, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous),
		remote.WithTransport(pinImageTransport{transport}), remote.WithUserAgent("nkdctl/image-pin"))
	if err != nil {
		return pinImageSource{}, err
	}
	switch d.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		index, err := d.ImageIndex()
		return pinImageSource{index: index}, err
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		image, err := d.Image()
		return pinImageSource{image: image}, err
	default:
		return pinImageSource{}, fmt.Errorf("unsupported image manifest media type %q", d.MediaType)
	}
}

func resolveImageDigestsWith(ctx context.Context, ref string, arches []string, read pinImageReader) (map[string]string, error) {
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
	pins, err := selectPinImageDigests(ctx, ref, arches, read)
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "image pin resolution canceled")
	}
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "resolve native image pins for %s", ref)
	}
	return pins, nil
}

func selectPinImageDigests(ctx context.Context, ref string, arches []string, read pinImageReader) (map[string]string, error) {
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
		}
	} else if source.image != nil && source.index == nil {
		for _, arch := range arches {
			digest, _, _, err := authenticatePinImage(source.image, arch)
			if err != nil {
				return nil, err
			}
			parent = digest
			pins[arch] = digest.String()
		}
	} else {
		return nil, fmt.Errorf("image reader returned an ambiguous or empty source")
	}
	if requested, ok := parsed.(name.Digest); ok && requested.DigestStr() != parent.String() {
		return nil, fmt.Errorf("image bytes differ from requested digest")
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

type pinImageTransport struct{ http.RoundTripper }

func (transport pinImageTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateHelmHTTPS(request.URL.String()); err != nil {
		return nil, err
	}
	response, err := transport.RoundTripper.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > maxPinImageMetadataBytes {
		_ = response.Body.Close()
		return nil, fmt.Errorf("image pin metadata response exceeds size bound")
	}
	response.Body = &helmBoundedBody{ReadCloser: response.Body, remaining: maxPinImageMetadataBytes}
	return response, nil
}
