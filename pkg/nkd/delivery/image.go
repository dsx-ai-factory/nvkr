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
	"path/filepath"
	"reflect"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
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
	manifestRaw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return ImageInspection{}, err
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
	config, err := verifyImageManifest(ctx, root, manifest, map[string]bool{})
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

func verifyImageManifest(ctx context.Context, root string, manifest ociManifest, visited map[string]bool) (imageConfig, error) {
	var config imageConfig
	media := types.MediaType(manifest.MediaType)
	configMedia := types.MediaType(manifest.Config.MediaType)
	if manifest.SchemaVersion != 2 || !media.IsImage() || manifest.ArtifactType != "" ||
		!configMedia.IsConfig() || (media == types.OCIManifestSchema1 && configMedia != types.OCIConfigJSON) ||
		(media == types.DockerManifestSchema2 && configMedia != types.DockerConfigJSON) ||
		len(manifest.Layers) == 0 || len(manifest.Layers) > maxDeliveryObjects {
		return config, e.New(e.CodeVerifyFailed, "container image manifest has an invalid schema or layer count")
	}
	raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return config, err
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
		(layer.MediaType != MediaImageLayer && layer.MediaType != MediaImageLayerGzip &&
			layer.MediaType != string(types.DockerLayer) && layer.MediaType != string(types.DockerUncompressedLayer)) {
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
// deliverables and evidence are NKD-owned OCI artifact manifests.
func publishedManifestMediaType(object PublishedObject) string {
	if types.MediaType(object.ArtifactType).IsImage() {
		return object.ArtifactType
	}
	return MediaOCIManifest
}
