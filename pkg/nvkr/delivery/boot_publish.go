// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// BootProofPublication borrows an intact original sparse owner. Lifecycle must
// authenticate these exact controls and all original aggregate/local joins.
type BootProofPublication struct {
	Reference string
	Metadata  *SelectedMetadata
}

type BootPublicationRequest struct {
	// Recovery requires the reconstructed inventory to match this exact attempt.
	// It never skips original proof authentication or this phase's raw recheck.
	Recovery     *StoredObject
	Expected     BootInventoryExpectation
	Availability *BootAvailability
	Uses         []BootOriginalUse
	Publications []BootProofPublication
	Proofs       []*sign.VerifiedOCIEvidence
}

// PublishProofs writes only original selected controls/proofs and commits the
// canonical inventory last. Its locator remains descriptive until the enclosing
// phase, including cleanup, succeeds and lifecycle issues scoped completion.
func (phase *BootPhase) PublishProofs(ctx context.Context, request BootPublicationRequest) (_ StoredObject, resultErr error) {
	state, finish, err := phase.begin(ctx)
	if err != nil {
		return StoredObject{}, err
	}
	defer func() { finish(resultErr) }()
	if request.Availability == nil || request.Availability.owner != state ||
		!reflect.DeepEqual(request.Expected.Selection, state.request.Selection) || !reflect.DeepEqual(request.Expected.Destination, state.request.Destination) {
		return StoredObject{}, e.New(e.CodeVerifyFailed, "boot final proof publication requires this phase's exact selection and availability")
	}
	return publishBootProofs(ctx, state.store, request, state.request.Limits)
}

func publishBootProofs(ctx context.Context, store ObjectStore, request BootPublicationRequest, limits BootTransferLimits) (StoredObject, error) {
	if err := request.Expected.Validate(); err != nil {
		return StoredObject{}, err
	}
	if request.Recovery != nil {
		if err := ValidateBootInventoryAttempt(request.Expected.RootReference, *request.Recovery); err != nil {
			return StoredObject{}, err
		}
	}
	available, err := request.Availability.Descriptions()
	if err != nil {
		return StoredObject{}, err
	}
	if len(available) != len(request.Uses) {
		return StoredObject{}, e.New(e.CodeVerifyFailed, "boot final inventory changes the observed selected set")
	}
	if len(request.Publications) != len(available)+1 || len(request.Proofs) == 0 || len(request.Proofs) > 2*len(available)+1 {
		return StoredObject{}, e.New(e.CodeVerifyFailed, "boot final publication requires its bounded original roots and proofs")
	}
	inventory := bootInventory{SchemaVersion: bootInventorySchema, Expected: request.Expected, Uses: slices.Clone(request.Uses)}
	objects := map[string]bootStoredObject{}
	stored := map[string]StoredObject{}
	add := func(object Object, role string) error {
		previous, found := objects[object.SHA256]
		if found && previous.stored().Object != object {
			return e.New(e.CodeVerifyFailed, "shared boot bytes have conflicting original descriptions")
		}
		if !found {
			if len(objects) >= limits.Storage.MaxObjects-1 {
				return e.New(e.CodeEngineCapability, "boot final object inventory exceeds its reserved count")
			}
			previous = bootStoredObject{Key: object.Key, SHA256: object.SHA256, Size: object.Size, MediaType: object.MediaType, VersionID: "pending"}
		}
		if !slices.Contains(previous.Roles, role) {
			previous.Roles = append(previous.Roles, role)
			slices.Sort(previous.Roles)
		}
		objects[object.SHA256] = previous
		return nil
	}
	for index, record := range available {
		if record.Binding != request.Uses[index].Binding || record.URL != request.Uses[index].URL {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "boot final named binding differs from this phase's availability")
		}
		if err := add(record.Object.Object, "payload"); err != nil {
			return StoredObject{}, err
		}
		stored[record.Object.SHA256] = record.Object
	}
	publications := slices.Clone(request.Publications)
	slices.SortFunc(publications, func(a, b BootProofPublication) int { return strings.Compare(a.Reference, b.Reference) })
	requiredProofs := map[string]bool{}
	selectedNames := make([]string, len(available))
	localNames := map[string]string{}
	memberCount := 0
	for index, asset := range available {
		selectedNames[index] = asset.Binding.Output
		localNames[asset.Binding.Publication] = asset.Binding.Output
	}
	for _, publication := range publications {
		description, err := publication.Metadata.Description(publication.Reference)
		if err != nil {
			return StoredObject{}, err
		}
		outputs := []string{localNames[publication.Reference]}
		if publication.Reference == request.Expected.RootReference {
			outputs = slices.Clone(selectedNames)
		} else {
			if outputs[0] == "" || len(description.Outputs) != 1 || description.Outputs[0].Name != outputs[0] {
				return StoredObject{}, e.New(e.CodeVerifyFailed, "boot proof set contains a foreign or nonlocal original root")
			}
			for _, output := range description.Outputs {
				requiredProofs[output.Reference] = true
			}
		}
		requiredProofs[publication.Reference] = true
		record := bootMetadataRecord{Reference: publication.Reference, RootDigest: description.RootDigest, Outputs: outputs}
		if err := publication.Metadata.VisitObjects(ctx, func(_ context.Context, object PublicationObject, _ io.ReadSeeker) error {
			memberCount++
			if memberCount > limits.ControlObjects {
				return e.New(e.CodeEngineCapability, "boot original control uses exceed their Plan-derived reservation")
			}
			if err := add(Object{Key: mirrorBlobKey(object.SHA256), SHA256: object.SHA256, Size: object.Size, MediaType: MediaOctetStream}, "control"); err != nil {
				return err
			}
			record.Members = append(record.Members, mirrorMember{Path: object.Key, SHA256: object.SHA256})
			return nil
		}); err != nil {
			return StoredObject{}, err
		}
		slices.SortFunc(record.Members, func(a, b mirrorMember) int { return strings.Compare(a.Path, b.Path) })
		inventory.Publications = append(inventory.Publications, record)
	}
	proofBytes := map[string][]byte{}
	proofSubjects := map[string]bool{}
	var retainedProofBytes int64
	for _, proof := range request.Proofs {
		subject, signature, provenance, err := proof.Bundles()
		if err != nil {
			return StoredObject{}, err
		}
		if !requiredProofs[subject.Ref] || proofSubjects[subject.Ref] {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "boot proof has a repeated or foreign original subject")
		}
		proofSubjects[subject.Ref] = true
		binding := mirroredProof{Subject: mirrorSubject{Ref: subject.Ref, Digest: subject.Digest}, Signature: digestBytes(signature), Provenance: digestBytes(provenance)}
		for _, raw := range [][]byte{signature, provenance} {
			if len(raw) == 0 || int64(len(raw)) > maxMirrorProofBundleBytes {
				return StoredObject{}, e.New(e.CodeEngineCapability, "boot original proof exceeds its individual bound")
			}
			digest := digestBytes(raw)
			if _, exists := proofBytes[digest]; !exists {
				if int64(len(raw)) > maxMirrorProofBytes-retainedProofBytes {
					return StoredObject{}, e.New(e.CodeEngineCapability, "boot original proofs exceed their complete reservation")
				}
				retainedProofBytes += int64(len(raw))
			}
			if err := add(Object{Key: mirrorBlobKey(digest), SHA256: digest, Size: int64(len(raw)), MediaType: MediaOctetStream}, "proof"); err != nil {
				return StoredObject{}, err
			}
			proofBytes[digest] = raw
		}
		inventory.Proofs = append(inventory.Proofs, binding)
	}
	if len(proofSubjects) != len(requiredProofs) {
		return StoredObject{}, e.New(e.CodeProvenanceMissing, "boot proof set omits an exact original root or output descriptor")
	}
	slices.SortFunc(inventory.Proofs, func(a, b mirroredProof) int { return strings.Compare(a.Subject.Ref, b.Subject.Ref) })
	for _, object := range objects {
		if observation, found := stored[object.SHA256]; found {
			object.VersionID, object.ETag = observation.VersionID, observation.ETag
		}
		inventory.Objects = append(inventory.Objects, object)
	}
	slices.SortFunc(inventory.Objects, func(a, b bootStoredObject) int { return strings.Compare(a.SHA256, b.SHA256) })
	preflight, err := canonical.Marshal(inventory)
	if err != nil {
		return StoredObject{}, err
	}
	digest := digestBytes(preflight)
	locator := StoredObject{Object: Object{Key: bootInventoryKey(request.Expected.RootReference, digest), SHA256: digest, Size: int64(len(preflight)), MediaType: "application/json"}, VersionID: "pending"}
	if err := validateBootInventory(inventory, locator, request.Expected, limits); err != nil {
		return StoredObject{}, err
	}
	// Reserve the maximum encoded provider-pin overhead independently of the
	// final actual inventory size. No payload or proof write precedes this check.
	controlBound := int64(len(preflight)) + int64(len(inventory.Objects))*(2*1024*6+64)
	var total int64
	for _, object := range inventory.Objects {
		total += object.Size
	}
	if controlBound > maxMirrorInventoryBytes || controlBound > limits.Storage.MaxObjectBytes || controlBound > limits.Storage.MaxTotalBytes-total {
		return StoredObject{}, e.New(e.CodeEngineCapability, "boot inventory cannot reserve bounded provider pins")
	}
	put := func(ctx context.Context, digest string, source io.ReadSeeker) error {
		if _, exists := stored[digest]; exists {
			return nil
		}
		observation, err := putVerifiedMirrorObject(ctx, store, objects[digest].stored().Object, source)
		if err != nil {
			return err
		}
		if err := validateBootPin(observation); err != nil {
			return err
		}
		stored[digest] = observation
		return nil
	}
	for _, publication := range publications {
		if err := publication.Metadata.VisitObjects(ctx, func(ctx context.Context, object PublicationObject, source io.ReadSeeker) error {
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
	for index := range inventory.Objects {
		object := &inventory.Objects[index]
		observation, exists := stored[object.SHA256]
		if !exists {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "boot final inventory has an unchecked object")
		}
		object.VersionID, object.ETag = observation.VersionID, observation.ETag
	}
	raw, err := canonical.Marshal(inventory)
	if err != nil {
		return StoredObject{}, err
	}
	digest = digestBytes(raw)
	locator.Object = Object{Key: bootInventoryKey(request.Expected.RootReference, digest), SHA256: digest, Size: int64(len(raw)), MediaType: "application/json"}
	if err := validateBootInventory(inventory, locator, request.Expected, limits); err != nil {
		return StoredObject{}, err
	}
	return commitBootInventory(ctx, store, locator.Object, raw, request.Recovery)
}
