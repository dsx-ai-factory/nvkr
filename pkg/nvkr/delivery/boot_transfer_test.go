// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func bootTransferFixture(t *testing.T, sources ...map[string]string) (definition.BootSelection, []*BootAsset, []BootAssetBinding, BootTransferLimits) {
	t.Helper()
	selection := definition.BootSelection{SchemaVersion: definition.BootSelectionSchemaVersion,
		Disks:   []definition.BootDisk{{Output: "node-image", Platform: "linux/amd64"}},
		Imagers: []definition.BootImager{{Output: "tools", Member: "cli", Platform: "linux/amd64", Profile: "test-profile-v1"}}}
	request, _ := diskPublicationRequest(t, "amd64")
	root := filepath.Join(t.TempDir(), "disk")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) > 0 {
		sources[0][selection.Disks[0].Output] = root
	}
	disk, err := openDiskPublicationFixture(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	diskAsset, err := disk.OpenBootDisk(t.Context(), "registry.example.test/disk@"+result.RootDigest, selection.Disks[0])
	if err != nil {
		t.Fatal(err)
	}
	root, digest := bootImagerPublicationFixture(t)
	if len(sources) > 0 {
		sources[0][selection.Imagers[0].Output] = root
	}
	imager, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = imager.Close() })
	imagerAsset, err := imager.OpenBootImager(t.Context(), "registry.example.test/imager@"+digest, selection.Imagers[0])
	if err != nil {
		t.Fatal(err)
	}
	assets := []*BootAsset{diskAsset, imagerAsset}
	bindings := make([]BootAssetBinding, len(assets))
	for i, asset := range assets {
		t.Cleanup(func() { _ = asset.Close() })
		bindings[i], err = asset.Binding()
		if err != nil {
			t.Fatal(err)
		}
	}
	return selection, assets, bindings, BootTransferLimits{Storage: MirrorLimits{MaxObjectBytes: 1 << 30, MaxTotalBytes: 4 << 30, MaxObjects: 128}, ControlObjects: 64}
}

func TestBootTransferCopiesOnlyRawAssetsAndRechecksExactPins(t *testing.T) {
	selection, assets, bindings, limits := bootTransferFixture(t)
	store := &mirrorMemoryStore{}
	servingCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		servingCalls++
		raw, found := store.data[strings.TrimPrefix(r.URL.Path, "/boot/")]
		if !found {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = original }()
	ctx, _, err := readbudget.WithLimit(t.Context(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newBootServingClient(ctx, definition.BootServing{Profile: definition.BootServingProfile, Origin: server.URL, Prefix: "/boot", ReaderMode: "anonymous"}, BootReaderCredential{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	records, err := copyBootAssets(ctx, store, reader, selection, assets, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(store.writes) != 2 || store.reads != 2 || servingCalls != 2 {
		t.Fatalf("unexpected transfer count: records=%d writes=%d reads=%d serving=%d", len(records), len(store.writes), store.reads, servingCalls)
	}
	for _, asset := range assets {
		if err := asset.Visit(ctx, func(_ context.Context, binding BootAssetBinding, source io.ReadSeeker) error {
			raw, err := io.ReadAll(source)
			if err != nil {
				return err
			}
			if string(store.data[mirrorBlobKey(binding.SHA256)]) != string(raw) {
				t.Error("stored wrapper or archive instead of raw selected bytes")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := recheckBootAssets(ctx, store, reader, selection, bindings, records, limits); err != nil {
		t.Fatal(err)
	}
	if store.reads != 4 || servingCalls != 4 {
		t.Fatal("final phase did not repeat both independent reads")
	}
	// A shared digest cannot bypass the second owner's exact named-use check.
	// Deliberately inconsistent internal fixture: the retained disk publication
	// has node-image only, while this copied description claims its sibling.
	sharedSelection := selection.Clone()
	sharedSelection.Disks = append(sharedSelection.Disks, definition.BootDisk{Output: "sibling", Platform: "linux/amd64"})
	sharedBinding := bindings[0]
	sharedBinding.Output = "sibling"
	sharedAsset := &BootAsset{state: &bootAssetState{binding: sharedBinding, publication: assets[0].state.publication}}
	sharedAssets := append(slices.Clone(assets), sharedAsset)
	if got, err := copyBootAssets(ctx, store, reader, sharedSelection, sharedAssets, limits); err == nil || got != nil {
		t.Fatal("shared bytes bypassed the sibling's retained raw binding")
	} else {
		assertRegistryCode(t, err, e.CodeVerifyFailed)
	}
	for _, tc := range []struct {
		name string
		edit func([]BootStoredAsset)
	}{
		{"sibling", func(r []BootStoredAsset) { r[0].Binding.Output = "other" }},
		{"URL", func(r []BootStoredAsset) { r[0].URL += "?version=x" }},
		{"weak pin", func(r []BootStoredAsset) { r[0].Object.VersionID = ""; r[0].Object.ETag = `W/"weak"` }},
		{"null version", func(r []BootStoredAsset) { r[0].Object.VersionID = "null" }},
		{"member", func(r []BootStoredAsset) { r[1].Binding.Member = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := slices.Clone(records)
			tc.edit(changed)
			before := store.reads
			assertRegistryCode(t, recheckBootAssets(ctx, store, reader, selection, bindings, changed, limits), e.CodeVerifyFailed)
			if store.reads != before {
				t.Fatal("read occurred before complete admission")
			}
		})
	}
	delete(store.data, records[0].Object.Key)
	before := servingCalls
	assertRegistryCode(t, recheckBootAssets(ctx, store, reader, selection, bindings, records, limits), e.CodeVerifyFailed)
	if servingCalls != before {
		t.Fatal("denied exact object fell back to HTTPS")
	}
}

func TestBootTransferReservesControlsBeforePayloadWrites(t *testing.T) {
	selection, _, bindings, limits := bootTransferFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*definition.BootSelection, []BootAssetBinding, *BootTransferLimits)
		code   e.Code
	}{
		{"control bytes", func(_ *definition.BootSelection, _ []BootAssetBinding, l *BootTransferLimits) {
			l.Storage.MaxTotalBytes = 1 << 30
		}, e.CodeEngineCapability},
		{"control count", func(_ *definition.BootSelection, _ []BootAssetBinding, l *BootTransferLimits) { l.ControlObjects = 127 }, e.CodeEngineCapability},
		{"inventory object", func(_ *definition.BootSelection, _ []BootAssetBinding, l *BootTransferLimits) {
			l.Storage.MaxObjectBytes = 1 << 20
		}, e.CodeEngineCapability},
		{"duplicate named use", func(_ *definition.BootSelection, b []BootAssetBinding, _ *BootTransferLimits) { b[1] = b[0] }, e.CodeVerifyFailed},
		{"missing exact use", func(s *definition.BootSelection, _ []BootAssetBinding, _ *BootTransferLimits) {
			s.Disks[0].Output = "sibling"
		}, e.CodeVerifyFailed},
		{"wrong profile", func(_ *definition.BootSelection, b []BootAssetBinding, _ *BootTransferLimits) { b[1].Profile = "other" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, l := selection.Clone(), slices.Clone(bindings), limits
			tc.change(&s, b, &l)
			assertRegistryCode(t, admitBootStorage(s, b, l), tc.code)
		})
	}
}
