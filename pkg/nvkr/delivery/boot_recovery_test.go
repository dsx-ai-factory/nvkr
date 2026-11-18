// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type bootRecoveryStore struct {
	mirrorMemoryStore
	lostAck bool
	denied  bool
	pins    []StoredObject
}

func (store *bootRecoveryStore) PutIfAbsent(ctx context.Context, object Object, source io.ReadSeeker) (StoredObject, error) {
	observed, err := store.mirrorMemoryStore.PutIfAbsent(ctx, object, source)
	if err == nil && store.lostAck {
		return StoredObject{}, e.New(e.CodeTimeout, "synthetic acknowledgement lost")
	}
	return observed, err
}
func (store *bootRecoveryStore) OpenPinned(ctx context.Context, object StoredObject) (io.ReadCloser, error) {
	store.pins = append(store.pins, object)
	if store.denied {
		return nil, e.New(e.CodeVerifyFailed, "synthetic exact version denied")
	}
	return store.mirrorMemoryStore.OpenPinned(ctx, object)
}

func TestBootInventoryRecoveryRetainsExactAttempt(t *testing.T) {
	raw := []byte("synthetic canonical inventory")
	object := Object{Key: "inventory", SHA256: digestBytes(raw), Size: int64(len(raw)), MediaType: "application/json"}
	for _, scenario := range []string{"lost acknowledgement", "readback denied", "changed inventory"} {
		t.Run(scenario, func(t *testing.T) {
			store := &bootRecoveryStore{lostAck: scenario == "lost acknowledgement", denied: scenario != "lost acknowledgement"}
			got, err := commitBootInventory(t.Context(), store, object, raw, nil)
			var failed *BootInventoryError
			if got != (StoredObject{}) || !errors.As(err, &failed) || failed.Attempt.Object != object {
				t.Fatalf("lost exact attempted object: %v, %v", got, err)
			}
			attempt := failed.Attempt
			if (attempt.VersionID == "") != store.lostAck {
				t.Fatal("observed pin was lost or invented")
			}
			store.lostAck = false
			writes := len(store.writes)
			if scenario == "changed inventory" {
				changed := object
				changed.SHA256 = digestBytes([]byte("changed"))
				_, err = commitBootInventory(t.Context(), store, changed, []byte("changed"), &attempt)
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if !errors.As(err, &failed) || failed.Attempt != attempt || len(store.writes) != writes {
					t.Fatal("changed reconstruction replaced the attempted locator or wrote")
				}
				return
			}
			if scenario == "readback denied" {
				_, err = commitBootInventory(t.Context(), store, object, raw, &attempt)
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if len(store.writes) != writes || store.pins[len(store.pins)-1] != attempt {
					t.Fatal("denied exact pin fell back to conditional creation or a different version")
				}
			}
			store.denied = false
			got, err = commitBootInventory(t.Context(), store, object, raw, &attempt)
			if err != nil {
				t.Fatal(err)
			}
			if got.Object != object || got.VersionID == "" || len(store.pins) == 0 {
				t.Fatal("recovery skipped pinned readback")
			}
			if scenario == "readback denied" && (got != attempt || len(store.writes) != writes) {
				t.Fatal("known-pin recovery changed the object or wrote")
			}
			if scenario == "lost acknowledgement" && len(store.writes) != writes+1 {
				t.Fatal("unknown-pin recovery did not reconcile conditional creation")
			}
		})
	}
}

func TestBootInventoryAttemptAdmission(t *testing.T) {
	reference := "registry.example.test/product/releases@" + digestBytes([]byte("root"))
	digest := digestBytes([]byte("inventory"))
	valid := StoredObject{Object: Object{Key: bootInventoryKey(reference, digest), SHA256: digest, Size: 9, MediaType: "application/json"}}
	for _, test := range []struct {
		name  string
		edit  func(*StoredObject)
		valid bool
	}{
		{"unknown pin", func(*StoredObject) {}, true},
		{"known version", func(a *StoredObject) { a.VersionID = "version" }, true},
		{"strong validator", func(a *StoredObject) { a.ETag = `"validator"` }, true},
		{"wrong root", func(a *StoredObject) { a.Key = "foreign" }, false},
		{"wrong media", func(a *StoredObject) { a.MediaType = MediaOctetStream }, false},
		{"oversized", func(a *StoredObject) { a.Size = maxMirrorInventoryBytes + 1 }, false},
		{"null version", func(a *StoredObject) { a.VersionID = "null" }, false},
		{"weak validator", func(a *StoredObject) { a.ETag = `W/"validator"` }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempt := valid
			test.edit(&attempt)
			err := ValidateBootInventoryAttempt(reference, attempt)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}
