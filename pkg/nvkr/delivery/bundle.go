// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/internal/bundlearchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const (
	MediaBundle        = "application/vnd.nvkr.bundle.v1"
	MediaBundleLayer   = "application/vnd.nvkr.bundle.layer.v1.tar+gzip"
	outputFormatBundle = "bundle-oci"
)

func validateBundleManifestDocument(raw []byte) error {
	if err := validateImageSchemaNames(raw, reflect.TypeFor[ociManifest]()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest ociManifest
	if err := decoder.Decode(&manifest); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding bundle OCI manifest")
	}
	return nil
}

// BuildBundleArtifact preserves an observed gzip tar archive as one OCI artifact
// layer. The archive is validated and held before output work; expectedDigest
// must identify its exact bytes. This format is neither a native Helm chart nor
// a runnable image, and validation makes no deployment compatibility claim.
func BuildBundleArtifact(ctx context.Context, out, name, archivePath, expectedDigest string) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if !outputNamePattern.MatchString(name) || !isDigest(expectedDigest) {
		return Result{}, e.New(e.CodeVerifyFailed, "bundle requires a canonical name and exact archive digest")
	}
	held, err := heldfile.OpenContext(ctx, archivePath, heldfile.Options{Label: "bundle archive", MaxBytes: bundlearchive.MaxArchiveBytes, ExpectedDigest: expectedDigest})
	if err != nil {
		return Result{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing bundle archive"))
	}
	defer func() { _ = held.Close() }()
	if _, err := bundlearchive.Inspect(ctx, io.NewSectionReader(held, 0, held.Size())); err != nil {
		return Result{}, err
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	descriptor, _, err := writer.addHeldObject(ctx, ObjectInput{Name: name, Kind: outputFormatBundle,
		ArtifactType: MediaBundle, LayerMediaType: MediaBundleLayer, SemanticDigest: held.Digest(), BlobDigest: held.Digest()}, held)
	if err != nil {
		return Result{}, err
	}
	rootDigest, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: MediaBundle, Manifests: []Descriptor{descriptor}})
	if err != nil {
		return Result{}, err
	}
	if _, err := PayloadDescriptor(ctx, stage); err != nil {
		return Result{}, err
	}
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: rootDigest, SemanticDigest: held.Digest()}, nil
}

// inspectBundleMetadata checks the fixed wrapper and optional release binding
// without downloading or claiming to inspect its archive payload.
func inspectBundleMetadata(ctx context.Context, root string, manifest ociManifest, object *PublishedObject) error {
	if manifest.SchemaVersion != 2 || manifest.MediaType != MediaOCIManifest || manifest.ArtifactType != MediaBundle ||
		manifest.Config.MediaType != MediaEmptyConfig || manifest.Subject != nil || len(manifest.Layers) != 1 ||
		manifest.Layers[0].MediaType != MediaBundleLayer || !isDigest(manifest.Layers[0].Digest) ||
		manifest.Layers[0].Size <= 0 || manifest.Layers[0].Size > bundlearchive.MaxArchiveBytes {
		return e.New(e.CodeVerifyFailed, "bundle has an invalid OCI wrapper")
	}
	config, err := readBlob(ctx, root, manifest.Config, 2)
	if err != nil {
		return err
	}
	if string(config) != "{}" {
		return e.New(e.CodeVerifyFailed, "bundle OCI config must be empty")
	}
	if object != nil && (object.Kind != outputFormatBundle || object.ArtifactType != MediaBundle ||
		object.BlobDigest != manifest.Layers[0].Digest || object.SemanticDigest != manifest.Layers[0].Digest ||
		object.Output == nil || object.Output.Format != outputFormatBundle || object.Output.TransportPath != "") {
		return e.New(e.CodeVerifyFailed, "bundle differs from its release binding")
	}
	return nil
}

func inspectBundlePayload(ctx context.Context, root string, manifest ociManifest) error {
	if err := inspectBundleMetadata(ctx, root, manifest, nil); err != nil {
		return err
	}
	held, err := holdBlob(ctx, root, manifest.Layers[0], bundlearchive.MaxArchiveBytes)
	if err != nil {
		return err
	}
	defer func() { _ = held.Close() }()
	_, err = bundlearchive.Inspect(ctx, io.NewSectionReader(held, 0, held.Size()))
	return err
}

func pullBundleOutput(ctx context.Context, reference, out, metadataRoot, workspace string, object PublishedObject, options RegistryOptions) (PulledFile, error) {
	manifest, err := flatManifest(ctx, metadataRoot, object)
	if err != nil {
		return PulledFile{}, err
	}
	if err := inspectBundleMetadata(ctx, metadataRoot, manifest, &object); err != nil {
		return PulledFile{}, err
	}
	layer := manifest.Layers[0]
	file, err := fetchPullLayer(ctx, reference, layer, workspace, options)
	if err != nil {
		return PulledFile{}, err
	}
	defer func() { _ = file.Close() }()
	held, err := heldfile.FromReaderContext(ctx, io.NewSectionReader(file, 0, layer.Size), heldfile.Options{
		Label: "downloaded bundle", ExpectedDigest: layer.Digest, MaxBytes: bundlearchive.MaxArchiveBytes,
	})
	if err != nil {
		return PulledFile{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing downloaded bundle"))
	}
	defer func() { _ = held.Close() }()
	if held.Size() != layer.Size {
		return PulledFile{}, e.New(e.CodeVerifyFailed, "downloaded bundle differs from its exact size")
	}
	if _, err := bundlearchive.Inspect(ctx, io.NewSectionReader(held, 0, held.Size())); err != nil {
		return PulledFile{}, err
	}
	err = publishPullDirectory(ctx, out, func(stage *os.File) error {
		return writeImageIndexFile(ctx, stage, object.Name+".tar.gz", func(destination io.Writer) error {
			_, err := held.WriteToContext(ctx, destination)
			return classifyContext(ctx, err)
		})
	})
	if err != nil {
		return PulledFile{}, err
	}
	return PulledFile{Path: object.Name + ".tar.gz", SHA256: layer.Digest, Size: layer.Size}, nil
}
