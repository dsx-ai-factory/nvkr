// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const (
	MediaImageConfig    = "application/vnd.oci.image.config.v1+json"
	MediaImageLayer     = "application/vnd.oci.image.layer.v1.tar"
	MediaImageLayerGzip = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaDockerManifest = string(types.DockerManifestSchema2)
)

// ImageInspection describes a verified single-platform runnable image. It is
// descriptive evidence, not a release authorization or reproduction receipt.
// ManifestDigest is the pullable image identity; RootDigest identifies its layout index.
type ImageInspection struct {
	MediaType      string
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
	Variant      string `json:"variant,omitempty"`
	Config       struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
	} `json:"config"`
	RootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

func inspectPublishedContainerImage(ctx context.Context, root string, publication Inspection, name string) (ImageInspection, error) {
	return inspectPublishedContainerImageFrom(ctx, publication, name, pathImageBlobs(root))
}

func inspectPublishedContainerImageFrom(ctx context.Context, publication Inspection, name string, open imageBlobOpener) (ImageInspection, error) {
	var object PublishedObject
	for _, candidate := range publication.Root.Objects {
		if candidate.Name == name {
			if object.Name != "" {
				return ImageInspection{}, e.New(e.CodeVerifyFailed, "ambiguous published image %q", name)
			}
			object = candidate
		}
	}
	if object.Name == "" || !types.MediaType(object.ArtifactType).IsImage() || object.OS != "linux" ||
		(object.Arch != "amd64" && object.Arch != "arm64") || object.BlobDigest != object.DescriptorDigest {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "publication has no exact native image binding for %q", name)
	}
	descriptor := Descriptor{MediaType: object.ArtifactType, Digest: object.DescriptorDigest, Size: object.Size}
	held, release, err := open(ctx, descriptor, 8<<20)
	if err != nil {
		return ImageInspection{}, err
	}
	manifestRaw, err := held.ReadAllContext(ctx, 8<<20)
	release()
	if err != nil {
		return ImageInspection{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading published image manifest"))
	}
	if err := canonical.ValidateJSONDocument(manifestRaw); err != nil {
		return ImageInspection{}, e.Wrap(e.CodeVerifyFailed, err, "validating published image manifest JSON")
	}
	var manifest ociManifest
	if err := validateImageSchemaNames(manifestRaw, reflect.TypeFor[v1.Manifest]()); err != nil {
		return ImageInspection{}, err
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return ImageInspection{}, e.Wrap(e.CodeVerifyFailed, err, "decoding published image manifest")
	}
	if manifest.MediaType != descriptor.MediaType {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "published image manifest differs from descriptor media type")
	}
	config, err := verifyImageManifestFrom(ctx, manifest, map[string]bool{}, open)
	if err != nil {
		return ImageInspection{}, err
	}
	if config.OS != object.OS || config.Architecture != object.Arch {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "published image platform differs from its binding")
	}
	return ImageInspection{MediaType: manifest.MediaType, RootDigest: object.DescriptorDigest, ManifestDigest: descriptor.Digest,
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
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return ImageInspection{}, e.Wrap(e.CodeVerifyFailed, err, "validating container image index JSON")
	}
	if err := validateImageSchemaNames(raw, reflect.TypeFor[v1.IndexManifest]()); err != nil {
		return ImageInspection{}, err
	}
	if index.ArtifactType != "" || len(index.Manifests) != 1 || !types.MediaType(index.Manifests[0].MediaType).IsImage() {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "container image requires exactly one OCI image manifest")
	}
	descriptor := index.Manifests[0]
	manifestRaw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return ImageInspection{}, err
	}
	if err := canonical.ValidateJSONDocument(manifestRaw); err != nil {
		return ImageInspection{}, e.Wrap(e.CodeVerifyFailed, err, "validating container image manifest JSON")
	}
	var manifest ociManifest
	if err := validateImageSchemaNames(manifestRaw, reflect.TypeFor[v1.Manifest]()); err != nil {
		return ImageInspection{}, err
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return ImageInspection{}, e.Wrap(e.CodeVerifyFailed, err, "decoding container image manifest")
	}
	if manifest.MediaType != descriptor.MediaType {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "container image manifest differs from descriptor media type")
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
	return ImageInspection{MediaType: manifest.MediaType, RootDigest: digestBytes(raw), ManifestDigest: descriptor.Digest,
		OS: config.OS, Arch: config.Architecture, Entrypoint: config.Config.Entrypoint, Command: config.Config.Cmd}, nil
}

// imageBlobOpener owns the returned file until its release function is called.
// A held closure lends already verified files; path diagnostics capture new ones.
type imageBlobOpener func(context.Context, Descriptor, int64) (*heldfile.File, func(), error)

func pathImageBlobs(root string) imageBlobOpener {
	return func(ctx context.Context, descriptor Descriptor, maximum int64) (*heldfile.File, func(), error) {
		file, err := holdBlob(ctx, root, descriptor, maximum)
		if err != nil {
			return nil, nil, err
		}
		return file, func() { _ = file.Close() }, nil
	}
}

func verifyImageManifest(ctx context.Context, root string, manifest ociManifest, visited map[string]bool) (imageConfig, error) {
	return verifyImageManifestFrom(ctx, manifest, visited, pathImageBlobs(root))
}

func verifyImageManifestFrom(ctx context.Context, manifest ociManifest, visited map[string]bool, open imageBlobOpener) (imageConfig, error) {
	config, err := inspectImageMetadataFrom(ctx, manifest, open)
	if err != nil {
		return config, err
	}
	visited[manifest.Config.Digest] = true
	for i, layer := range manifest.Layers {
		if err := verifyImageLayerFrom(ctx, layer, config.RootFS.DiffIDs[i], open); err != nil {
			return config, err
		}
		visited[layer.Digest] = true
	}
	return config, nil
}

func inspectImageMetadata(ctx context.Context, root string, manifest ociManifest) (imageConfig, error) {
	return inspectImageMetadataFrom(ctx, manifest, pathImageBlobs(root))
}

func inspectImageMetadataFrom(ctx context.Context, manifest ociManifest, open imageBlobOpener) (imageConfig, error) {
	var config imageConfig
	media := types.MediaType(manifest.MediaType)
	configMedia := types.MediaType(manifest.Config.MediaType)
	if manifest.SchemaVersion != 2 || !media.IsImage() || manifest.ArtifactType != "" ||
		!configMedia.IsConfig() || (media == types.OCIManifestSchema1 && configMedia != types.OCIConfigJSON) ||
		(media == types.DockerManifestSchema2 && configMedia != types.DockerConfigJSON) ||
		len(manifest.Layers) == 0 || len(manifest.Layers) > maxDeliveryObjects {
		return config, e.New(e.CodeVerifyFailed, "container image manifest has an invalid schema or layer count")
	}
	held, release, err := open(ctx, manifest.Config, 8<<20)
	if err != nil {
		return config, err
	}
	raw, err := held.ReadAllContext(ctx, 8<<20)
	release()
	if err != nil {
		return config, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading container image config"))
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return config, e.Wrap(e.CodeVerifyFailed, err, "validating container image config JSON")
	}
	if err := validateImageSchemaNames(raw, reflect.TypeFor[v1.ConfigFile]()); err != nil {
		return config, err
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, e.Wrap(e.CodeVerifyFailed, err, "decoding container image config")
	}
	if config.OS != "linux" || (config.Architecture != "amd64" && config.Architecture != "arm64") ||
		(config.Variant != "" && (config.Architecture != "arm64" || config.Variant != "v8")) ||
		config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(manifest.Layers) ||
		(len(config.Config.Entrypoint) == 0 && len(config.Config.Cmd) == 0) {
		return config, e.New(e.CodeVerifyFailed, "container image config has an invalid platform, rootfs or command")
	}
	for i, layer := range manifest.Layers {
		if err := validateImageLayerDescriptor(layer, config.RootFS.DiffIDs[i]); err != nil {
			return config, err
		}
	}
	return config, nil
}

func verifyImageLayerFrom(ctx context.Context, layer Descriptor, diffID string, open imageBlobOpener) error {
	// Repeat the metadata check so this helper rejects invalid descriptors before
	// opening payloads even when called without inspectImageMetadataFrom.
	if err := validateImageLayerDescriptor(layer, diffID); err != nil {
		return err
	}
	held, release, err := open(ctx, layer, maxDeliveryFileBytes)
	if err != nil {
		return err
	}
	defer release()
	var reader io.Reader = io.NewSectionReader(held, 0, held.Size())
	if layer.MediaType == MediaImageLayerGzip || layer.MediaType == string(types.DockerLayer) {
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

// publishedManifestMediaType keeps the exact native image encoding. Other
// deliverables and evidence are NVKR-owned OCI artifact manifests.
func publishedManifestMediaType(object PublishedObject) string {
	if object.Output != nil && object.Output.ImageIndex != nil {
		return MediaOCIIndex
	}
	if types.MediaType(object.ArtifactType).IsImage() {
		return object.ArtifactType
	}
	return MediaOCIManifest
}

func validateImageLayerDescriptor(layer Descriptor, diffID string) error {
	if !isDigest(layer.Digest) || !isDigest(diffID) || layer.Size < 0 || layer.Size > maxDeliveryFileBytes ||
		(layer.MediaType != MediaImageLayer && layer.MediaType != MediaImageLayerGzip &&
			layer.MediaType != string(types.DockerLayer) && layer.MediaType != string(types.DockerUncompressedLayer)) {
		return e.New(e.CodeVerifyFailed, "container image layer has an invalid identity, size or media type")
	}
	return nil
}
