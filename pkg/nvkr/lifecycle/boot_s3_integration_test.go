// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// The SDK performs real HTTPS conditional writes and pinned reads against this
// synthetic S3 endpoint. Proofs use already-issued synthetic-runner evidence;
// independent trust acquisition/cryptography is covered by sign's tests, not
// claimed by this transport/membership round trip.
type bootReplayVerifier struct {
	*objectIntegrationVerifier
	records        map[string]sign.VerificationRecord
	proofs         map[string]*sign.VerifiedOCIEvidence
	seen           int
	allowDiscovery bool
}

func (v *bootReplayVerifier) VerifyAndRetain(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error) {
	if !v.allowDiscovery {
		return sign.VerificationRecord{}, nil, e.New(e.CodeVerifyFailed, "fixture forbids registry proof discovery")
	}
	proof := v.proofs[subject.Ref]
	if proof == nil {
		return sign.VerificationRecord{}, nil, e.New(e.CodeProvenanceMissing, "missing original fixture proof")
	}
	_, signature, provenance, err := proof.Bundles()
	if err != nil {
		return sign.VerificationRecord{}, nil, err
	}
	return v.VerifyBundles(ctx, subject, policy, signature, provenance)
}
func (v *bootReplayVerifier) AdmitBoundedVerification() error {
	return v.signer.AdmitBoundedVerification()
}
func (v *bootReplayVerifier) VerifyBundles(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy, signature, provenance []byte) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error) {
	if err := ctx.Err(); err != nil {
		return sign.VerificationRecord{}, nil, err
	}
	proof, record := v.proofs[subject.Ref], v.records[subject.Ref]
	if proof == nil {
		return record, nil, e.New(e.CodeProvenanceMissing, "missing original fixture proof")
	}
	original, sig, prov, err := proof.Bundles()
	if err != nil || subject != original || !bytes.Equal(sig, signature) || !bytes.Equal(prov, provenance) ||
		policy.IdentityRegexp != record.Identity || policy.Issuer != record.Issuer || !policy.RequireAttestation {
		return sign.VerificationRecord{}, nil, e.New(e.CodeSignatureInvalid, "restored bytes differ from original fixture proof")
	}
	v.seen++
	return record, proof, nil
}

func TestBootS3OnlySelectedProofRoundTrip(t *testing.T) {
	data := map[string][]byte{}
	var mu sync.Mutex
	var writes []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := strings.TrimPrefix(r.URL.Path, "/synthetic-boot")
		key = strings.TrimPrefix(key, "/boot/")
		if r.Method == http.MethodPut {
			if r.Header.Get("If-None-Match") != "*" {
				http.Error(w, "conditional write required", 400)
				return
			}
			if _, exists := data[key]; exists {
				http.Error(w, "exists", http.StatusPreconditionFailed)
				return
			}
			reader := io.Reader(r.Body)
			if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
				reader = httputil.NewChunkedReader(r.Body)
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				http.Error(w, "read failed", 500)
				return
			}
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				http.Error(w, "trailer read failed", 500)
				return
			}
			data[key] = raw
			writes = append(writes, key)
		} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		raw, exists := data[key]
		if !exists {
			http.NotFound(w, r)
			return
		}
		if pin := r.URL.Query().Get("versionId"); pin != "" && pin != "synthetic-version" {
			http.Error(w, "version denied", http.StatusForbidden)
			return
		}
		w.Header().Set("ETag", `"synthetic-etag"`)
		w.Header().Set("x-amz-version-id", "synthetic-version")
		if r.Method != http.MethodPut {
			w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(raw)
		}
	}))
	defer server.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = originalTransport }()
	f := newBootMembershipFixture(t, server.URL)
	f.setAggregateSigningAttempt(t, "4", false)
	s3 := *f.expected.Destination.S3
	request := f.expectation.final["boot"]
	options := delivery.S3ObjectStoreOptions{Region: s3.Region, Endpoint: s3.Endpoint, Bucket: s3.Bucket, Prefix: s3.Prefix,
		ExpectedBucketOwner: s3.ExpectedBucketOwner, Addressing: s3.Addressing, OperationTimeout: 1200 * time.Second,
		Credentials: &aws.Credentials{AccessKeyID: "SYNTHETICACCESS", SecretAccessKey: "synthetic-secret", SessionToken: "synthetic-token", CanExpire: true, Expires: time.Now().Add(time.Hour)}}
	membership, err := authenticateBootMembership(t.Context(), f.expected, f.expectation, f.policy, f.verifier, f.read(t))
	if err != nil {
		t.Fatal(err)
	}
	var assets []*delivery.BootAsset
	for _, use := range membership.uses {
		owner, err := delivery.OpenPublication(t.Context(), f.sources[use.Binding.Publication], referenceDigest(use.Binding.Publication))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = owner.Close() })
		var asset *delivery.BootAsset
		if use.Binding.Output == "disk" {
			asset, err = owner.OpenBootDisk(t.Context(), use.Binding.Publication, f.expected.Selection.Disks[0])
		} else {
			asset, err = owner.OpenBootImager(t.Context(), use.Binding.Publication, f.expected.Selection.Imagers[0])
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = asset.Close() })
		assets = append(assets, asset)
	}
	phase := func(work func(context.Context, *delivery.BootPhase) error) error {
		return delivery.WithBootPhase(t.Context(), request, options, delivery.BootReaderCredential{}, work)
	}
	var records []delivery.BootStoredAsset
	if err := phase(func(ctx context.Context, phase *delivery.BootPhase) error {
		available, err := phase.Copy(ctx, assets)
		if err != nil {
			return err
		}
		records, err = available.Descriptions()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var locator delivery.StoredObject
	if err := phase(func(ctx context.Context, phase *delivery.BootPhase) error {
		available, err := phase.Recheck(ctx, bootMembershipBindings(membership), records)
		if err != nil {
			return err
		}
		locator, err = phase.PublishProofs(ctx, delivery.BootPublicationRequest{Expected: f.expected, Availability: available, Uses: membership.uses, Publications: membership.publications, Proofs: membership.proofs})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lastWrite := writes[len(writes)-1]
	mu.Unlock()
	if lastWrite != locator.Key {
		t.Fatal("inventory was not last")
	}
	replay := &bootReplayVerifier{objectIntegrationVerifier: f.verifier, records: map[string]sign.VerificationRecord{}, proofs: map[string]*sign.VerifiedOCIEvidence{}}
	for reference := range f.verifier.bundles {
		record, proof, err := f.verifier.VerifyAndRetain(t.Context(), sign.Subject{Ref: reference, Digest: referenceDigest(reference)}, sign.VerifyPolicy{IdentityRegexp: f.policy.IdentityRegexp, Issuer: f.policy.Issuer, RequireAttestation: true})
		if err != nil {
			t.Fatal(err)
		}
		replay.records[reference], replay.proofs[reference] = record, proof
	}
	if err := phase(func(ctx context.Context, phase *delivery.BootPhase) error {
		restored, err := phase.RestoreProofs(ctx, locator, f.expected, t.TempDir())
		if err != nil {
			return err
		}
		defer restored.Close()
		proofs, err := restored.Proofs()
		if err != nil {
			return err
		}
		supplied := &objectProofVerifier{verifier: replay, proofs: map[string]delivery.RestoredProof{}}
		for _, proof := range proofs {
			supplied.proofs[proof.Subject.Ref] = proof
		}
		got, err := authenticateBootMembership(ctx, f.expected, f.expectation, f.policy, supplied, func(_ context.Context, reference string, _ []string) (*delivery.SelectedMetadata, error) {
			return restored.Metadata(reference)
		})
		if err != nil {
			return err
		}
		uses, err := restored.Uses()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(uses, got.uses) || len(proofs) != len(got.proofs) {
			t.Fatal("restored original membership changed")
		}
		stored, err := restored.StoredAssets()
		if err != nil {
			return err
		}
		available, err := phase.Recheck(ctx, bootMembershipBindings(got), stored)
		if err != nil {
			return err
		}
		description, err := bootReceiptDescription(f.expected, f.policy, locator, got, available)
		if err == nil && (len(description.Assets) != 2 || description.ContentSubject != membership.contentSubject) {
			t.Fatal("selected receipt changed logical membership")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if replay.seen != 5 {
		t.Fatal("S3 consumption omitted original proofs")
	}
	mu.Lock()
	clear(data)
	writes = nil
	mu.Unlock()
	// Exercise the exported lifecycle orchestration with private test adapters.
	// The trust seam preserves the real phase context/budget but deliberately
	// substitutes already-issued synthetic evidence, not a cryptographic claim.
	trustCalls, cleanupFailure := 0, false
	consumer := &Consumer{verifier: replay, inspection: InspectionOptions{WorkspaceRoot: t.TempDir()},
		materialize: func(ctx context.Context, reference string) (*delivery.Publication, error) {
			return delivery.OpenPublication(ctx, f.sources[reference], referenceDigest(reference))
		}, bootMetadata: f.read(t),
		bootTrust: func(ctx context.Context, work func(context.Context) error) error {
			trustCalls++
			if _, err := readbudget.Require(ctx); err != nil {
				return err
			}
			if _, bounded := ctx.Deadline(); !bounded {
				t.Fatal("exported API lost its phase deadline")
			}
			err := work(ctx)
			if cleanupFailure {
				return errors.Join(err, e.New(e.CodeImmutableViolation, "synthetic trust cleanup failure"))
			}
			return err
		},
	}
	references := map[string]string{}
	for _, use := range membership.uses {
		references[use.Binding.Output] = use.Binding.Publication
	}
	replay.allowDiscovery = true
	var borrowed *PreparedBootAssets
	prepared, err := consumer.WithPreparedBootAssets(t.Context(), f.expectation, "boot", references, f.expected.Invocation, f.policy, options, delivery.BootReaderCredential{},
		func(_ context.Context, receipt *PreparedBootAssets) error {
			borrowed = receipt
			_, err := receipt.Description()
			return err
		})
	if err != nil {
		t.Fatal(err)
	}
	_, err = borrowed.Description()
	assertCode(t, err, e.CodeGateUnmet)
	verification := BootVerification{Reference: f.expected.RootReference, Invocation: f.expected.Invocation, Destination: "boot", Policy: f.policy, Expectation: f.expectation}
	for _, mutate := range []func(*PreparedBootDescription){
		func(p *PreparedBootDescription) { p.PolicyDigest = pipeline.FileDigest([]byte("foreign policy")) },
		func(p *PreparedBootDescription) { p.Selection.Disks[0].Output = "foreign" },
		func(p *PreparedBootDescription) { p.Destination.Name = "foreign" },
		func(p *PreparedBootDescription) { p.Assets[0].URL += "?mutable=true" },
		func(p *PreparedBootDescription) { p.Assets[0], p.Assets[1] = p.Assets[1], p.Assets[0] },
		func(p *PreparedBootDescription) { p.Invocation = objectOriginInvocation("4") },
	} {
		changed := prepared
		changed.Selection = prepared.Selection.Clone()
		changed.Assets = append([]PreparedBootAsset(nil), prepared.Assets...)
		mutate(&changed)
		before := trustCalls
		got, err := consumer.PublishBootSelection(t.Context(), changed, verification, options, delivery.BootReaderCredential{})
		assertCode(t, err, e.CodeVerifyFailed)
		if got != nil || trustCalls != before {
			t.Fatal("changed preparation reached effectful trust admission")
		}
	}
	mu.Lock()
	_, inventoryExists := data[locator.Key]
	mu.Unlock()
	if inventoryExists {
		t.Fatal("exported publish must create a fresh inventory")
	}
	completion, err := consumer.PublishBootSelection(t.Context(), prepared, verification, options, delivery.BootReaderCredential{})
	if err != nil {
		t.Fatal(err)
	}
	description, err := completion.Description()
	if err != nil || description.Inventory != locator {
		t.Fatalf("exported publication changed canonical inventory: %v", err)
	}
	mu.Lock()
	lastWrite = writes[len(writes)-1]
	mu.Unlock()
	if lastWrite != locator.Key {
		t.Fatal("exported publication did not commit inventory last")
	}
	cleanupFailure = true
	completion, err = consumer.PublishBootSelection(t.Context(), prepared, verification, options, delivery.BootReaderCredential{})
	var failed *delivery.BootInventoryError
	if completion != nil || !errors.As(err, &failed) || failed.Attempt != locator {
		t.Fatalf("cleanup failure lost exact recovery input or issued completion: %v", err)
	}
	cleanupFailure = false
	completion, err = consumer.RetryBootSelection(t.Context(), prepared, verification, options, delivery.BootReaderCredential{}, failed.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	description, err = completion.Description()
	if err != nil || description.Inventory != locator {
		t.Fatal("retry changed exact inventory")
	}
	replay.allowDiscovery = false
	consumer.bootMetadata = func(context.Context, string, []string) (*delivery.SelectedMetadata, error) {
		t.Fatal("S3-only consumer reached original registry controls")
		return nil, nil
	}
	completion, err = consumer.VerifyBootSelection(t.Context(), locator, verification, options, delivery.BootReaderCredential{})
	if err != nil {
		t.Fatal(err)
	}
	description, err = completion.Description()
	if err != nil || len(description.Assets) != 2 || description.ContentSubject != membership.contentSubject {
		t.Fatal("exported consumer widened or changed selected result")
	}
	proofCalls := replay.seen
	// Remove a required original proof while leaving every payload and inventory
	// intact. Restoration must fail before proof discovery or any receipt.
	_, signature, _, err := membership.proofs[0].Bundles()
	if err != nil {
		t.Fatal(err)
	}
	key := "blobs/sha256/" + strings.TrimPrefix(pipeline.FileDigest(signature), "sha256:")
	mu.Lock()
	delete(data, key)
	mu.Unlock()
	completion, err = consumer.VerifyBootSelection(t.Context(), locator, verification, options, delivery.BootReaderCredential{})
	assertCode(t, err, e.CodeVerifyFailed)
	if completion != nil || replay.seen != proofCalls {
		t.Fatal("missing proof attempted remote discovery")
	}
}
