// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// BootProofSet holds only original sparse controls and proof bundles obtained
// from exact S3 pins. It has no registry fallback and no payload/export methods.
// It establishes byte integrity, never signatures or selected release authority.
type BootProofSet struct{ state *bootProofSetState }

type bootProofSetState struct {
	mu        sync.RWMutex
	closed    bool
	inventory bootInventory
	metadata  map[string]*SelectedMetadata
	proofs    []RestoredProof
}

// Uses returns detached discovery records; the consumer must independently join
// them to the original metadata before any availability receipt can be issued.
func (set *BootProofSet) Uses() ([]BootOriginalUse, error) {
	if set == nil || set.state == nil {
		return nil, e.New(e.CodeGateUnmet, "boot proof set was not opened")
	}
	set.state.mu.RLock()
	defer set.state.mu.RUnlock()
	if set.state.closed {
		return nil, e.New(e.CodeGateUnmet, "boot proof set is closed")
	}
	return slices.Clone(set.state.inventory.Uses), nil
}

// StoredAssets returns only descriptive pins. Recheck must join them to raw
// bindings independently authenticated from the original signed controls.
func (set *BootProofSet) StoredAssets() ([]BootStoredAsset, error) {
	if set == nil || set.state == nil {
		return nil, e.New(e.CodeGateUnmet, "boot proof set was not opened")
	}
	set.state.mu.RLock()
	defer set.state.mu.RUnlock()
	if set.state.closed {
		return nil, e.New(e.CodeGateUnmet, "boot proof set is closed")
	}
	objects := map[string]StoredObject{}
	for _, object := range set.state.inventory.Objects {
		objects[object.SHA256] = object.stored()
	}
	assets := make([]BootStoredAsset, len(set.state.inventory.Uses))
	for index, use := range set.state.inventory.Uses {
		assets[index] = BootStoredAsset{Binding: use.Binding, Object: objects[use.Binding.SHA256], URL: use.URL}
	}
	return assets, nil
}

// Metadata lends one exact original sparse owner. Closing that owner makes
// subsequent checks fail. Callers must not close the set during owner callbacks.
func (set *BootProofSet) Metadata(reference string) (*SelectedMetadata, error) {
	if set == nil || set.state == nil {
		return nil, e.New(e.CodeGateUnmet, "boot proof set was not opened")
	}
	set.state.mu.RLock()
	defer set.state.mu.RUnlock()
	if set.state.closed {
		return nil, e.New(e.CodeGateUnmet, "boot proof set is closed")
	}
	metadata := set.state.metadata[reference]
	if metadata == nil {
		return nil, e.New(e.CodeVerifyFailed, "original root is absent from selected proof set")
	}
	return metadata, nil
}

func (set *BootProofSet) Proofs() ([]RestoredProof, error) {
	if set == nil || set.state == nil {
		return nil, e.New(e.CodeGateUnmet, "boot proof set was not opened")
	}
	set.state.mu.RLock()
	defer set.state.mu.RUnlock()
	if set.state.closed {
		return nil, e.New(e.CodeGateUnmet, "boot proof set is closed")
	}
	proofs := make([]RestoredProof, len(set.state.proofs))
	for index, proof := range set.state.proofs {
		proof.Signature, proof.Provenance = bytes.Clone(proof.Signature), bytes.Clone(proof.Provenance)
		proofs[index] = proof
	}
	return proofs, nil
}

func (set *BootProofSet) Close() error {
	if set == nil || set.state == nil {
		return nil
	}
	set.state.mu.Lock()
	defer set.state.mu.Unlock()
	if set.state.closed {
		return nil
	}
	set.state.closed = true
	var result error
	for _, publication := range set.state.inventory.Publications {
		result = errors.Join(result, set.state.metadata[publication.Reference].Close())
	}
	set.state.proofs = nil
	return result
}

// RestoreProofs retrieves only the selected proof/control closure. The caller
// owns Close even if later signature authentication fails. Raw bytes are checked
// separately by Recheck against bindings derived from these original controls.
func (phase *BootPhase) RestoreProofs(ctx context.Context, locator StoredObject, expected BootInventoryExpectation, workspaceRoot string) (_ *BootProofSet, resultErr error) {
	state, finish, err := phase.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { finish(resultErr) }()
	if !reflect.DeepEqual(expected.Selection, state.request.Selection) || !reflect.DeepEqual(expected.Destination, state.request.Destination) {
		return nil, e.New(e.CodeVerifyFailed, "boot proof restoration differs from admitted selection or destination")
	}
	return restoreBootProofs(ctx, state.store, locator, expected, state.request.Limits, workspaceRoot)
}

func restoreBootProofs(ctx context.Context, store ObjectStore, locator StoredObject, expected BootInventoryExpectation, limits BootTransferLimits, workspaceRoot string) (result *BootProofSet, resultErr error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if _, err := readbudget.Require(ctx); err != nil {
		return nil, err
	}
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	if err := limits.Storage.Validate(); err != nil {
		return nil, err
	}
	if store == nil || !canonical.IsDigest(locator.SHA256) || locator.Key != bootInventoryKey(expected.RootReference, locator.SHA256) ||
		locator.Size <= 0 || locator.Size > maxMirrorInventoryBytes || locator.MediaType != "application/json" {
		return nil, e.New(e.CodeVerifyFailed, "selected proof restoration requires an exact bounded inventory locator")
	}
	if locator.Size > limits.Storage.MaxObjectBytes || locator.Size > limits.Storage.MaxTotalBytes {
		return nil, e.New(e.CodeEngineCapability, "boot inventory exceeds independent consumer storage limits")
	}
	if err := validateBootPin(locator); err != nil {
		return nil, err
	}
	var raw bytes.Buffer
	if err := readMirrorObject(ctx, store, locator, &raw); err != nil {
		return nil, err
	}
	inventory, err := decodeBootInventory(raw.Bytes(), locator, expected, limits)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(workspaceRoot, "nvkr-boot-proofs-*")
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private selected proof workspace")
	}
	set := &BootProofSet{state: &bootProofSetState{inventory: inventory, metadata: map[string]*SelectedMetadata{}}}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, err, "removing selected proof workspace"))
		}
		if resultErr != nil {
			resultErr = errors.Join(resultErr, set.Close())
			result = nil
		}
	}()
	paths := map[string]string{}
	for _, record := range inventory.Objects {
		if !slices.Contains(record.Roles, "control") && !slices.Contains(record.Roles, "proof") {
			continue
		}
		path := filepath.Join(stage, strings.TrimPrefix(record.SHA256, "sha256:"))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private selected proof object")
		}
		readErr := readMirrorObject(ctx, store, record.stored(), file)
		if err := errors.Join(readErr, file.Close()); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading complete selected proof object")
		}
		paths[record.SHA256] = path
	}
	for _, publication := range inventory.Publications {
		root, err := os.MkdirTemp(stage, "root-*")
		if err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating original selected metadata root")
		}
		if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0o700); err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating selected metadata blob directory")
		}
		for _, member := range publication.Members {
			if err := os.Link(paths[member.SHA256], filepath.Join(root, filepath.FromSlash(member.Path))); err != nil {
				return nil, e.Wrap(e.CodeImmutableViolation, err, "assembling original selected metadata bytes")
			}
		}
		owner, err := OpenSelectedMetadata(ctx, root, publication.RootDigest, publication.Outputs, workspaceRoot)
		if err != nil {
			return nil, err
		}
		set.state.metadata[publication.Reference] = owner
	}
	proofBytes := map[string][]byte{}
	for _, proof := range inventory.Proofs {
		for _, digest := range []string{proof.Signature, proof.Provenance} {
			if _, exists := proofBytes[digest]; exists {
				continue
			}
			// These are private regular files already size/hash checked through a
			// pinned read and admitted individually and in the 16 MiB proof pool.
			raw, err := readPrivateBootProof(ctx, paths[digest], digest)
			if err != nil {
				return nil, err
			}
			proofBytes[digest] = raw
		}
		set.state.proofs = append(set.state.proofs, RestoredProof{Subject: proof.Subject.signingSubject(), Signature: proofBytes[proof.Signature], Provenance: proofBytes[proof.Provenance]})
	}
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	return set, nil
}

func readPrivateBootProof(ctx context.Context, path, digest string) (raw []byte, resultErr error) {
	file, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "original boot proof", ExpectedDigest: digest, MaxBytes: maxMirrorProofBundleBytes})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "retaining exact original boot proof")
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeVerifyFailed, err, "closing original boot proof"))
		}
		if resultErr != nil {
			raw = nil
		}
	}()
	raw, err = file.ReadAllContext(ctx, maxMirrorProofBundleBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading retained original boot proof")
	}
	return raw, nil
}
