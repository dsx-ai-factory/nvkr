// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BootTransferLimits is a description of the Plan's storage reservation. The
// lifecycle owner rederives ControlObjects from the complete compiled graph;
// serialized counters and caller claims do not establish that reservation.
// ControlObjects includes original control/evidence and proof objects, excluding
// raw payloads and the one final inventory. Shared payload bytes count once.
type BootTransferLimits struct {
	Storage        MirrorLimits `json:"storage"`
	ControlObjects int          `json:"controlObjects"`
}

// BootStoredAsset describes one exact logical use, including its original
// output binding. Equal bytes can share an object but never a logical output.
// This description is transport data; it grants no signature or release trust.
type BootStoredAsset struct {
	Binding BootAssetBinding `json:"binding"`
	Object  StoredObject     `json:"object"`
	URL     string           `json:"url"`
}

// admitBootStorage reserves the full control, proof and inventory pools before
// the first payload write. Finalization additionally checks actual unique bytes
// and counts against these same limits; it cannot accept transported counters.
func admitBootStorage(selection definition.BootSelection, bindings []BootAssetBinding, limits BootTransferLimits) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	if err := limits.Storage.Validate(); err != nil {
		return err
	}
	count := len(selection.Disks) + len(selection.Imagers)
	if len(bindings) != count || limits.ControlObjects <= 0 || limits.ControlObjects >= limits.Storage.MaxObjects {
		return e.New(e.CodeEngineCapability, "boot transfer requires its complete selected set and bounded control reservation")
	}
	reserved := int64(count+1)*maxMetadataBytes + maxMirrorProofBytes + maxMirrorInventoryBytes
	if reserved > limits.Storage.MaxTotalBytes || limits.Storage.MaxObjectBytes < maxMirrorInventoryBytes {
		return e.New(e.CodeEngineCapability, "boot transfer cannot reserve original controls, proofs and final inventory")
	}
	names := map[string]BootAssetBinding{}
	objects := map[string]int64{}
	for _, binding := range bindings {
		if _, exists := names[binding.Output]; exists {
			return e.New(e.CodeVerifyFailed, "boot transfer repeats a named output")
		}
		if err := ValidatePublicationReference(binding.Publication); err != nil {
			return err
		}
		if !canonical.IsDigest(binding.SHA256) || !canonical.IsDigest(binding.DescriptorDigest) || !canonical.IsDigest(binding.BuildDigest) || binding.Size <= 0 {
			return e.New(e.CodeVerifyFailed, "boot transfer requires exact original raw bindings")
		}
		names[binding.Output] = binding
		if size, shared := objects[binding.SHA256]; shared {
			if size != binding.Size {
				return e.New(e.CodeVerifyFailed, "shared boot bytes have conflicting sizes")
			}
			continue
		}
		if binding.Size > limits.Storage.MaxObjectBytes || binding.Size > limits.Storage.MaxTotalBytes-reserved {
			return e.New(e.CodeEngineCapability, "boot payloads exceed storage remaining after required reservations")
		}
		reserved += binding.Size
		objects[binding.SHA256] = binding.Size
	}
	if len(objects)+limits.ControlObjects+1 > limits.Storage.MaxObjects {
		return e.New(e.CodeEngineCapability, "boot transfer exceeds reserved complete object count")
	}
	for _, disk := range selection.Disks {
		binding := names[disk.Output]
		if binding.Kind != "disk-image" || binding.Platform != disk.Platform || binding.Member != "" || binding.Profile != "" {
			return e.New(e.CodeVerifyFailed, "boot disk binding differs from exact selection")
		}
	}
	for _, imager := range selection.Imagers {
		binding := names[imager.Output]
		if binding.Kind != "executable" || binding.Platform != imager.Platform || binding.Member != imager.Member || binding.Profile != imager.Profile {
			return e.New(e.CodeVerifyFailed, "boot imager binding differs from exact selection")
		}
	}
	return nil
}

// copyBootAssets is the byte-only preparation operation. Its caller owns the
// fresh phase/session, authenticated source receipts and serving client. It
// returns descriptions only after every exact raw object passes pinned S3 and
// independent HTTPS readback. It never publishes controls or an inventory.
func copyBootAssets(ctx context.Context, store ObjectStore, reader *bootServingClient, selection definition.BootSelection,
	assets []*BootAsset, limits BootTransferLimits,
) ([]BootStoredAsset, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if _, err := readbudget.Require(ctx); err != nil {
		return nil, err
	}
	if store == nil || reader == nil {
		return nil, e.New(e.CodeEngineCapability, "boot transfer requires an admitted store and independent serving reader")
	}
	bindings := make([]BootAssetBinding, len(assets))
	byName := make(map[string]*BootAsset, len(assets))
	for index, asset := range assets {
		binding, err := asset.Binding()
		if err != nil {
			return nil, err
		}
		bindings[index] = binding
		byName[binding.Output] = asset
	}
	if err := admitBootStorage(selection, bindings, limits); err != nil {
		return nil, err
	}
	slices.SortFunc(bindings, func(a, b BootAssetBinding) int { return strings.Compare(a.Output, b.Output) })
	stored := map[string]StoredObject{}
	locations := map[string]string{}
	result := make([]BootStoredAsset, 0, len(bindings))
	for _, binding := range bindings {
		object, exists := stored[binding.SHA256]
		err := byName[binding.Output].Visit(ctx, func(ctx context.Context, actual BootAssetBinding, source io.ReadSeeker) error {
			if actual != binding {
				return e.New(e.CodeVerifyFailed, "boot asset changed after storage admission")
			}
			if exists {
				return nil
			}
			var err error
			object, err = putVerifiedMirrorObject(ctx, store, bootPayloadObject(binding), source)
			return err
		})
		if err != nil {
			return nil, err
		}
		if !exists {
			if err := validateBootPin(object); err != nil {
				return nil, err
			}
			location, err := reader.verify(ctx, object.Object)
			if err != nil {
				return nil, err
			}
			stored[binding.SHA256], locations[binding.SHA256] = object, location
		}
		result = append(result, BootStoredAsset{Binding: binding, Object: object, URL: locations[binding.SHA256]})
	}
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func bootPayloadObject(binding BootAssetBinding) Object {
	return Object{Key: mirrorBlobKey(binding.SHA256), SHA256: binding.SHA256, Size: binding.Size, MediaType: MediaOctetStream}
}

func validateBootPin(stored StoredObject) error {
	if len(stored.VersionID) > 1024 || len(stored.ETag) > 1024 ||
		!utf8.ValidString(stored.VersionID) || strings.ContainsFunc(stored.VersionID, unicode.IsControl) || stored.VersionID == "null" ||
		(stored.ETag != "" && !validObjectETag(stored.ETag)) || (stored.VersionID == "" && stored.ETag == "") {
		return e.New(e.CodeVerifyFailed, "boot object requires an exact version or strong validator")
	}
	return nil
}

// recheckBootAssets treats transported records only as expectations. Admission
// checks every logical binding/mapping before any read; there is no fallback
// from a denied exact S3 pin to the current object or its HTTPS path.
func recheckBootAssets(ctx context.Context, store ObjectStore, reader *bootServingClient, selection definition.BootSelection,
	expected []BootAssetBinding, records []BootStoredAsset, limits BootTransferLimits,
) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if _, err := readbudget.Require(ctx); err != nil {
		return err
	}
	if store == nil || reader == nil || len(expected) != len(records) {
		return e.New(e.CodeVerifyFailed, "boot recheck requires the complete expected selected set")
	}
	if err := admitBootStorage(selection, expected, limits); err != nil {
		return err
	}
	byName := make(map[string]BootAssetBinding, len(expected))
	for _, binding := range expected {
		byName[binding.Output] = binding
	}
	objects := map[string]StoredObject{}
	previous := ""
	for _, record := range records {
		binding, found := byName[record.Binding.Output]
		if !found || binding != record.Binding || binding.Output <= previous || record.Object.Object != bootPayloadObject(binding) {
			return e.New(e.CodeVerifyFailed, "boot record differs from exact expected output or raw bytes")
		}
		previous = binding.Output
		if err := validateBootPin(record.Object); err != nil {
			return err
		}
		location, err := bootServingURL(reader.config, record.Object.Object)
		if err != nil || location != record.URL {
			return e.New(e.CodeVerifyFailed, "boot record differs from independently authorized serving configuration")
		}
		if prior, shared := objects[binding.SHA256]; shared && prior != record.Object {
			return e.New(e.CodeVerifyFailed, "shared boot payload has conflicting destination pins")
		}
		objects[binding.SHA256] = record.Object
	}
	checked := map[string]bool{}
	for _, record := range records {
		if checked[record.Object.SHA256] {
			continue
		}
		if err := readMirrorObject(ctx, store, record.Object, io.Discard); err != nil {
			return err
		}
		if _, err := reader.verify(ctx, record.Object.Object); err != nil {
			return err
		}
		checked[record.Object.SHA256] = true
	}
	return deliveryContext(ctx)
}
