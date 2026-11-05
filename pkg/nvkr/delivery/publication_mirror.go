// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const mirrorInventorySchema = "nvkr.dev/publication-mirror/v1"
const maxMirrorInventoryBytes int64 = 128 << 20
const maxMirrorProofBundleBytes int64 = 8 << 20
const maxMirrorProofBytes int64 = 16 << 20
const maxMirrorObjects = maxDeliveryObjects * 8

type MirrorLimits struct {
	MaxObjectBytes, MaxTotalBytes int64
	MaxObjects                    int
}

// Validate admits numeric transfer bounds without filesystem or provider I/O.
func (limits MirrorLimits) Validate() error { return limits.validate() }

func (limits MirrorLimits) validate() error {
	if limits.MaxObjectBytes <= 0 || limits.MaxObjectBytes > maxDeliveryFileBytes || limits.MaxTotalBytes <= 0 || limits.MaxObjects <= 0 || limits.MaxObjects > maxMirrorObjects {
		return e.New(e.CodeUsage, "mirror requires explicit bounded object, aggregate-byte and object-count limits")
	}
	return nil
}

type MirrorPublication struct {
	Reference   string
	Publication *Publication
}

// MirrorRequest borrows checked layouts and original verified proof bytes.
// Lifecycle owns policy, exact Plan/output membership and aggregate/local joins.
// Mirroring never issues a new signature or authenticates a destination policy.
type MirrorRequest struct {
	RootReference string
	Publications  []MirrorPublication
	Proofs        []*sign.VerifiedOCIEvidence
	Limits        MirrorLimits
}

type mirrorMember struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type MirrorDiskPayload struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Key    string `json:"key"`
}

type mirroredPublication struct {
	Reference    string              `json:"reference"`
	RootDigest   string              `json:"rootDigest"`
	Members      []mirrorMember      `json:"members"`
	DiskPayloads []MirrorDiskPayload `json:"diskPayloads,omitempty"`
}

type mirroredProof struct {
	Subject    mirrorSubject `json:"subject"`
	Signature  string        `json:"signature"`
	Provenance string        `json:"provenance"`
}

// These v1 wire fields deliberately retain their original capitalized names.
// Provider and signing API changes must not alter persisted inventory bytes.
type mirrorSubject struct {
	Ref    string `json:"Ref"`
	Digest string `json:"Digest"`
}

func (subject mirrorSubject) signingSubject() sign.Subject {
	return sign.Subject{Ref: subject.Ref, Digest: subject.Digest}
}

type mirrorStoredObject struct {
	Key       string `json:"Key"`
	SHA256    string `json:"SHA256"`
	MediaType string `json:"MediaType"`
	Size      int64  `json:"Size"`
	VersionID string `json:"VersionID"`
	ETag      string `json:"ETag"`
}

func mirrorObject(stored StoredObject) mirrorStoredObject {
	return mirrorStoredObject{Key: stored.Key, SHA256: stored.SHA256, MediaType: stored.MediaType, Size: stored.Size, VersionID: stored.VersionID, ETag: stored.ETag}
}

func (object mirrorStoredObject) storedObject() StoredObject {
	return StoredObject{Object: Object{Key: object.Key, SHA256: object.SHA256, MediaType: object.MediaType, Size: object.Size}, VersionID: object.VersionID, ETag: object.ETag}
}

// This is an immutable transport inventory, not a signed delivery root. Every
// restored publication and proof must independently pass its existing owner.
type mirrorInventory struct {
	SchemaVersion string                `json:"schemaVersion"`
	RootReference string                `json:"rootReference"`
	Publications  []mirroredPublication `json:"publications"`
	Proofs        []mirroredProof       `json:"proofs"`
	Objects       []mirrorStoredObject  `json:"objects"`
}

func mirrorBlobKey(digest string) string {
	return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
}

// MirrorPublications preflights the complete unique inventory before its first
// conditional write. Original OCI bytes and proof bundles retain their subjects.
// Every PUT is followed by a full pinned hash/size readback. Only then is the
// content-bound inventory written last; failure returns no completion locator.
// A provider-issued exact readback is reused; generic stores are always read here.
// The caller owns the fresh store and all publication lifetimes through return.
func MirrorPublications(ctx context.Context, store ObjectStore, request MirrorRequest) (StoredObject, error) {
	if err := deliveryContext(ctx); err != nil {
		return StoredObject{}, err
	}
	if store == nil {
		return StoredObject{}, e.New(e.CodeUsage, "mirror requires an object store")
	}
	if err := request.Limits.validate(); err != nil {
		return StoredObject{}, err
	}
	if err := ValidatePublicationReference(request.RootReference); err != nil {
		return StoredObject{}, err
	}
	if len(request.Publications) == 0 || len(request.Publications) > maxDeliveryObjects || len(request.Proofs) > maxMirrorObjects {
		return StoredObject{}, e.New(e.CodeUsage, "mirror requires bounded original publications and proofs")
	}
	inventory := mirrorInventory{SchemaVersion: mirrorInventorySchema, RootReference: request.RootReference}
	objects := map[string]Object{}
	var total int64
	add := func(object Object) error {
		if previous, ok := objects[object.SHA256]; ok {
			if previous != object {
				return e.New(e.CodeVerifyFailed, "shared mirror bytes have conflicting descriptions")
			}
			return nil
		}
		if object.Size < 0 || object.Size > request.Limits.MaxObjectBytes || len(objects) >= request.Limits.MaxObjects || object.Size > request.Limits.MaxTotalBytes-total {
			return e.New(e.CodeEngineCapability, "complete mirror exceeds admitted object/count/aggregate byte budget")
		}
		objects[object.SHA256] = object
		total += object.Size
		return nil
	}
	publications := append([]MirrorPublication(nil), request.Publications...)
	sort.Slice(publications, func(i, j int) bool { return publications[i].Reference < publications[j].Reference })
	rootFound := false
	for index, input := range publications {
		if err := ValidatePublicationReference(input.Reference); err != nil {
			return StoredObject{}, err
		}
		if index > 0 && publications[index-1].Reference == input.Reference {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "mirror repeats an original publication")
		}
		description, err := input.Publication.Description(input.Reference)
		if err != nil {
			return StoredObject{}, err
		}
		rootFound = rootFound || input.Reference == request.RootReference
		publication := mirroredPublication{Reference: input.Reference, RootDigest: description.RootDigest}
		if err := input.Publication.VisitObjects(ctx, func(_ context.Context, object PublicationObject, _ io.ReadSeeker) error {
			if object.Key == "oci-layout" {
				return nil
			}
			if err := add(Object{Key: mirrorBlobKey(object.SHA256), SHA256: object.SHA256, Size: object.Size, MediaType: MediaOctetStream}); err != nil {
				return err
			}
			publication.Members = append(publication.Members, mirrorMember{Path: object.Key, SHA256: object.SHA256})
			for _, name := range object.DiskOutputs {
				publication.DiskPayloads = append(publication.DiskPayloads, MirrorDiskPayload{Name: name, SHA256: object.SHA256, Size: object.Size, Key: mirrorBlobKey(object.SHA256)})
			}
			return nil
		}); err != nil {
			return StoredObject{}, err
		}
		sort.Slice(publication.DiskPayloads, func(i, j int) bool { return publication.DiskPayloads[i].Name < publication.DiskPayloads[j].Name })
		inventory.Publications = append(inventory.Publications, publication)
	}
	if !rootFound {
		return StoredObject{}, e.New(e.CodeVerifyFailed, "mirror root is not among its checked publications")
	}
	proofBytes := map[string][]byte{}
	var retainedProofBytes int64
	closureDigests := make(map[string]bool, len(objects))
	for digest := range objects {
		closureDigests[digest] = true
	}
	proofSubjects := map[string]bool{}
	for _, proof := range request.Proofs {
		subject, signature, provenance, err := proof.Bundles()
		if err != nil {
			return StoredObject{}, err
		}
		if !closureDigests[subject.Digest] || proofSubjects[subject.Ref] {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "original mirror proof has an absent or repeated source subject")
		}
		proofSubjects[subject.Ref] = true
		binding := mirroredProof{Subject: mirrorSubject{Ref: subject.Ref, Digest: subject.Digest}, Signature: digestBytes(signature), Provenance: digestBytes(provenance)}
		for _, raw := range [][]byte{signature, provenance} {
			digest := digestBytes(raw)
			if int64(len(raw)) > maxMirrorProofBundleBytes {
				return StoredObject{}, e.New(e.CodeSignatureInvalid, "original proof bundle exceeds verification bound")
			}
			if _, exists := proofBytes[digest]; !exists {
				if int64(len(raw)) > maxMirrorProofBytes-retainedProofBytes {
					return StoredObject{}, e.New(e.CodeEngineCapability, "complete original proof inventory exceeds retained-byte bound")
				}
				retainedProofBytes += int64(len(raw))
			}
			if err := add(Object{Key: mirrorBlobKey(digest), SHA256: digest, Size: int64(len(raw)), MediaType: MediaOctetStream}); err != nil {
				return StoredObject{}, err
			}
			proofBytes[digest] = raw
		}
		inventory.Proofs = append(inventory.Proofs, binding)
	}
	for _, publication := range inventory.Publications {
		if !proofSubjects[publication.Reference] {
			return StoredObject{}, e.New(e.CodeSignatureInvalid, "mirror omits an original publication proof")
		}
	}
	sort.Slice(inventory.Proofs, func(i, j int) bool { return inventory.Proofs[i].Subject.Ref < inventory.Proofs[j].Subject.Ref })
	// Bound the control document before writes too. Provider observations are
	// bounded independently when accepted below; reserve their maximum overhead.
	for _, object := range objects {
		inventory.Objects = append(inventory.Objects, mirrorObject(StoredObject{Object: object}))
	}
	sort.Slice(inventory.Objects, func(i, j int) bool { return inventory.Objects[i].SHA256 < inventory.Objects[j].SHA256 })
	if err := admitMirrorInventory(inventory, request.Limits, total); err != nil {
		return StoredObject{}, err
	}
	inventory.Objects = nil
	stored := map[string]StoredObject{}
	put := func(ctx context.Context, digest string, source io.ReadSeeker) error {
		if _, exists := stored[digest]; exists {
			return nil
		}
		observation, err := putVerifiedMirrorObject(ctx, store, objects[digest], source)
		if err != nil {
			return err
		}
		stored[digest] = observation
		return nil
	}
	for _, publication := range publications {
		if err := publication.Publication.VisitObjects(ctx, func(ctx context.Context, object PublicationObject, source io.ReadSeeker) error {
			if object.Key == "oci-layout" {
				return nil
			}
			return put(ctx, object.SHA256, source)
		}); err != nil {
			return StoredObject{}, err
		}
	}
	for _, proof := range inventory.Proofs {
		for _, digest := range []string{proof.Signature, proof.Provenance} {
			if err := put(ctx, digest, bytes.NewReader(proofBytes[digest])); err != nil {
				return StoredObject{}, err
			}
		}
	}
	for _, object := range stored {
		inventory.Objects = append(inventory.Objects, mirrorObject(object))
	}
	sort.Slice(inventory.Objects, func(i, j int) bool { return inventory.Objects[i].SHA256 < inventory.Objects[j].SHA256 })
	raw, err := canonical.Marshal(inventory)
	if err != nil {
		return StoredObject{}, err
	}
	if int64(len(raw)) > maxMirrorInventoryBytes {
		return StoredObject{}, e.New(e.CodeEngineCapability, "mirror transport inventory exceeds its byte bound")
	}
	rootDigest := strings.SplitN(request.RootReference, "@", 2)[1]
	return putVerifiedMirrorObject(ctx, store, Object{Key: "publications/" + strings.TrimPrefix(rootDigest, "sha256:") + "/" + strings.TrimPrefix(digestBytes(raw), "sha256:") + ".json", SHA256: digestBytes(raw), Size: int64(len(raw)), MediaType: "application/json"}, bytes.NewReader(raw))
}

func admitMirrorInventory(inventory mirrorInventory, limits MirrorLimits, total int64) error {
	preflight, err := canonical.Marshal(inventory)
	if err != nil {
		return err
	}
	// Preserve 1024-byte UTF-8 provider pins, including worst-case JSON escaping.
	controlBound := int64(len(preflight)) + int64(len(inventory.Objects))*(2*1024*6+64)
	if controlBound > maxMirrorInventoryBytes || controlBound > limits.MaxObjectBytes || controlBound > limits.MaxTotalBytes-total || len(inventory.Objects)+1 > limits.MaxObjects {
		return e.New(e.CodeEngineCapability, "complete mirror including transport inventory exceeds its control/count/byte budget")
	}
	return nil
}

func putVerifiedMirrorObject(ctx context.Context, store ObjectStore, object Object, source io.ReadSeeker) (StoredObject, error) {
	var stored StoredObject
	var readback *objectReadback
	var err error
	if provider, ok := store.(objectStoreReadback); ok {
		stored, readback, err = provider.putIfAbsentWithReadback(ctx, object, source)
	} else {
		stored, err = store.PutIfAbsent(ctx, object, source)
	}
	if err != nil {
		return StoredObject{}, e.Wrap(e.CodeVerifyFailed, err, "conditionally storing exact mirror object")
	}
	if stored.Object != object || len(stored.VersionID) > 1024 || len(stored.ETag) > 1024 {
		return StoredObject{}, e.New(e.CodeVerifyFailed, "mirror storage observation changed the requested exact object")
	}
	if readback != nil {
		if err := readback.require(ctx, store, stored); err != nil {
			return StoredObject{}, err
		}
	} else if err := readMirrorObject(ctx, store, stored, io.Discard); err != nil {
		return StoredObject{}, err
	}
	return stored, nil
}

func readMirrorObject(ctx context.Context, store ObjectStore, object StoredObject, destination io.Writer) (returnErr error) {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	body, err := store.OpenPinned(ctx, object)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening pinned mirror object")
	}
	if body == nil {
		return e.New(e.CodeVerifyFailed, "mirror store returned no pinned body")
	}
	defer func() {
		if err := body.Close(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeVerifyFailed, err, "closing pinned mirror object"))
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(body, object.Size+1))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading complete mirrored object")
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if written != object.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != object.SHA256 {
		return e.New(e.CodeVerifyFailed, "mirrored object differs from original exact bytes")
	}
	return nil
}
