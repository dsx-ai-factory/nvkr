// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Structural wire fixture only. Original signatures and retained metadata are
// authenticated by the later consumer; these synthetic controls prove neither.
func bootInventoryFixture(t *testing.T) (bootInventory, BootInventoryExpectation, BootTransferLimits) {
	t.Helper()
	selection, _, bindings, limits := bootTransferFixture(t)
	request := bootPhaseRequestFixture(t, selection, limits, "https://boot.example.test")
	pin := "sha256:" + strings.Repeat("c", 64)
	expected := BootInventoryExpectation{RootReference: "registry.example.test/release@" + pin, Invocation: "https://github.com/example/nvkr/actions/runs/123/attempts/1",
		DefinitionDigest: "sha256:" + strings.Repeat("d", 64), EnvironmentDigest: "sha256:" + strings.Repeat("e", 64), PlanDigest: "sha256:" + strings.Repeat("f", 64), Selection: selection, Destination: request.Destination}
	inventory := bootInventory{SchemaVersion: bootInventorySchema, Expected: expected}
	objects := map[string]bootStoredObject{}
	add := func(digest string, size int64, role string) {
		object := objects[digest]
		if object.SHA256 == "" {
			object = bootStoredObject{Key: mirrorBlobKey(digest), SHA256: digest, Size: size, MediaType: MediaOctetStream, VersionID: "synthetic-version"}
		}
		if !slices.Contains(object.Roles, role) {
			object.Roles = append(object.Roles, role)
			slices.Sort(object.Roles)
		}
		objects[digest] = object
	}
	names := []string{}
	for _, binding := range bindings {
		names = append(names, binding.Output)
		location, err := bootServingURL(*expected.Destination.Serving, bootPayloadObject(binding))
		if err != nil {
			t.Fatal(err)
		}
		inventory.Uses = append(inventory.Uses, bootInventoryUse{Binding: binding, PreparedInvocation: expected.Invocation, SignedInvocation: expected.Invocation, URL: location})
		_, digest, _ := strings.Cut(binding.Publication, "@")
		inventory.Publications = append(inventory.Publications, bootMetadataRecord{Reference: binding.Publication, RootDigest: digest, Outputs: []string{binding.Output}, Members: []mirrorMember{{Path: "index.json", SHA256: digest}}})
		add(binding.SHA256, binding.Size, "payload")
	}
	inventory.Publications = append(inventory.Publications, bootMetadataRecord{Reference: expected.RootReference, RootDigest: pin, Outputs: names, Members: []mirrorMember{{Path: "index.json", SHA256: pin}}})
	signature, predicate := digestBytes([]byte("synthetic-signature")), digestBytes([]byte("synthetic-provenance"))
	for _, publication := range inventory.Publications {
		add(publication.RootDigest, 1, "control")
		inventory.Proofs = append(inventory.Proofs, mirroredProof{Subject: mirrorSubject{Ref: publication.Reference, Digest: publication.RootDigest}, Signature: signature, Provenance: predicate})
	}
	add(signature, 19, "proof")
	add(predicate, 20, "proof")
	for _, object := range objects {
		inventory.Objects = append(inventory.Objects, object)
	}
	slices.SortFunc(inventory.Objects, func(a, b bootStoredObject) int { return strings.Compare(a.SHA256, b.SHA256) })
	slices.SortFunc(inventory.Publications, func(a, b bootMetadataRecord) int { return strings.Compare(a.Reference, b.Reference) })
	slices.SortFunc(inventory.Proofs, func(a, b mirroredProof) int { return strings.Compare(a.Subject.Ref, b.Subject.Ref) })
	return inventory, expected, limits
}

func encodeBootInventoryFixture(t *testing.T, inventory bootInventory) ([]byte, StoredObject) {
	t.Helper()
	raw, err := canonical.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	digest := digestBytes(raw)
	return raw, StoredObject{Object: Object{Key: bootInventoryKey(inventory.Expected.RootReference, digest), SHA256: digest, Size: int64(len(raw)), MediaType: "application/json"}, ETag: `"synthetic"`}
}

func TestBootInventoryRejectsChangedExpectationsAndUnboundObjects(t *testing.T) {
	fixture, expected, limits := bootInventoryFixture(t)
	baseline, _ := encodeBootInventoryFixture(t, fixture)
	for _, tc := range []struct {
		name   string
		mutate func(*bootInventory)
	}{
		{"schema", func(i *bootInventory) { i.SchemaVersion = mirrorInventorySchema }},
		{"subject", func(i *bootInventory) {
			i.Expected.RootReference = "registry.example.test/foreign@sha256:" + strings.Repeat("a", 64)
		}},
		{"inputs", func(i *bootInventory) { i.Expected.PlanDigest = "sha256:" + strings.Repeat("a", 64) }},
		{"selection", func(i *bootInventory) { i.Expected.Selection.Disks[0].Output = "foreign" }},
		{"URL", func(i *bootInventory) { i.Uses[0].URL += "?token=synthetic" }},
		{"missing invocation", func(i *bootInventory) { i.Uses[0].SignedInvocation = "" }},
		{"extra object", func(i *bootInventory) {
			object := i.Objects[0]
			object.SHA256 = "sha256:" + strings.Repeat("0", 64)
			object.Key = mirrorBlobKey(object.SHA256)
			i.Objects = append([]bootStoredObject{object}, i.Objects...)
		}},
		{"role", func(i *bootInventory) { i.Objects[0].Roles = []string{"payload", "proof", "control"} }},
		{"weak pin", func(i *bootInventory) { i.Objects[0].VersionID = ""; i.Objects[0].ETag = `W/"weak"` }},
		{"root control", func(i *bootInventory) { i.Publications[0].Members[0].SHA256 = "sha256:" + strings.Repeat("a", 64) }},
		{"foreign member path", func(i *bootInventory) { i.Publications[0].Members[0].Path = "../../foreign" }},
		{"extra root selection", func(i *bootInventory) { i.Publications[0].Outputs = append(i.Publications[0].Outputs, "foreign") }},
		{"missing proof", func(i *bootInventory) { i.Proofs = i.Proofs[1:] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed bootInventory
			if err := canonical.UnmarshalExact(baseline, &changed); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&changed)
			raw, locator := encodeBootInventoryFixture(t, changed)
			_, err := decodeBootInventory(raw, locator, expected, limits)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
	raw, locator := encodeBootInventoryFixture(t, fixture)
	if _, err := decodeBootInventory(raw, locator, expected, limits); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		bytes.Replace(raw, []byte(`"schemaVersion":`), []byte(`"schemaVersion":"duplicate","schemaVersion":`), 1),
		bytes.Replace(raw, []byte(`"uses":`), []byte(`"unknown":true,"uses":`), 1),
		[]byte(`{"schemaVersion":"nvkr.dev/publication-mirror/v1"}`),
	} {
		locator.Size, locator.SHA256 = int64(len(raw)), digestBytes(raw)
		locator.Key = bootInventoryKey(expected.RootReference, locator.SHA256)
		_, err := decodeBootInventory(raw, locator, expected, limits)
		assertRegistryCode(t, err, e.CodeVerifyFailed)
	}
}
