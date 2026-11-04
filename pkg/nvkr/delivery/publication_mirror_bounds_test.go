// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func mirrorInventoryFixture(t *testing.T, disk bool) (*mirrorMemoryStore, StoredObject, mirrorInventory) {
	t.Helper()
	root, digest := releasePublicationFixture(t)
	if disk {
		root = pullExecutableFixture(t).root
		_, raw, err := readIndex(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		digest = digestBytes(raw)
	}
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publication.Close() })
	reference := "registry.example.test/original@" + digest
	store := &mirrorMemoryStore{}
	locator, err := MirrorPublications(t.Context(), store, MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Proofs: []*sign.VerifiedOCIEvidence{mirrorFixtureProof(t, mirrorFixtureSigner(t), reference)}, Limits: mirrorFixtureLimits()})
	if err != nil {
		t.Fatal(err)
	}
	var inventory mirrorInventory
	if err := canonical.UnmarshalExact(store.data[locator.Key], &inventory); err != nil {
		t.Fatal(err)
	}
	return store, locator, inventory
}

func replaceMirrorInventory(t *testing.T, store *mirrorMemoryStore, inventory mirrorInventory) StoredObject {
	t.Helper()
	sort.Slice(inventory.Proofs, func(i, j int) bool { return inventory.Proofs[i].Subject.Ref < inventory.Proofs[j].Subject.Ref })
	sort.Slice(inventory.Objects, func(i, j int) bool { return inventory.Objects[i].SHA256 < inventory.Objects[j].SHA256 })
	raw, err := canonical.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	root := strings.SplitN(inventory.RootReference, "@", 2)[1]
	object := Object{Key: "publications/" + strings.TrimPrefix(root, "sha256:") + "/" + strings.TrimPrefix(digestBytes(raw), "sha256:") + ".json", SHA256: digestBytes(raw), Size: int64(len(raw)), MediaType: "application/json"}
	locator, err := store.PutIfAbsent(t.Context(), object, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return locator
}

func TestRestoreMirrorReadsSharedProofBytesOnce(t *testing.T) {
	store, locator, inventory := mirrorInventoryFixture(t, false)
	store.reads = 0
	if _, err := RestoreMirror(t.Context(), store, locator, filepath.Join(t.TempDir(), "baseline"), mirrorFixtureLimits()); err != nil {
		t.Fatal(err)
	}
	baselineReads := store.reads
	original := inventory.Proofs[0]
	for i := range 200 {
		alias := original
		alias.Subject.Ref = fmt.Sprintf("registry.example.test/alias-%03d@%s", i, original.Subject.Digest)
		inventory.Proofs = append(inventory.Proofs, alias)
	}
	locator = replaceMirrorInventory(t, store, inventory)
	store.reads = 0
	restored, err := RestoreMirror(t.Context(), store, locator, filepath.Join(t.TempDir(), "aliases"), mirrorFixtureLimits())
	if err != nil {
		t.Fatal(err)
	}
	if store.reads != baselineReads || len(restored.Proofs) != 201 {
		t.Fatalf("shared proof amplification: reads=%d baseline=%d proofs=%d", store.reads, baselineReads, len(restored.Proofs))
	}
	for _, proof := range restored.Proofs[1:] {
		if &proof.Signature[0] != &restored.Proofs[0].Signature[0] || &proof.Provenance[0] != &restored.Proofs[0].Provenance[0] {
			t.Fatal("shared proof bytes were duplicated in retained memory")
		}
	}
}

func TestRestoreMirrorAdmitsRetainedProofBytesBeforePayloadReads(t *testing.T) {
	for _, scenario := range []string{"bundle", "aggregate"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, inventory := mirrorInventoryFixture(t, false)
			proof := inventory.Proofs[0]
			if scenario == "bundle" {
				for i := range inventory.Objects {
					if inventory.Objects[i].SHA256 == proof.Signature {
						inventory.Objects[i].Size = maxMirrorProofBundleBytes + 1
					}
				}
			} else {
				for i := range 2 {
					digest := digestBytes([]byte(fmt.Sprintf("additional proof %d", i)))
					inventory.Objects = append(inventory.Objects, mirrorObject(StoredObject{Object: Object{Key: mirrorBlobKey(digest), SHA256: digest, Size: maxMirrorProofBundleBytes, MediaType: MediaOctetStream}}))
					alias := proof
					alias.Subject.Ref = fmt.Sprintf("registry.example.test/extra-%d@%s", i, proof.Subject.Digest)
					alias.Signature = digest
					inventory.Proofs = append(inventory.Proofs, alias)
				}
			}
			locator := replaceMirrorInventory(t, store, inventory)
			store.reads = 0
			out := filepath.Join(t.TempDir(), "refused")
			result, err := RestoreMirror(t.Context(), store, locator, out, mirrorFixtureLimits())
			code := e.CodeEngineCapability
			if scenario == "bundle" {
				code = e.CodeSignatureInvalid
			}
			assertRegistryCode(t, err, code)
			if result != nil || store.reads != 1 {
				t.Fatalf("proof refusal followed blob I/O: result=%v reads=%d", result, store.reads)
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("proof refusal exposed restored output: %v", err)
			}
		})
	}
}

func TestRestoreMirrorRefusesChangedDiskOutputMapping(t *testing.T) {
	store, _, inventory := mirrorInventoryFixture(t, true)
	if len(inventory.Publications[0].DiskPayloads) != 1 {
		t.Fatal("fixture lacks one checked raw disk output")
	}
	inventory.Publications[0].DiskPayloads[0].Name = "different-output"
	locator := replaceMirrorInventory(t, store, inventory)
	out := filepath.Join(t.TempDir(), "refused")
	result, err := RestoreMirror(t.Context(), store, locator, out, mirrorFixtureLimits())
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if result != nil {
		t.Fatal("changed disk mapping returned a restored publication")
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("changed disk mapping exposed output: %v", err)
	}
}

func TestMirrorAdmitsRetainedProofBytesBeforeWrites(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	verifier := mirrorFixtureSigner(t)
	reference := "registry.example.test/original@" + digest
	request := MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Limits: mirrorFixtureLimits()}
	for _, name := range []string{"original", "alias-1", "alias-2"} {
		subject := sign.Subject{Ref: "registry.example.test/" + name + "@" + digest, Digest: digest}
		signature := append(mirrorFixtureBundle(t, digest, sign.CosignSignaturePredicateType), bytes.Repeat([]byte(" "), 3<<20)...)
		provenance := append(mirrorFixtureBundle(t, digest, sign.ProvenancePredicateType), bytes.Repeat([]byte(" "), 3<<20)...)
		_, proof, err := verifier.VerifyBundles(t.Context(), subject, sign.VerifyPolicy{IdentityRegexp: "^" + mirrorTestIdentity + "$", Issuer: mirrorTestIssuer, RequireAttestation: true}, signature, provenance)
		if err != nil {
			t.Fatal(err)
		}
		request.Proofs = append(request.Proofs, proof)
	}
	store := &mirrorMemoryStore{}
	locator, err := MirrorPublications(t.Context(), store, request)
	assertRegistryCode(t, err, e.CodeEngineCapability)
	if locator.Key != "" || len(store.writes) != 0 {
		t.Fatal("over-budget retained proofs wrote provider objects")
	}
}

func TestMirrorInventoryDefaultCountFitsWorstCaseProviderPins(t *testing.T) {
	for _, count := range []int{8192, maxMirrorObjects} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			inventory := mirrorInventory{SchemaVersion: mirrorInventorySchema, RootReference: "registry.example.test/original@sha256:" + strings.Repeat("0", 64)}
			publication := mirroredPublication{Reference: inventory.RootReference, RootDigest: "sha256:" + strings.Repeat("0", 64)}
			for i := range count - 1 { // The final transport inventory is also an object.
				digest := fmt.Sprintf("sha256:%064x", i)
				inventory.Objects = append(inventory.Objects, mirrorObject(StoredObject{Object: Object{Key: mirrorBlobKey(digest), SHA256: digest, Size: 1, MediaType: MediaOctetStream}}))
				publication.Members = append(publication.Members, mirrorMember{Path: mirrorBlobKey(digest), SHA256: digest})
			}
			inventory.Publications = []mirroredPublication{publication}
			limits := MirrorLimits{MaxObjectBytes: 64 << 30, MaxTotalBytes: 256 << 30, MaxObjects: count}
			err := admitMirrorInventory(inventory, limits, int64(count-1))
			if count == maxMirrorObjects {
				assertRegistryCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Verify the reservation with the canonical codec's actual worst-case
			// escaping rather than assuming JSON encodes opaque pins at byte length.
			raw, err := canonical.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}
			empty, err := canonical.Marshal(inventory.Objects[0])
			if err != nil {
				t.Fatal(err)
			}
			worst := inventory.Objects[0]
			worst.VersionID, worst.ETag = strings.Repeat("\x01", 1024), strings.Repeat("\x01", 1024)
			encoded, err := canonical.Marshal(worst)
			if err != nil {
				t.Fatal(err)
			}
			actualSize := int64(len(raw)) + int64(len(inventory.Objects))*int64(len(encoded)-len(empty))
			if actualSize > maxMirrorInventoryBytes {
				t.Fatalf("admitted default inventory exceeds fixed control bound: bytes=%d", actualSize)
			}
		})
	}
}

func TestMirrorGenericStorePerformsFullReadback(t *testing.T) {
	store := &mirrorMemoryStore{}
	raw := []byte("exact original bytes")
	object := Object{Key: "blob", SHA256: digestBytes(raw), Size: int64(len(raw)), MediaType: MediaOctetStream}
	for attempt := range 2 {
		if _, err := putVerifiedMirrorObject(t.Context(), store, object, bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		if store.reads != attempt+1 {
			t.Fatal("generic provider bypassed full pinned readback")
		}
	}
}

func TestMirrorReusesOneExactS3ReadbackWithoutUploadingExistingParts(t *testing.T) {
	for _, size := range []int{321, 5<<20 + 731} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store, fixture := newObjectFixture(t, "")
			raw := bytes.Repeat([]byte{0x37}, size)
			object := objectDescription(raw)
			fixture.mu.Lock()
			fixture.data[object.Key] = bytes.Clone(raw)
			fixture.mu.Unlock()
			if _, err := putVerifiedMirrorObject(t.Context(), store, object, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.calls["GET"] != 1 || fixture.calls["part"] != 0 || fixture.calls["initiate"] != 0 {
				t.Fatalf("existing exact object was uploaded or read twice: calls=%v", fixture.calls)
			}
		})
	}
}
