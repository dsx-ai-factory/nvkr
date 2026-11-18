// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// SelectedMetadata owns an intact original root and the control/evidence bytes
// needed by a bounded named selection. It cannot export payloads or be used as
// a complete Publication. It establishes integrity, never signatures or policy.
// Shallow copies share revocation; Close joins active synchronous callbacks.
type SelectedMetadata struct {
	state     *publicationState
	selection metadataSelection
}

// OpenSelectedMetadata captures a sparse metadata layout into immutable held
// files, then validates the captured bytes. Source replacement after issuance
// cannot change the retained proof closure. All payloads and extra blobs fail.
func OpenSelectedMetadata(ctx context.Context, root, expectedRoot string, names []string) (result *SelectedMetadata, resultErr error) {
	selection, err := newMetadataSelection(names)
	if err != nil {
		return nil, err
	}
	if _, err := InspectSelectedMetadata(ctx, root, expectedRoot, names); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp("", "nvkr-selected-metadata-*")
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating selected metadata workspace")
	}
	owner := &SelectedMetadata{state: &publicationState{}, selection: selection}
	issued := false
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, err, "removing selected metadata workspace"))
		}
		if !issued || resultErr != nil {
			resultErr = errors.Join(resultErr, owner.Close())
			result = nil
		}
	}()
	if err := os.MkdirAll(filepath.Join(stage, "blobs", "sha256"), 0o700); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating selected metadata blob directory")
	}
	var total int64
	capture := func(relative, digest string) error {
		remaining := maxMetadataBytes - total
		if remaining <= 0 || len(owner.state.files) >= maxDeliveryObjects*4 {
			return e.New(e.CodeEngineCapability, "selected metadata snapshot exceeds its byte or object limit")
		}
		file, err := heldfile.OpenContext(ctx, filepath.Join(root, relative), heldfile.Options{
			Label: "selected metadata", ExpectedDigest: digest, MaxBytes: remaining,
		})
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing selected metadata bytes"))
		}
		owner.state.files = append(owner.state.files, file)
		total += file.Size()
		out, err := os.OpenFile(filepath.Join(stage, relative), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "materializing selected metadata snapshot")
		}
		_, writeErr := file.WriteToContext(ctx, out)
		if err := errors.Join(writeErr, out.Close()); err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "writing selected metadata snapshot"))
		}
		return nil
	}
	if err := capture("index.json", expectedRoot); err != nil {
		return nil, err
	}
	directory, err := os.Open(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening selected metadata inventory")
	}
	entries, readErr := directory.ReadDir(maxDeliveryObjects*4 + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading selected metadata inventory")
	}
	if len(entries) >= maxDeliveryObjects*4 {
		return nil, e.New(e.CodeEngineCapability, "selected metadata has too many blobs")
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, entry := range entries {
		pin := "sha256:" + entry.Name()
		if !isDigest(pin) || !entry.Type().IsRegular() {
			return nil, e.New(e.CodeVerifyFailed, "selected metadata has an invalid inventory entry")
		}
		if err := capture(filepath.Join("blobs", "sha256", entry.Name()), pin); err != nil {
			return nil, err
		}
	}
	inspection, err := inspectSelectedMetadata(ctx, stage, false, expectedRoot, nil, selection)
	if err != nil {
		return nil, err
	}
	outputs, err := MetadataOutputs(inspection.Root)
	if err != nil {
		return nil, err
	}
	platforms := make(map[string]publicationPlatform)
	for _, object := range inspection.Root.Objects {
		if !selection.includes(object) {
			// This remains an original metadata claim; no omitted config is read.
			platforms[object.Name] = publicationPlatform{OS: object.OS, Arch: object.Arch}
			continue
		}
		platform, err := metadataObjectPlatform(ctx, stage, object)
		if err != nil {
			return nil, err
		}
		platforms[object.Name] = platform
	}
	owner.state.inspection, owner.state.outputs, owner.state.platforms = inspection, outputs, platforms
	issued = true
	return owner, nil
}

func (owner *SelectedMetadata) Close() error {
	if owner == nil {
		return nil
	}
	return owner.state.close()
}

// Description retains complete original logical membership. Only selected
// controls and required evidence were physically checked; no payload was checked.
func (owner *SelectedMetadata) Description(reference string) (PublicationDescription, error) {
	if owner == nil || owner.state == nil {
		return PublicationDescription{}, e.New(e.CodeGateUnmet, "selected metadata is not open")
	}
	view, err := owner.state.description(reference)
	if err == nil {
		view.ReferenceScope = "original metadata; only selected controls and evidence checked, no payload verification"
	}
	return view, err
}

// VisitObjects lends only the captured sparse control/proof closure. Callbacks
// must honor context, bound writes and not reenter or close the owner.
func (owner *SelectedMetadata) VisitObjects(ctx context.Context, visit func(context.Context, PublicationObject, io.ReadSeeker) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if owner == nil || owner.state == nil {
		return e.New(e.CodeGateUnmet, "selected metadata is not open")
	}
	if visit == nil {
		return e.New(e.CodeUsage, "selected metadata requires an object visitor")
	}
	owner.state.mu.RLock()
	defer owner.state.mu.RUnlock()
	if owner.state.closed {
		return e.New(e.CodeGateUnmet, "selected metadata is closed")
	}
	for index, file := range owner.state.files {
		key := "blobs/sha256/" + strings.TrimPrefix(file.Digest(), "sha256:")
		if index == 0 {
			key = "index.json"
		}
		if err := checkPublicationObjectSize(ctx, file.OSFile(), file.Size()); err != nil {
			return err
		}
		source := publicationObjectSource{
			description: PublicationObject{Key: key, SHA256: file.Digest(), Size: file.Size(), MediaType: MediaOctetStream},
			source:      file,
			check:       func() error { return checkPublicationObjectSize(ctx, file.OSFile(), file.Size()) },
		}
		if err := visitPublicationObject(ctx, source, visit); err != nil {
			var coded *e.Error
			if !errors.As(err, &coded) {
				err = e.Wrap(e.CodeVerifyFailed, err, "visiting selected metadata object")
			}
			return classifyContext(ctx, err)
		}
	}
	return deliveryContext(ctx)
}

// VisitEvidence lends original selected attestation bytes under the lifetime
// lock. The callback owns cryptographic policy and must not reenter the owner.
func (owner *SelectedMetadata) VisitEvidence(ctx context.Context, visit func(context.Context, EvidenceBinding, []byte) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if owner == nil || owner.state == nil {
		return e.New(e.CodeGateUnmet, "selected metadata is not open")
	}
	if visit == nil {
		return e.New(e.CodeUsage, "selected evidence requires a visitor")
	}
	owner.state.mu.RLock()
	defer owner.state.mu.RUnlock()
	if owner.state.closed {
		return e.New(e.CodeGateUnmet, "selected metadata is closed")
	}
	for _, object := range owner.state.inspection.Root.Objects {
		if object.Evidence != nil && owner.selection.includes(object) && object.ArtifactType != sbom.BundleMediaType {
			return e.New(e.CodeProvenanceMissing, "selected evidence requires original attestations")
		}
	}
	files := make(map[string]*heldfile.File, len(owner.state.files))
	for _, file := range owner.state.files {
		files[file.Digest()] = file
	}
	for _, object := range owner.state.inspection.Root.Objects {
		if object.Evidence == nil || !owner.selection.includes(object) {
			continue
		}
		file := files[object.BlobDigest]
		if file == nil {
			return e.New(e.CodeVerifyFailed, "selected attestation has no retained bytes")
		}
		raw, err := file.ReadAllContext(ctx, sbom.MaxBundleBytes)
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading selected attestation"))
		}
		binding := *object.Evidence
		binding.Outputs = slices.Clone(binding.Outputs)
		if err := visit(ctx, binding, raw); err != nil {
			var coded *e.Error
			if !errors.As(err, &coded) {
				err = e.Wrap(e.CodeVerifyFailed, err, "visiting selected attestation")
			}
			return classifyContext(ctx, err)
		}
	}
	return deliveryContext(ctx)
}
