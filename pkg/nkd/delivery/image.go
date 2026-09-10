package delivery

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const (
	MediaImageConfig    = "application/vnd.oci.image.config.v1+json"
	MediaImageLayer     = "application/vnd.oci.image.layer.v1.tar"
	MediaImageLayerGzip = "application/vnd.oci.image.layer.v1.tar+gzip"
)

// ImageInspection describes a verified single-platform runnable image. It is
// descriptive evidence, not a release authorization or reproduction receipt.
// ManifestDigest is the pullable image identity; RootDigest identifies its layout index.
type ImageInspection struct {
	RootDigest     string
	ManifestDigest string
	OS             string
	Arch           string
	Entrypoint     []string
	Command        []string
}

type imageConfig struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Config       struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
	} `json:"config"`
	RootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

// InspectPublishedContainerImage verifies a publication and its selected native
// image. The platform and exact nested index are rejoined to the publication
// binding; no extraction, registry lookup or image execution occurs here.
func InspectPublishedContainerImage(ctx context.Context, root, name string) (ImageInspection, error) {
	publication, err := Inspect(ctx, root)
	if err != nil {
		return ImageInspection{}, err
	}
	var object PublishedObject
	for _, candidate := range publication.Root.Objects {
		if candidate.Name == name {
			if object.Name != "" {
				return ImageInspection{}, e.New(e.CodeVerifyFailed, "ambiguous published image %q", name)
			}
			object = candidate
		}
	}
	if object.Name == "" || object.ArtifactType != MediaOCIManifest || object.OS != "linux" ||
		(object.Arch != "amd64" && object.Arch != "arm64") || object.BlobDigest != object.DescriptorDigest {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "publication has no exact native image binding for %q", name)
	}
	raw, err := readBlob(ctx, root, Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return ImageInspection{}, err
	}
	var index ociIndex
	if err := json.Unmarshal(raw, &index); err != nil || index.SchemaVersion != 2 || index.MediaType != MediaOCIIndex ||
		index.ArtifactType != "" || len(index.Manifests) != 1 || index.Manifests[0].MediaType != MediaOCIManifest {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "published image %q has an ambiguous index", name)
	}
	descriptor := index.Manifests[0]
	manifestRaw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return ImageInspection{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return ImageInspection{}, e.Wrap(e.CodeVerifyFailed, err, "decoding published image manifest")
	}
	config, err := verifyImageManifest(ctx, root, manifest, map[string]bool{})
	if err != nil {
		return ImageInspection{}, err
	}
	if config.OS != object.OS || config.Architecture != object.Arch {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "published image platform differs from its binding")
	}
	return ImageInspection{RootDigest: object.DescriptorDigest, ManifestDigest: descriptor.Digest,
		OS: config.OS, Arch: config.Architecture, Entrypoint: config.Config.Entrypoint, Command: config.Config.Cmd}, nil
}

// InspectContainerImage checks the exact manifest/config/layer closure and each
// uncompressed layer identity. Only Linux amd64/arm64 images with one manifest
// are admitted; an index containing attestations or ambiguous platforms is not
// silently reduced to its first entry. No layer is extracted or executed.
func InspectContainerImage(ctx context.Context, root, platform string) (ImageInspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return ImageInspection{}, err
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return ImageInspection{}, e.New(e.CodeEngineCapability, "container image platform %q is unsupported", platform)
	}
	if err := verifyImageLayoutMarker(ctx, root); err != nil {
		return ImageInspection{}, err
	}
	index, raw, err := readIndex(ctx, root)
	if err != nil {
		return ImageInspection{}, err
	}
	if index.ArtifactType != "" || len(index.Manifests) != 1 || index.Manifests[0].MediaType != MediaOCIManifest {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "container image requires exactly one OCI image manifest")
	}
	descriptor := index.Manifests[0]
	manifestRaw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return ImageInspection{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return ImageInspection{}, e.Wrap(e.CodeVerifyFailed, err, "decoding container image manifest")
	}
	visited := map[string]bool{descriptor.Digest: true}
	config, err := verifyImageManifest(ctx, root, manifest, visited)
	if err != nil {
		return ImageInspection{}, err
	}
	if config.OS+"/"+config.Architecture != platform {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "container image platform differs from %s", platform)
	}
	if err := verifyBlobInventory(root, visited); err != nil {
		return ImageInspection{}, err
	}
	return ImageInspection{RootDigest: digestBytes(raw), ManifestDigest: descriptor.Digest,
		OS: config.OS, Arch: config.Architecture, Entrypoint: config.Config.Entrypoint, Command: config.Config.Cmd}, nil
}

func verifyImageManifest(ctx context.Context, root string, manifest ociManifest, visited map[string]bool) (imageConfig, error) {
	var config imageConfig
	if manifest.SchemaVersion != 2 || manifest.MediaType != MediaOCIManifest || manifest.ArtifactType != "" ||
		manifest.Config.MediaType != MediaImageConfig || len(manifest.Layers) == 0 || len(manifest.Layers) > maxDeliveryObjects {
		return config, e.New(e.CodeVerifyFailed, "container image manifest has an invalid schema or layer count")
	}
	raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, e.Wrap(e.CodeVerifyFailed, err, "decoding container image config")
	}
	if config.OS != "linux" || (config.Architecture != "amd64" && config.Architecture != "arm64") ||
		config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(manifest.Layers) ||
		(len(config.Config.Entrypoint) == 0 && len(config.Config.Cmd) == 0) {
		return config, e.New(e.CodeVerifyFailed, "container image config has an invalid platform, rootfs or command")
	}
	visited[manifest.Config.Digest] = true
	for i, layer := range manifest.Layers {
		if err := verifyImageLayer(ctx, root, layer, config.RootFS.DiffIDs[i]); err != nil {
			return config, err
		}
		visited[layer.Digest] = true
	}
	return config, nil
}

func verifyImageLayer(ctx context.Context, root string, layer Descriptor, diffID string) error {
	if !isDigest(layer.Digest) || !isDigest(diffID) || layer.Size < 0 || layer.Size > maxDeliveryFileBytes ||
		(layer.MediaType != MediaImageLayer && layer.MediaType != MediaImageLayerGzip) {
		return e.New(e.CodeVerifyFailed, "container image layer has an invalid identity, size or media type")
	}
	held, err := heldfile.OpenContext(ctx, filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(layer.Digest, "sha256:")),
		heldfile.Options{Label: "container-image-layer", MaxBytes: maxDeliveryFileBytes})
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding container image layer"))
	}
	defer func() { _ = held.Close() }()
	if held.Digest() != layer.Digest || held.Size() != layer.Size {
		return e.New(e.CodeVerifyFailed, "container image layer does not match its descriptor")
	}
	var reader io.Reader = io.NewSectionReader(held, 0, held.Size())
	if layer.MediaType == MediaImageLayerGzip {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding container image layer")
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	}
	hash := sha256.New()
	reader = io.LimitReader(reader, maxDeliveryFileBytes+1)
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		n, readErr := reader.Read(buffer)
		total += int64(n)
		if total > maxDeliveryFileBytes {
			return e.New(e.CodeVerifyFailed, "expanded container image layer exceeds byte bound")
		}
		_, _ = hash.Write(buffer[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return e.Wrap(e.CodeVerifyFailed, readErr, "reading container image layer")
		}
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != diffID {
		return e.New(e.CodeVerifyFailed, "container image layer does not match its uncompressed identity")
	}
	return nil
}
