// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type mirrorMemoryStore struct {
	data    map[string][]byte
	writes  []Object
	corrupt string
	reads   int
}

func (store *mirrorMemoryStore) PutIfAbsent(_ context.Context, object Object, source io.ReadSeeker) (StoredObject, error) {
	raw, err := io.ReadAll(source)
	if err != nil {
		return StoredObject{}, err
	}
	if store.data == nil {
		store.data = map[string][]byte{}
	}
	if previous, exists := store.data[object.Key]; exists && !bytes.Equal(previous, raw) {
		return StoredObject{}, e.New(e.CodeImmutableViolation, "fixture conditional write conflict")
	}
	store.data[object.Key] = raw
	store.writes = append(store.writes, object)
	if store.corrupt == "write" {
		store.data[object.Key] = append(bytes.Clone(raw), '!')
	}
	if store.corrupt == "same-size" && len(raw) > 0 {
		store.data[object.Key] = bytes.Clone(raw)
		store.data[object.Key][0] ^= 1
	}
	return StoredObject{Object: object, VersionID: "synthetic-version", ETag: "\"synthetic-etag\""}, nil
}
func (store *mirrorMemoryStore) OpenPinned(_ context.Context, object StoredObject) (io.ReadCloser, error) {
	store.reads++
	raw, exists := store.data[object.Key]
	if !exists {
		return nil, e.New(e.CodeVerifyFailed, "fixture object missing")
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func TestRestoreMirrorAdmitsConsumerInventoryBudgetBeforeRead(t *testing.T) {
	for _, scenario := range []string{"object", "total"} {
		t.Run(scenario, func(t *testing.T) {
			limits := mirrorFixtureLimits()
			if scenario == "object" {
				limits.MaxObjectBytes = 1
			} else {
				limits.MaxTotalBytes = 1
			}
			store := &mirrorMemoryStore{}
			result, err := RestoreMirror(t.Context(), store, StoredObject{Object: Object{Key: "irrelevant", SHA256: digestBytes([]byte("inventory")), Size: 9, MediaType: "application/json"}}, filepath.Join(t.TempDir(), "no-output"), limits)
			assertRegistryCode(t, err, e.CodeEngineCapability)
			if result != nil || store.reads != 0 {
				t.Fatal("over-budget inventory triggered provider reads")
			}
		})
	}
}
func (*mirrorMemoryStore) Close() error { return nil }

const mirrorTestIdentity = "mirror@example.test"
const mirrorTestIssuer = "https://issuer.example.test"

func mirrorFixtureSigner(t *testing.T) *sign.CosignOCISigner {
	t.Helper()
	verifier, err := sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: "synthetic-cosign", Identity: "^" + mirrorTestIdentity + "$", Issuer: mirrorTestIssuer, Runner: sign.RunnerFunc(func(_ context.Context, _ string, args []string, stdout, _ io.Writer) error {
		if len(args) > 0 && args[0] == "version" {
			_, err := io.WriteString(stdout, `{"gitVersion":"v3.1.3"}`)
			return err
		}
		if len(args) == 0 || args[0] != "verify-blob-attestation" {
			t.Fatalf("unexpected verification subprocess %v", args)
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}
func mirrorFixtureProof(t *testing.T, verifier *sign.CosignOCISigner, reference string) *sign.VerifiedOCIEvidence {
	t.Helper()
	digest := strings.SplitN(reference, "@", 2)[1]
	signature := mirrorFixtureBundle(t, digest, sign.CosignSignaturePredicateType)
	provenance := mirrorFixtureBundle(t, digest, sign.ProvenancePredicateType)
	_, proof, err := verifier.VerifyBundles(t.Context(), sign.Subject{Ref: reference, Digest: digest}, sign.VerifyPolicy{IdentityRegexp: "^" + mirrorTestIdentity + "$", Issuer: mirrorTestIssuer, RequireAttestation: true}, signature, provenance)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}
func mirrorFixtureBundle(t *testing.T, digest, predicateType string) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := asn1.Marshal(mirrorTestIssuer)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic mirror contract fixture"}, NotBefore: time.Unix(1, 0), NotAfter: time.Unix(4102444800, 0), KeyUsage: x509.KeyUsageDigitalSignature, EmailAddresses: []string{mirrorTestIdentity}, ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}, Value: issuer}}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": predicateType, "subject": []any{map[string]any{"name": "original", "digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{"certificate": map[string]string{"rawBytes": base64.StdEncoding.EncodeToString(der)}}, "dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "c3ludGhldGlj"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func mirrorFixtureLimits() MirrorLimits {
	return MirrorLimits{MaxObjectBytes: 1 << 30, MaxTotalBytes: 8 << 30, MaxObjects: 4096}
}

func TestMirrorRestoresOriginalPublicationsAndProofs(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) string
	}{
		{"executable+SBOM", func(t *testing.T) string { root, _ := releasePublicationFixture(t); return root }},
		{"QCOW2", func(t *testing.T) string { return pullExecutableFixture(t).root }},
		{"Helm", func(t *testing.T) string { return packagePullFixture(t, outputFormatHelm, "exact").root }},
		{"APT", func(t *testing.T) string { return packagePullFixture(t, "apt-repository", "exact").root }},
		{"aggregate image", func(t *testing.T) string { root, _, _ := registryImageIndexFixture(t); return root }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := test.fixture(t)
			_, index, err := readIndex(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			digest := digestBytes(index)
			reference := "registry.example.test/original@" + digest
			publication, err := OpenPublication(t.Context(), source, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			verifier := mirrorFixtureSigner(t)
			proof := mirrorFixtureProof(t, verifier, reference)
			store := &mirrorMemoryStore{}
			locator, err := MirrorPublications(t.Context(), store, MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Proofs: []*sign.VerifiedOCIEvidence{proof}, Limits: mirrorFixtureLimits()})
			if err != nil {
				t.Fatal(err)
			}
			if len(store.writes) == 0 || store.writes[len(store.writes)-1].Key != locator.Key {
				t.Fatal("mirror inventory was not the final conditional write")
			}
			if err := os.Rename(source, source+"-replaced"); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "restored")
			restored, err := RestoreMirror(t.Context(), store, locator, out, mirrorFixtureLimits())
			if err != nil {
				t.Fatal(err)
			}
			if len(restored.Publications) != 1 || restored.Publications[0].RootDigest != digest || restored.RootReference != reference || len(restored.Proofs) != 1 {
				t.Fatal("mirror changed original source identities")
			}
			_, signature, provenance, err := proof.Bundles()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(signature, restored.Proofs[0].Signature) || !bytes.Equal(provenance, restored.Proofs[0].Provenance) {
				t.Fatal("mirror rewrote original proof bytes")
			}
			if _, _, err := verifier.VerifyBundles(t.Context(), restored.Proofs[0].Subject, sign.VerifyPolicy{IdentityRegexp: "^" + mirrorTestIdentity + "$", Issuer: mirrorTestIssuer, RequireAttestation: true}, restored.Proofs[0].Signature, restored.Proofs[0].Provenance); err != nil {
				t.Fatal(err)
			}
			checked, err := OpenPublication(t.Context(), restored.Publications[0].Path, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer checked.Close()
			before, err := publication.Description(reference)
			if err != nil {
				t.Fatal(err)
			}
			after, err := checked.Description(reference)
			if err != nil {
				t.Fatal(err)
			}
			if before.RootDigest != after.RootDigest || before.Metadata.ContentSubject != after.Metadata.ContentSubject {
				t.Fatal("restored layout changed content identity")
			}
			for _, disk := range restored.Publications[0].DiskPayloads {
				if digestBytes(store.data[disk.Key]) != disk.SHA256 || int64(len(store.data[disk.Key])) != disk.Size {
					t.Fatal("QCOW2 mapping does not address the original raw blob")
				}
			}
		})
	}
}

func TestMirrorRejectsInvalidPreflightBeforeWrites(t *testing.T) {
	for _, scenario := range []string{"missing-proof", "unissued-proof", "foreign-proof-subject", "duplicate-publication", "budget", "closed"} {
		t.Run(scenario, func(t *testing.T) {
			root, digest := releasePublicationFixture(t)
			reference := "registry.example.test/original@" + digest
			publication, err := OpenPublication(t.Context(), root, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			verifier := mirrorFixtureSigner(t)
			request := MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Proofs: []*sign.VerifiedOCIEvidence{mirrorFixtureProof(t, verifier, reference)}, Limits: mirrorFixtureLimits()}
			switch scenario {
			case "missing-proof":
				request.Proofs = nil
			case "unissued-proof":
				request.Proofs = []*sign.VerifiedOCIEvidence{{}}
			case "foreign-proof-subject":
				request.Proofs = append(request.Proofs, mirrorFixtureProof(t, verifier, "registry.example.test/foreign@"+digestBytes([]byte("foreign"))))
			case "duplicate-publication":
				request.Publications = append(request.Publications, request.Publications[0])
			case "budget":
				request.Limits.MaxTotalBytes = 1
			case "closed":
				if err := publication.Close(); err != nil {
					t.Fatal(err)
				}
			}
			store := &mirrorMemoryStore{}
			locator, err := MirrorPublications(t.Context(), store, request)
			if err == nil || locator.Key != "" || len(store.writes) != 0 {
				t.Fatalf("invalid preflight published objects: %v %+v", err, locator)
			}
			assertRegistryCode(t, err, map[string]e.Code{"missing-proof": e.CodeSignatureInvalid, "unissued-proof": e.CodeGateUnmet, "foreign-proof-subject": e.CodeVerifyFailed, "duplicate-publication": e.CodeVerifyFailed, "budget": e.CodeEngineCapability, "closed": e.CodeGateUnmet}[scenario])
		})
	}
}

func TestRestoreMirrorRejectsMalformedOrIncompleteInventory(t *testing.T) {
	for _, scenario := range []string{"traversal", "wrong-index", "extra-object", "foreign-proof", "missing-object", "changed-object"} {
		t.Run(scenario, func(t *testing.T) {
			root, digest := releasePublicationFixture(t)
			reference := "registry.example.test/original@" + digest
			publication, err := OpenPublication(t.Context(), root, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			store := &mirrorMemoryStore{}
			locator, err := MirrorPublications(t.Context(), store, MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Proofs: []*sign.VerifiedOCIEvidence{mirrorFixtureProof(t, mirrorFixtureSigner(t), reference)}, Limits: mirrorFixtureLimits()})
			if err != nil {
				t.Fatal(err)
			}
			var inventory mirrorInventory
			if err := canonical.UnmarshalExact(store.data[locator.Key], &inventory); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "traversal":
				inventory.Publications[0].Members[0].Path = "../../escape"
			case "wrong-index":
				for i := range inventory.Publications[0].Members {
					if inventory.Publications[0].Members[i].Path == "index.json" {
						inventory.Publications[0].Members[i].SHA256 = inventory.Publications[0].Members[0].SHA256
					}
				}
			case "extra-object":
				inventory.Objects = append(inventory.Objects, mirrorObject(StoredObject{Object: Object{Key: "../../escape", SHA256: digestBytes([]byte("extra")), Size: 5, MediaType: MediaOctetStream}}))
			case "foreign-proof":
				inventory.Proofs[0].Subject = mirrorSubject{Ref: "registry.example.test/foreign@" + digestBytes([]byte("foreign")), Digest: digestBytes([]byte("foreign"))}
			case "missing-object":
				delete(store.data, inventory.Objects[0].Key)
			case "changed-object":
				store.data[inventory.Objects[0].Key] = append(bytes.Clone(store.data[inventory.Objects[0].Key]), '!')
			}
			if scenario != "missing-object" && scenario != "changed-object" {
				raw, err := canonical.Marshal(inventory)
				if err != nil {
					t.Fatal(err)
				}
				object := Object{Key: "publications/" + strings.TrimPrefix(digest, "sha256:") + "/" + strings.TrimPrefix(digestBytes(raw), "sha256:") + ".json", SHA256: digestBytes(raw), Size: int64(len(raw)), MediaType: "application/json"}
				locator, err = store.PutIfAbsent(t.Context(), object, bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(t.TempDir(), "refused")
			result, err := RestoreMirror(t.Context(), store, locator, out, mirrorFixtureLimits())
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if result != nil {
				t.Fatal("malformed mirror returned a successful restored description")
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("failed restore exposed output: %v", err)
			}
		})
	}
}

func TestMirrorReadbackFailureDoesNotPublishInventory(t *testing.T) {
	for _, corruption := range []string{"write", "same-size"} {
		t.Run(corruption, func(t *testing.T) {
			root, digest := releasePublicationFixture(t)
			reference := "registry.example.test/original@" + digest
			publication, err := OpenPublication(t.Context(), root, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			store := &mirrorMemoryStore{corrupt: corruption}
			locator, err := MirrorPublications(t.Context(), store, MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Proofs: []*sign.VerifiedOCIEvidence{mirrorFixtureProof(t, mirrorFixtureSigner(t), reference)}, Limits: mirrorFixtureLimits()})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if locator.Key != "" {
				t.Fatal("failed readback returned completion locator")
			}
			for _, write := range store.writes {
				if strings.HasPrefix(write.Key, "publications/") {
					t.Fatal("failed object readback published inventory")
				}
			}
		})
	}
}

func TestMirrorPreflightsAggregateUniqueBytesBeforeWrites(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	reference := "registry.example.test/original@" + digest
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	proof := mirrorFixtureProof(t, mirrorFixtureSigner(t), reference)
	unique := map[string]int64{}
	if err := publication.VisitObjects(t.Context(), func(_ context.Context, object PublicationObject, _ io.ReadSeeker) error {
		if object.Key != "oci-layout" {
			unique[object.SHA256] = object.Size
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, signature, provenance, err := proof.Bundles()
	if err != nil {
		t.Fatal(err)
	}
	unique[digestBytes(signature)] = int64(len(signature))
	unique[digestBytes(provenance)] = int64(len(provenance))
	var total, largest int64
	for _, size := range unique {
		total += size
		largest = max(largest, size)
	}
	if total <= largest+1 {
		t.Fatal("aggregate budget fixture has no meaningful unique-object excess")
	}
	for _, scenario := range []string{"aggregate bytes", "inventory count", "control bytes"} {
		t.Run(scenario, func(t *testing.T) {
			limits := mirrorFixtureLimits()
			failureStage := "transport inventory"
			switch scenario {
			case "aggregate bytes":
				limits.MaxTotalBytes = total - 1
				failureStage = "object/count/aggregate"
			case "inventory count":
				limits.MaxObjects = len(unique) // Every data object fits; the final inventory does not.
			case "control bytes":
				limits.MaxObjectBytes = largest // Every data object fits; inventory headroom does not.
			}
			store := &mirrorMemoryStore{}
			locator, err := MirrorPublications(t.Context(), store, MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Proofs: []*sign.VerifiedOCIEvidence{proof}, Limits: limits})
			assertRegistryCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), failureStage) {
				t.Fatalf("refusal occurred at another budget backstop: %v", err)
			}
			if locator.Key != "" || len(store.writes) != 0 {
				t.Fatal("complete inventory overflow wrote provider objects")
			}
		})
	}
}
