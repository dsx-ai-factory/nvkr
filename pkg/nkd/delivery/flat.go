// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const MediaEmptyConfig = "application/vnd.oci.empty.v1+json"

// outputFormatHelm mirrors definition.OutputHelm and factory.PackageHelm; the
// delivery package intentionally does not import definition to dispatch on an
// Output.Format string, so it carries its own copy of the same literal instead
// of a bare "helm-oci" repeated at every call site.
const outputFormatHelm = "helm-oci"

// The Helm OCI support media types (https://helm.sh/docs/topics/registries/), used
// by BuildHelmChartArtifact so a packaged chart is a standards-conformant Helm OCI
// artifact: real `helm pull`/`helm show`/`oras pull` resolve content purely from
// these two media types, never from NKD's own manifest-level ArtifactType.
const (
	MediaHelmConfig     = "application/vnd.cncf.helm.config.v1+json"
	MediaHelmChartLayer = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// PayloadDescriptor identifies the single deliverable carried by a local OCI
// layout. Layout index files are transport scaffolding, not published wrappers.
func PayloadDescriptor(ctx context.Context, root string) (Descriptor, error) {
	index, _, err := readIndex(ctx, root)
	if err != nil {
		return Descriptor{}, err
	}
	var selected Descriptor
	visited := map[string]bool{}
	for _, descriptor := range index.Manifests {
		if err := verifyDescriptor(ctx, root, descriptor, visited); err != nil {
			return Descriptor{}, err
		}
		if descriptor.ArtifactType == MediaPublicationRoot {
			continue
		}
		if selected.Digest != "" || !types.MediaType(descriptor.MediaType).IsImage() {
			return Descriptor{}, e.New(e.CodeVerifyFailed, "payload layout must contain one deliverable manifest")
		}
		selected = descriptor
	}
	if selected.Digest == "" {
		return Descriptor{}, e.New(e.CodeVerifyFailed, "payload layout has no deliverable manifest")
	}
	if err := verifyBlobInventory(root, visited); err != nil {
		return Descriptor{}, err
	}
	return selected, nil
}

func (writer layoutWriter) importFlatLayout(ctx context.Context, input ObjectInput) (Descriptor, string, error) {
	if input.Output != nil && input.Output.ImageIndex != nil {
		return writer.importImageIndexLayout(ctx, input)
	}
	_, indexRaw, err := readIndex(ctx, input.Path)
	if err != nil {
		return Descriptor{}, "", err
	}
	if input.BlobDigest != "" && digestBytes(indexRaw) != input.BlobDigest {
		return Descriptor{}, "", e.New(e.CodeVerifyFailed, "payload layout differs from its input pin")
	}
	selected, err := PayloadDescriptor(ctx, input.Path)
	if err != nil {
		return Descriptor{}, "", err
	}
	if err := writer.copyClosure(ctx, input.Path, selected); err != nil {
		return Descriptor{}, "", err
	}
	blob := selected.Digest
	manifest, err := flatManifest(ctx, input.Path, PublishedObject{DescriptorDigest: selected.Digest, Size: selected.Size})
	if err != nil {
		return Descriptor{}, "", err
	}
	if len(manifest.Layers) == 1 && (manifest.Config.MediaType == MediaEmptyConfig || manifest.Config.MediaType == MediaHelmConfig ||
		(manifest.Config.MediaType == MediaImageConfig && manifest.Layers[0].MediaType == MediaQCOW2)) {
		blob = manifest.Layers[0].Digest
	}
	return selected, blob, nil
}

func flatManifest(ctx context.Context, root string, object PublishedObject) (ociManifest, error) {
	raw, err := readBlob(ctx, root, Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return ociManifest{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ociManifest{}, e.Wrap(e.CodeVerifyFailed, err, "decoding deliverable manifest")
	}
	return manifest, nil
}

// ReadArtifact returns a held, verified single-payload artifact for packaging
// inspection. Binary and native-image manifests require their own typed readers.
func ReadArtifact(ctx context.Context, root string) (Descriptor, []byte, error) {
	descriptor, err := PayloadDescriptor(ctx, root)
	if err != nil {
		return Descriptor{}, nil, err
	}
	manifest, err := flatManifest(ctx, root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
	if err != nil {
		return Descriptor{}, nil, err
	}
	if len(manifest.Layers) != 1 {
		return Descriptor{}, nil, e.New(e.CodeVerifyFailed, "artifact has an unsupported config or payload count")
	}
	switch manifest.Config.MediaType {
	case MediaEmptyConfig:
		config, err := readBlob(ctx, root, manifest.Config, 8<<20)
		if err != nil || string(config) != "{}" {
			return Descriptor{}, nil, e.New(e.CodeVerifyFailed, "artifact config is not empty")
		}
	case MediaHelmConfig:
		// The Helm chart config carries real Chart.yaml-derived metadata, opaque to
		// this reader beyond confirming it is present and within bound.
		if _, err := readBlob(ctx, root, manifest.Config, 8<<20); err != nil {
			return Descriptor{}, nil, err
		}
	default:
		return Descriptor{}, nil, e.New(e.CodeVerifyFailed, "artifact has an unsupported config or payload count")
	}
	raw, err := readBlob(ctx, root, manifest.Layers[0], maxDeliveryFileBytes)
	return descriptor, raw, err
}

func (writer layoutWriter) copyClosure(ctx context.Context, source string, descriptor Descriptor) (err error) {
	visited := map[string]bool{}
	disks := map[string]*DiskArtifact{}
	defer func() {
		for _, disk := range disks {
			err = errors.Join(err, disk.Close())
		}
	}()
	if err := verifyDescriptorHoldingDisks(ctx, source, descriptor, visited, nil, disks); err != nil {
		return err
	}
	for digest := range visited {
		if disk := disks[digest]; disk != nil {
			if err := writer.copyDiskPayload(ctx, disk); err != nil {
				return err
			}
			continue
		}
		held, err := heldfile.OpenContext(ctx, filepath.Join(source, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), heldfile.Options{Label: "release payload", MaxBytes: maxDeliveryFileBytes})
		if err != nil {
			return err
		}
		if held.Digest() != digest {
			_ = held.Close()
			return e.New(e.CodeVerifyFailed, "payload changed during import")
		}
		_, copyErr := writer.addBlobHeld(ctx, held, MediaOctetStream)
		closeErr := held.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}

	return nil
}
