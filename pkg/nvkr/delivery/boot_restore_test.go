// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// This fixture deliberately presents the same original document under several
// references. Restoration must preserve it exactly; lifecycle must reject its
// multi-output local roots and missing original EFI profile. It is transport
// evidence only, not a successful selected Release authentication fixture.
func bootSparseRestoreFixture(t *testing.T) (*mirrorMemoryStore, StoredObject, BootInventoryExpectation, BootTransferLimits, string) {
	t.Helper()
	inventory, expected, limits := bootInventoryFixture(t)
	full, pin := selectedMetadataFixture(t)
	expected.RootReference = "registry.example.test/release@" + pin
	inventory.Expected = expected
	store := &mirrorMemoryStore{data: map[string][]byte{}}
	objects := map[string]bootStoredObject{}
	for _, object := range inventory.Objects {
		if slices.Contains(object.Roles, "payload") {
			objects[object.SHA256] = object
		}
	}
	add := func(digest string, raw []byte, role string) {
		object := objects[digest]
		if object.SHA256 == "" {
			object = bootStoredObject{Key: mirrorBlobKey(digest), SHA256: digest, Size: int64(len(raw)), MediaType: MediaOctetStream, VersionID: "synthetic-version"}
		}
		if !slices.Contains(object.Roles, role) {
			object.Roles = append(object.Roles, role)
			slices.Sort(object.Roles)
		}
		objects[digest] = object
		store.data[object.Key] = raw
	}
	inventory.Publications, inventory.Proofs = nil, nil
	names := []string{}
	for index := range inventory.Uses {
		use := &inventory.Uses[index]
		use.Binding.Publication = "registry.example.test/" + use.Binding.Output + "@" + pin
		names = append(names, use.Binding.Output)
		inventory.Publications = append(inventory.Publications, bootMetadataRecord{Reference: use.Binding.Publication, RootDigest: pin, Outputs: []string{use.Binding.Output}})
	}
	inventory.Publications = append(inventory.Publications, bootMetadataRecord{Reference: expected.RootReference, RootDigest: pin, Outputs: names})
	signature, predicate := []byte("synthetic signature bytes"), []byte("synthetic provenance bytes")
	add(digestBytes(signature), signature, "proof")
	add(digestBytes(predicate), predicate, "proof")
	for index := range inventory.Publications {
		publication := &inventory.Publications[index]
		sparse := filepath.Join(t.TempDir(), "sparse")
		if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), full, sparse, pin, publication.Outputs); err != nil {
			t.Fatal(err)
		}
		owner, err := OpenSelectedMetadata(t.Context(), sparse, pin, publication.Outputs)
		if err != nil {
			t.Fatal(err)
		}
		err = owner.VisitObjects(t.Context(), func(_ context.Context, object PublicationObject, source io.ReadSeeker) error {
			raw, err := io.ReadAll(source)
			if err != nil {
				return err
			}
			add(object.SHA256, raw, "control")
			publication.Members = append(publication.Members, mirrorMember{Path: object.Key, SHA256: object.SHA256})
			return nil
		})
		if closeErr := owner.Close(); err != nil || closeErr != nil {
			t.Fatalf("capturing original metadata: %v; %v", err, closeErr)
		}
		slices.SortFunc(publication.Members, func(a, b mirrorMember) int { return strings.Compare(a.Path, b.Path) })
		inventory.Proofs = append(inventory.Proofs, mirroredProof{Subject: mirrorSubject{Ref: publication.Reference, Digest: pin}, Signature: digestBytes(signature), Provenance: digestBytes(predicate)})
	}
	slices.SortFunc(inventory.Publications, func(a, b bootMetadataRecord) int { return strings.Compare(a.Reference, b.Reference) })
	slices.SortFunc(inventory.Proofs, func(a, b mirroredProof) int { return strings.Compare(a.Subject.Ref, b.Subject.Ref) })
	inventory.Objects = nil
	for _, object := range objects {
		inventory.Objects = append(inventory.Objects, object)
	}
	slices.SortFunc(inventory.Objects, func(a, b bootStoredObject) int { return strings.Compare(a.SHA256, b.SHA256) })
	raw, locator := encodeBootInventoryFixture(t, inventory)
	store.data[locator.Key] = raw
	return store, locator, expected, limits, digestBytes(signature)
}

func TestBootRestoreUsesOnlyPinnedSparseControlsAndProofs(t *testing.T) {
	store, locator, expected, limits, proofDigest := bootSparseRestoreFixture(t)
	ctx, _, err := readbudget.WithLimit(t.Context(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	set, err := restoreBootProofs(ctx, store, locator, expected, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	uses, err := set.Uses()
	if err != nil || len(uses) != 2 {
		t.Fatalf("restored uses: %v", err)
	}
	for _, use := range uses {
		if _, exists := store.data[mirrorBlobKey(use.Binding.SHA256)]; exists {
			t.Fatal("fixture unexpectedly included selected raw payload")
		}
	}
	if store.reads != len(store.data) {
		t.Fatalf("read %d objects, expected %d unique inventory/proof/control objects", store.reads, len(store.data))
	}
	owner, err := set.Metadata(expected.RootReference)
	if err != nil {
		t.Fatal(err)
	}
	description, err := owner.Description(expected.RootReference)
	if err != nil || len(description.Outputs) != 2 {
		t.Fatalf("original complete logical membership changed: %v", err)
	}
	// An unsigned inventory profile cannot supply the missing original binding.
	_, err = owner.BootImagerBinding(expected.RootReference, expected.Selection.Imagers[0])
	assertRegistryCode(t, err, e.CodeEngineCapability)
	proofs, err := set.Proofs()
	if err != nil || len(proofs) != 3 {
		t.Fatalf("original proof set changed: %v", err)
	}
	proofs[0].Signature[0] = '!'
	again, err := set.Proofs()
	if err != nil || again[0].Signature[0] == '!' {
		t.Fatal("proof projection aliases retained bytes")
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = owner.Description(expected.RootReference)
	assertRegistryCode(t, err, e.CodeGateUnmet)
	delete(store.data, mirrorBlobKey(proofDigest))
	set, err = restoreBootProofs(ctx, store, locator, expected, limits)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if set != nil {
		t.Fatal("missing S3 proof returned a retained owner")
	}
}

func TestBootRestoreRejectsForeignLocatorBeforeRead(t *testing.T) {
	store, locator, expected, limits, _ := bootSparseRestoreFixture(t)
	ctx, _, err := readbudget.WithLimit(t.Context(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	locator.Key = "publications/foreign.json"
	_, err = restoreBootProofs(ctx, store, locator, expected, limits)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if store.reads != 0 {
		t.Fatal("foreign locator read before admission")
	}
}
