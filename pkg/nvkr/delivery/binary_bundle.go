// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// BinaryFileInput names one regular file to capture into a bundle.
type BinaryFileInput struct {
	Name        string
	Path        string
	InstallPath string
	Mode        uint32
}

type BinaryBundleRequest struct {
	OS               string
	Arch             string
	Purpose          string
	ReleaseSetDigest string
	InventoryDigest  string
	Epoch            int64
	Files            []BinaryFileInput
}

type capturedBinary struct {
	input BinaryFileInput
	file  *heldfile.File
}

// BuildBinaryBundle creates a deterministic OCI layout containing one typed
// binary-bundle artifact. It captures every source through an immutable held-file
// capability before the output directory becomes visible.
func BuildBinaryBundle(ctx context.Context, out string, request BinaryBundleRequest) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if len(request.Files) == 0 || len(request.Files) > maxDeliveryObjects {
		return Result{}, e.New(e.CodeUsage, "binary bundle requires 1..%d files", maxDeliveryObjects)
	}
	files := append([]BinaryFileInput(nil), request.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	captures := make([]capturedBinary, 0, len(files))
	cleanup := func() {
		for i := len(captures) - 1; i >= 0; i-- {
			_ = captures[i].file.Close()
		}
	}
	defer cleanup()
	bundle := BinaryBundle{SchemaVersion: BinaryBundleSchemaVersion, OS: request.OS,
		Arch: request.Arch, Purpose: request.Purpose, ReleaseSetDigest: request.ReleaseSetDigest,
		InventoryDigest: request.InventoryDigest, Entries: make([]BinaryEntry, 0, len(files))}
	for index, input := range files {
		if index > 0 && files[index-1].Name == input.Name {
			return Result{}, e.New(e.CodeInvalidCatalog, "binary bundle repeats name %q", input.Name)
		}
		if input.Mode == 0 {
			input.Mode = 0o755
		}
		held, err := heldfile.OpenContext(ctx, input.Path, heldfile.Options{
			Label: "binary-bundle:" + input.Name, MaxBytes: maxDeliveryFileBytes,
		})
		if err != nil {
			return Result{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing binary bundle file %q", input.Name))
		}
		captures = append(captures, capturedBinary{input: input, file: held})
		bundle.Entries = append(bundle.Entries, BinaryEntry{Name: input.Name, InstallPath: input.InstallPath,
			Mode: input.Mode, SHA256: held.Digest(), Size: held.Size()})
	}
	if err := bundle.Validate(); err != nil {
		return Result{}, err
	}
	semantic, err := bundle.Digest()
	if err != nil {
		return Result{}, err
	}
	stage, commit, cleanupStage, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanupStage()
	writer := layoutWriter{root: stage}
	configBytes, err := canonical.Marshal(bundle)
	if err != nil {
		return Result{}, err
	}
	config, err := writer.addBlobBytes(configBytes, MediaBinaryBundleConfig)
	if err != nil {
		return Result{}, err
	}
	layer, err := writer.addBundleLayer(ctx, captures, request.Epoch)
	if err != nil {
		return Result{}, err
	}
	layer.Annotations = map[string]string{"org.opencontainers.image.title": "bundle.tar.gz"}
	manifest := ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest, ArtifactType: MediaBinaryBundle,
		Config: config, Layers: []Descriptor{layer}}
	manifestDescriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		return Result{}, err
	}
	manifestDescriptor.ArtifactType = MediaBinaryBundle
	manifestDescriptor.Annotations = map[string]string{
		"org.opencontainers.image.title":        request.Purpose,
		"org.opencontainers.image.os":           request.OS,
		"org.opencontainers.image.architecture": request.Arch,
		"dev.nvkr.semantic.digest":              semantic,
	}
	index := ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: MediaBinaryBundle,
		Manifests: []Descriptor{manifestDescriptor}, Annotations: map[string]string{"dev.nvkr.semantic.digest": semantic}}
	rootDigest, err := writer.writeIndex(index)
	if err != nil {
		return Result{}, err
	}
	if _, verified, err := InspectBinaryBundle(ctx, stage); err != nil || verified.RootDigest != rootDigest || verified.SemanticDigest != semantic {
		if err != nil {
			return Result{}, err
		}
		return Result{}, e.New(e.CodeVerifyFailed, "staged binary-bundle layout changed before commit")
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: rootDigest, SemanticDigest: semantic}, nil
}

// ExtractBinaryBundle verifies a publication root, selects one binary-bundle
// child by name, validates its canonical config against its tar layer, and
// atomically materializes only regular files under out.
func ExtractBinaryBundle(ctx context.Context, layoutRoot, name, out string) (BinaryBundle, error) {
	inspection, err := Inspect(ctx, layoutRoot)
	if err != nil {
		return BinaryBundle{}, err
	}
	return extractBinaryBundle(ctx, layoutRoot, inspection, name, out)
}

// Only the owner of an independently checked closure may reuse its inspection.
func extractBinaryBundle(ctx context.Context, layoutRoot string, inspection Inspection, name, out string) (BinaryBundle, error) {
	var err error
	var object PublishedObject
	for _, candidate := range inspection.Root.Objects {
		if candidate.Name == name && candidate.ArtifactType == MediaBinaryBundle {
			object = candidate
			break
		}
	}
	if object.Name == "" {
		return BinaryBundle{}, e.New(e.CodeUnverifiedRef, "publication root has no binary bundle named %q", name)
	}
	var manifestRaw []byte
	if inspection.Root.Release != nil {
		manifestRaw, err = readBlob(ctx, layoutRoot, Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	} else {
		indexRaw, readErr := readBlob(ctx, layoutRoot, Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
		if readErr != nil {
			return BinaryBundle{}, readErr
		}
		var index ociIndex
		if err := json.Unmarshal(indexRaw, &index); err != nil || len(index.Manifests) != 1 {
			return BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary layout must contain one manifest")
		}
		manifestRaw, err = readBlob(ctx, layoutRoot, index.Manifests[0], 8<<20)
	}
	if err != nil {
		return BinaryBundle{}, err
	}
	manifest, bundle, err := readBinaryBundleConfig(ctx, layoutRoot, manifestRaw)
	if err != nil {
		return BinaryBundle{}, err
	}
	if err := bundle.Validate(); err != nil {
		return BinaryBundle{}, err
	}
	semantic, _ := bundle.Digest()
	if semantic != object.SemanticDigest {
		return BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary-bundle semantic identity does not match publication root")
	}
	layerPath := filepath.Join(layoutRoot, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:"))
	held, err := heldfile.OpenContext(ctx, layerPath, heldfile.Options{Label: "binary-bundle layer", MaxBytes: maxDeliveryFileBytes})
	if err != nil {
		return BinaryBundle{}, err
	}
	defer func() { _ = held.Close() }()
	if held.Digest() != manifest.Layers[0].Digest || held.Size() != manifest.Layers[0].Size {
		return BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary-bundle layer does not match its descriptor")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return BinaryBundle{}, err
	}
	defer cleanup()
	if err := extractBundleLayer(ctx, held, stage, bundle); err != nil {
		return BinaryBundle{}, err
	}
	if err := commit(); err != nil {
		return BinaryBundle{}, err
	}
	return bundle, nil
}

// InspectBinaryBundle verifies a standalone binary-bundle layout and returns its
// canonical config plus both the OCI bootstrap and semantic digests.
func InspectBinaryBundle(ctx context.Context, root string) (BinaryBundle, Result, error) {
	index, raw, err := readIndex(ctx, root)
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	if index.ArtifactType != MediaBinaryBundle || len(index.Manifests) != 1 {
		return BinaryBundle{}, Result{}, e.New(e.CodeVerifyFailed, "OCI layout is not a single NVKR binary bundle")
	}
	visited := map[string]bool{}
	if err := verifyDescriptor(ctx, root, index.Manifests[0], visited); err != nil {
		return BinaryBundle{}, Result{}, err
	}
	if err := verifyBlobInventory(root, visited); err != nil {
		return BinaryBundle{}, Result{}, err
	}
	manifestRaw, err := readBlob(ctx, root, index.Manifests[0], 8<<20)
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	_, bundle, err := readBinaryBundleConfig(ctx, root, manifestRaw)
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	semantic, err := bundle.Digest()
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	if index.Annotations["dev.nvkr.semantic.digest"] != semantic || index.Manifests[0].Annotations["dev.nvkr.semantic.digest"] != semantic {
		return BinaryBundle{}, Result{}, e.New(e.CodeVerifyFailed, "binary-bundle OCI descriptors do not bind its semantic identity")
	}
	return bundle, Result{LayoutPath: root, RootDigest: digestBytes(raw), SemanticDigest: semantic}, nil
}

// readBinaryBundleConfig decodes one manifest and its exact config bytes. The
// caller separately checks the surrounding closure and binds these descriptions
// to either the standalone index or the publication's selected object.
func readBinaryBundleConfig(ctx context.Context, root string, raw []byte) (ociManifest, BinaryBundle, error) {
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.ArtifactType != MediaBinaryBundle || len(manifest.Layers) != 1 {
		return ociManifest{}, BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary-bundle manifest has an invalid shape")
	}
	configRaw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return ociManifest{}, BinaryBundle{}, err
	}
	var bundle BinaryBundle
	if err := canonical.UnmarshalExact(configRaw, &bundle); err != nil {
		return ociManifest{}, BinaryBundle{}, err
	}
	return manifest, bundle, nil
}
