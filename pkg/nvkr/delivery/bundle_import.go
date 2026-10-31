// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"io"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/internal/bundlearchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MediaAICRArtifact is the artifact type AICR writes for a deployment bundle.
const MediaAICRArtifact = "application/vnd.nvidia.aicr.artifact"

// bundleImportArtifactTypes lists the producer formats admitted for import.
var bundleImportArtifactTypes = map[string]bool{MediaAICRArtifact: true}

// ImportBundleLayout admits a producer-written OCI image layout holding one gzip
// tar bundle and copies its exact manifest, empty config and layer, so the
// output digest equals the producer's own push. expectedDigest pins the
// manifest. Shape, artifact type and archive admission complete before output
// work; any failure leaves no output.
func ImportBundleLayout(ctx context.Context, out, source, expectedDigest string) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if !isDigest(expectedDigest) {
		return Result{}, e.New(e.CodeVerifyFailed, "bundle layout import requires an exact manifest digest")
	}
	if err := verifyImageLayoutMarker(ctx, source); err != nil {
		return Result{}, err
	}
	index, _, err := readIndex(ctx, source)
	if err != nil {
		return Result{}, err
	}
	if len(index.Manifests) != 1 || index.Manifests[0].MediaType != MediaOCIManifest || index.Manifests[0].Digest != expectedDigest {
		return Result{}, e.New(e.CodeVerifyFailed, "bundle layout must hold exactly the pinned OCI image manifest")
	}
	descriptor := index.Manifests[0]
	raw, err := readBlob(ctx, source, descriptor, 8<<20)
	if err != nil {
		return Result{}, err
	}
	manifest, err := admitBundleManifest(raw)
	if err != nil {
		return Result{}, err
	}
	if descriptor.ArtifactType != "" && descriptor.ArtifactType != manifest.ArtifactType {
		return Result{}, e.New(e.CodeVerifyFailed, "bundle layout descriptor differs from its artifact type")
	}
	config, err := readBlob(ctx, source, manifest.Config, 2)
	if err != nil {
		return Result{}, err
	}
	if string(config) != "{}" {
		return Result{}, e.New(e.CodeVerifyFailed, "bundle OCI config must be empty")
	}
	layer := manifest.Layers[0]
	if err := verifyBlobInventory(source, map[string]bool{descriptor.Digest: true, manifest.Config.Digest: true, layer.Digest: true}); err != nil {
		return Result{}, err
	}
	held, err := holdBlob(ctx, source, layer, bundlearchive.MaxArchiveBytes)
	if err != nil {
		return Result{}, err
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
	if _, err := writer.addBlobBytes(config, MediaEmptyConfig); err != nil {
		return Result{}, err
	}
	if _, err := writer.addBlobHeld(ctx, held, layer.MediaType); err != nil {
		return Result{}, err
	}
	imported, err := writer.addBlobBytes(raw, MediaOCIManifest)
	if err != nil {
		return Result{}, err
	}
	imported.ArtifactType = manifest.ArtifactType
	rootDigest, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: manifest.ArtifactType, Manifests: []Descriptor{imported}})
	if err != nil {
		return Result{}, err
	}
	if err := held.AssertUnchangedContext(ctx); err != nil {
		return Result{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "bundle layer changed during import"))
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

// admitBundleManifest checks the fixed producer shape: one OCI image manifest
// with an admitted artifact type, the empty config and one gzip tar layer.
func admitBundleManifest(raw []byte) (ociManifest, error) {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return ociManifest{}, err
	}
	if err := validateImageSchemaNames(raw, reflect.TypeFor[ociManifest]()); err != nil {
		return ociManifest{}, err
	}
	var manifest ociManifest
	// Descriptor omits the OCI data member, so the config's embedded copy is checked separately.
	var embedded struct {
		Config struct {
			Data *[]byte `json:"data"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ociManifest{}, e.Wrap(e.CodeVerifyFailed, err, "decoding bundle OCI manifest")
	}
	if err := json.Unmarshal(raw, &embedded); err != nil {
		return ociManifest{}, e.Wrap(e.CodeVerifyFailed, err, "decoding bundle OCI config data")
	}
	if !bundleImportArtifactTypes[manifest.ArtifactType] {
		return ociManifest{}, e.New(e.CodeVerifyFailed, "bundle artifact type %q is not admitted", manifest.ArtifactType)
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != MediaOCIManifest || manifest.Subject != nil ||
		manifest.Config.MediaType != MediaEmptyConfig || manifest.Config.Digest != digestBytes([]byte("{}")) || manifest.Config.Size != 2 ||
		(embedded.Config.Data != nil && string(*embedded.Config.Data) != "{}") ||
		len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaImageLayerGzip || !isDigest(manifest.Layers[0].Digest) ||
		manifest.Layers[0].Size <= 0 || manifest.Layers[0].Size > bundlearchive.MaxArchiveBytes {
		return ociManifest{}, e.New(e.CodeVerifyFailed, "bundle layout has an invalid OCI manifest shape")
	}
	return manifest, nil
}
