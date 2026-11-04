// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const objectIntegrationIdentity = "mirror@example.test"
const objectIntegrationIssuer = "https://issuer.example.test"

// These fixtures exercise real retained-bundle claims, subject/predicate parsing,
// exact-input gates and bytes. The injected runner does not perform cryptography,
// Fulcio/Rekor or external service validation. Synthetic SBOM envelopes exercise
// structural subject binding only, as in attestFixtureLayout.
type objectIntegrationVerifier struct {
	signer  *sign.CosignOCISigner
	bundles map[string][2][]byte
	seen    map[string]bool
}

func (verifier *objectIntegrationVerifier) VerifyAndRetain(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error) {
	bundles, ok := verifier.bundles[subject.Ref]
	if !ok {
		return sign.VerificationRecord{}, nil, e.New(e.CodeProvenanceMissing, "fixture has no original subject proof")
	}
	verifier.seen[subject.Ref] = true
	return verifier.signer.VerifyBundles(ctx, subject, policy, bundles[0], bundles[1])
}
func (verifier *objectIntegrationVerifier) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	record, _, err := verifier.VerifyAndRetain(ctx, subject, policy)
	return record, err
}
func (verifier *objectIntegrationVerifier) VerifyBundles(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy, signature, attestation []byte) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error) {
	return verifier.signer.VerifyBundles(ctx, subject, policy, signature, attestation)
}
func (*objectIntegrationVerifier) VerifySBOM(_ context.Context, digest string, bundle []byte) error {
	_, err := sbom.AttestedDocument(bundle, digest)
	return err
}
func (*objectIntegrationVerifier) SignAndAttest(context.Context, sign.Subject, []byte) (sign.SigningRecord, error) {
	return sign.SigningRecord{}, e.New(e.CodeEngineCapability, "fixture cannot sign")
}
func (*objectIntegrationVerifier) AttestSBOM(context.Context, string, []byte) ([]byte, error) {
	return nil, e.New(e.CodeEngineCapability, "fixture cannot attest")
}

type objectIntegrationStore struct {
	data  map[string][]byte
	reads int
}

func (store *objectIntegrationStore) PutIfAbsent(ctx context.Context, object delivery.Object, body io.ReadSeeker) (delivery.StoredObject, error) {
	if err := ctx.Err(); err != nil {
		return delivery.StoredObject{}, err
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return delivery.StoredObject{}, err
	}
	if previous, exists := store.data[object.Key]; exists && !bytes.Equal(previous, raw) {
		return delivery.StoredObject{}, e.New(e.CodeImmutableViolation, "fixture immutable object conflict")
	}
	store.data[object.Key] = bytes.Clone(raw)
	return delivery.StoredObject{Object: object, VersionID: "synthetic-version", ETag: `"synthetic-etag"`}, nil
}
func (store *objectIntegrationStore) OpenPinned(ctx context.Context, object delivery.StoredObject) (io.ReadCloser, error) {
	store.reads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, ok := store.data[object.Key]
	if !ok {
		return nil, e.New(e.CodeVerifyFailed, "fixture remote object missing")
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}
func (*objectIntegrationStore) Close() error { return nil }
func objectIntegrationDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func objectIntegrationPolicy() VerificationPolicy {
	return VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^" + objectIntegrationIdentity + "$", Issuer: objectIntegrationIssuer}
}

func objectIntegrationSource(t *testing.T) (exactInputFixture, *Consumer, *objectIntegrationVerifier, []byte, string) {
	t.Helper()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	env := map[string]string{"GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "example/project", "GITHUB_WORKFLOW_REF": "example/project/.github/workflows/build.yml@refs/heads/main", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1"}
	run, err := provenance.NewGitHubRunContext(func(key string) string { return env[key] }, now, now)
	if err != nil {
		t.Fatal(err)
	}
	plan := newExactInputPlan(t)
	fixture := exactInputFixtureForPlanAndInvocation(t, plan.Definition, plan.Environment, plan, run.InvocationID())
	metadata := fixture.request.Release
	predicate, err := provenance.MarshalProduction(provenance.ProductionPredicate(run, provenance.ProductionInputs{BuildType: provenance.BuildTypeHermeticBuild, PlanDigest: metadata.PlanDigest,
		RunPlanReference: "nvkr:synthetic-exact-plan", DefinitionDigest: metadata.DefinitionDigest, EnvironmentDigest: metadata.EnvironmentDigest, CatalogDigest: "sha256:" + strings.Repeat("1", 64), GeneratorVersion: "fixture", SigningMode: provenance.SigningModeCosign}))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: "synthetic-cosign", Identity: objectIntegrationPolicy().IdentityRegexp, Issuer: objectIntegrationIssuer,
		Runner: sign.RunnerFunc(func(_ context.Context, _ string, args []string, stdout, _ io.Writer) error {
			if len(args) > 0 && args[0] == "version" {
				_, err := io.WriteString(stdout, `{"gitVersion":"v3.1.3"}`)
				return err
			}
			if len(args) == 0 || args[0] != "verify-blob-attestation" {
				t.Fatalf("unexpected fixture subprocess %v", args)
			}
			return nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	verifier := &objectIntegrationVerifier{signer: signer, bundles: make(map[string][2][]byte), seen: make(map[string]bool)}
	publication, err := delivery.OpenPublication(t.Context(), fixture.layout, fixture.rootDigest)
	if err != nil {
		t.Fatal(err)
	}
	description, err := publication.Description(fixture.reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	references := []string{fixture.reference}
	for _, output := range description.Outputs {
		references = append(references, output.Reference)
	}
	cert := objectIntegrationCertificate(t)
	for _, reference := range references {
		digest := strings.SplitN(reference, "@", 2)[1]
		verifier.bundles[reference] = [2][]byte{objectIntegrationBundle(t, cert, digest, sign.CosignSignaturePredicateType, []byte(`{}`)), objectIntegrationBundle(t, cert, digest, sign.ProvenancePredicateType, predicate)}
	}
	consumer := &Consumer{verifier: verifier, materialize: func(ctx context.Context, reference string) (*delivery.Publication, error) {
		if reference != fixture.reference {
			t.Fatalf("unexpected source materialization %s", reference)
		}
		return delivery.OpenPublication(ctx, fixture.layout, fixture.rootDigest)
	}}
	return fixture, consumer, verifier, cert, run.InvocationID()
}

func objectIntegrationCertificate(t *testing.T) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := asn1.Marshal(objectIntegrationIssuer)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic object publication fixture"}, NotBefore: time.Unix(1, 0), NotAfter: time.Unix(4102444800, 0), KeyUsage: x509.KeyUsageDigitalSignature, EmailAddresses: []string{objectIntegrationIdentity}, ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}, Value: issuer}}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
func objectIntegrationBundle(t *testing.T, cert []byte, digest, predicateType string, predicate []byte) []byte {
	t.Helper()
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": predicateType, "subject": []any{map[string]any{"name": "original", "digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(predicate)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{"certificate": map[string]string{"rawBytes": base64.StdEncoding.EncodeToString(cert)}}, "dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "c3ludGhldGlj"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestObjectPublicationFullExactInputRoundTrip(t *testing.T) {
	for _, scenario := range []string{"exact", "changed policy", "partial proof", "wrong original subject", "unused proof", "corrupt payload", "wrong invocation", "changed plan"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, consumer, verifier, cert, invocation := objectIntegrationSource(t)
			expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
			if err != nil {
				t.Fatal(err)
			}
			store := &objectIntegrationStore{data: make(map[string][]byte)}
			limits := delivery.MirrorLimits{MaxObjectBytes: 64 << 20, MaxTotalBytes: 256 << 20, MaxObjects: 4096}
			locator, err := consumer.MirrorExactInputs(t.Context(), fixture.reference, objectIntegrationPolicy(), expectation, invocation, store, limits)
			if err != nil {
				t.Fatal(err)
			}
			if len(verifier.seen) != len(verifier.bundles) || len(verifier.seen) < 3 {
				t.Fatal("publisher omitted root or deliverable proof")
			}
			// Offline verification must never invoke original registry materialization.
			consumer.materialize = func(context.Context, string) (*delivery.Publication, error) {
				t.Fatal("offline verification consulted registry materializer")
				return nil, nil
			}
			options := ObjectPublicationVerification{Reference: fixture.reference, Invocation: invocation, Policy: objectIntegrationPolicy(), Expectation: expectation, Limits: limits}
			switch scenario {
			case "changed policy":
				options.Policy.IdentityRegexp = "^untrusted@example.test$"
			case "partial proof":
				delete(store.data, "blobs/sha256/"+strings.TrimPrefix(objectIntegrationDigest(verifier.bundles[fixture.reference][1]), "sha256:"))
			case "wrong original subject":
				wrong := objectIntegrationBundle(t, cert, "sha256:"+strings.Repeat("f", 64), sign.CosignSignaturePredicateType, []byte(`{}`))
				locator = replaceObjectIntegrationProof(t, store, locator, verifier.bundles[fixture.reference][0], wrong)
			case "unused proof":
				var inventory map[string]any
				if err := json.Unmarshal(store.data[locator.Key], &inventory); err != nil {
					t.Fatal(err)
				}
				proofs := inventory["proofs"].([]any)
				original := proofs[0].(map[string]any)
				digest := original["subject"].(map[string]any)["Digest"].(string)
				alias := map[string]any{"subject": map[string]any{"Ref": "registry.example.test/unused@" + digest, "Digest": digest}, "signature": original["signature"], "provenance": original["provenance"]}
				proofs = append(proofs, alias)
				sort.Slice(proofs, func(i, j int) bool {
					return proofs[i].(map[string]any)["subject"].(map[string]any)["Ref"].(string) < proofs[j].(map[string]any)["subject"].(map[string]any)["Ref"].(string)
				})
				inventory["proofs"] = proofs
				locator = replaceObjectIntegrationInventory(t, store, inventory)
			case "corrupt payload":
				for key, raw := range store.data {
					if key != locator.Key && len(raw) > 0 && !json.Valid(raw) {
						changed := bytes.Clone(raw)
						changed[0] ^= 1
						store.data[key] = changed
						break
					}
				}
			case "wrong invocation":
				options.Invocation = "https://github.com/example/project/actions/runs/999/attempts/1"
			case "changed plan":
				environment := fixture.environment
				environment.Metadata.Name = "different-exact-environment"
				changed, compileErr := Compile(t.Context(), "definition.yaml", fixture.definition, "environment.yaml", environment, "test")
				if compileErr != nil {
					t.Fatal(compileErr)
				}
				options.Expectation, err = PrepareExactInputs(fixture.definition, environment, changed)
				if err != nil {
					t.Fatal(err)
				}
			}
			verified, receipt, err := consumer.VerifyObjectPublication(t.Context(), store, locator, options)
			if scenario == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				if verified == nil || receipt == nil {
					t.Fatal("verified round trip lacks existing receipts")
				}
				if _, err := receipt.Description(); err != nil {
					t.Fatal(err)
				}
				if err := verified.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || verified != nil || receipt != nil {
				if verified != nil {
					_ = verified.Close()
				}
				t.Fatal("substituted transport/evidence/input produced a receipt")
			}
			switch scenario {
			case "changed policy":
				assertCode(t, err, e.CodeUntrustedSigner)
			case "wrong original subject":
				assertCode(t, err, e.CodeVerifyFailed)
			default:
				assertCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

// Rebind the unsigned transport inventory to substituted signature bytes. This
// defeats transport hashes deliberately; the unchanged original subject must
// still fail the real bundle owner's subject check during offline verification.
func replaceObjectIntegrationProof(t *testing.T, store *objectIntegrationStore, locator delivery.StoredObject, previous, replacement []byte) delivery.StoredObject {
	t.Helper()
	var inventory map[string]any
	if err := json.Unmarshal(store.data[locator.Key], &inventory); err != nil {
		t.Fatal(err)
	}
	oldDigest, newDigest := objectIntegrationDigest(previous), objectIntegrationDigest(replacement)
	for _, value := range inventory["proofs"].([]any) {
		proof := value.(map[string]any)
		if proof["signature"] == oldDigest {
			proof["signature"] = newDigest
		}
	}
	objects := inventory["objects"].([]any)
	for _, value := range objects {
		object := value.(map[string]any)
		if object["SHA256"] == oldDigest {
			object["SHA256"] = newDigest
			object["Key"] = "blobs/sha256/" + strings.TrimPrefix(newDigest, "sha256:")
			object["Size"] = len(replacement)
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].(map[string]any)["SHA256"].(string) < objects[j].(map[string]any)["SHA256"].(string)
	})
	store.data["blobs/sha256/"+strings.TrimPrefix(newDigest, "sha256:")] = replacement
	return replaceObjectIntegrationInventory(t, store, inventory)
}

func replaceObjectIntegrationInventory(t *testing.T, store *objectIntegrationStore, inventory map[string]any) delivery.StoredObject {
	t.Helper()
	raw, err := canonical.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	digest := objectIntegrationDigest(raw)
	locator := delivery.StoredObject{Object: delivery.Object{Key: "publications/" + strings.TrimPrefix(fixtureRootDigest(inventory), "sha256:") + "/" + strings.TrimPrefix(digest, "sha256:") + ".json", SHA256: digest, Size: int64(len(raw)), MediaType: "application/json"}, VersionID: "synthetic-version", ETag: `"synthetic-etag"`}
	store.data[locator.Key] = raw
	return locator
}
func fixtureRootDigest(inventory map[string]any) string {
	return strings.SplitN(inventory["rootReference"].(string), "@", 2)[1]
}

func TestObjectPublicationAdmitsExpectationsBeforeReads(t *testing.T) {
	fixture, consumer, _, _, invocation := objectIntegrationSource(t)
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		expectation *ExactInputExpectation
		lane, scope string
		code        e.Code
	}{
		{"missing expectation", nil, LanePipeline, ScopeArtifactPublication, e.CodeGateUnmet},
		{"unissued expectation", &ExactInputExpectation{}, LanePipeline, ScopeArtifactPublication, e.CodeGateUnmet},
		{"authenticity scope", expectation, LanePipeline, ScopeAuthenticity, e.CodeEngineCapability},
		{"release scope", expectation, LanePipeline, ScopeRelease, e.CodeEngineCapability},
		{"qualified scope", expectation, LaneQualified, ScopeRelease, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &objectIntegrationStore{data: make(map[string][]byte)}
			policy := objectIntegrationPolicy()
			policy.Lane = test.lane
			policy.Scope = test.scope
			result, receipt, err := consumer.VerifyObjectPublication(t.Context(), store, delivery.StoredObject{}, ObjectPublicationVerification{Reference: fixture.reference, Invocation: invocation, Policy: policy, Expectation: test.expectation, Limits: delivery.MirrorLimits{MaxObjectBytes: 64 << 20, MaxTotalBytes: 256 << 20, MaxObjects: 4096}})
			assertCode(t, err, test.code)
			if result != nil || receipt != nil || store.reads != 0 {
				t.Fatal("unsupported verification admitted remote work or receipt")
			}
		})
	}
}

type objectIntegrationTokenBody struct {
	reader   io.Reader
	cancel   context.CancelFunc
	closeErr error
}

func (body *objectIntegrationTokenBody) Read(p []byte) (int, error) {
	n, err := body.reader.Read(p)
	if body.cancel != nil && n > 0 {
		body.cancel()
	}
	return n, err
}
func (body *objectIntegrationTokenBody) Close() error { return body.closeErr }

func TestObjectPublicationTokenFinalCancellationAndClose(t *testing.T) {
	for _, scenario := range []string{"final chunk cancellation", "body close failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := &objectIntegrationTokenBody{reader: strings.NewReader(`{"value":"synthetic-workload-token"}`)}
			want := e.CodeUnavailable
			if scenario == "final chunk cancellation" {
				body.cancel = cancel
				want = e.CodeTimeout
			} else {
				body.closeErr = errors.New("synthetic-secret-close-text")
			}
			client := &http.Client{Transport: objectTokenTransport(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header), Request: request}, nil
			})}
			values := map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "https://token.actions.githubusercontent.com/request", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "synthetic-request-token"}
			token, err := objectGitHubToken(ctx, client, func(key string) string { return values[key] })
			assertCode(t, err, want)
			if len(token) != 0 || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("failed/canceled token observation escaped or disclosed source errors")
			}
		})
	}
}

func TestObjectPublicationAdmitsBudgetsBeforeSourceOrStore(t *testing.T) {
	fixture, consumer, _, _, invocation := objectIntegrationSource(t)
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	consumer.materialize = func(context.Context, string) (*delivery.Publication, error) {
		t.Fatal("invalid budget reached source materialization")
		return nil, nil
	}
	for _, mode := range []string{"publish", "consume"} {
		t.Run(mode, func(t *testing.T) {
			store := &objectIntegrationStore{data: make(map[string][]byte)}
			if mode == "publish" {
				_, err = consumer.MirrorExactInputs(t.Context(), fixture.reference, objectIntegrationPolicy(), expectation, invocation, store, delivery.MirrorLimits{})
			} else {
				_, _, err = consumer.VerifyObjectPublication(t.Context(), store, delivery.StoredObject{}, ObjectPublicationVerification{Reference: fixture.reference, Invocation: invocation, Policy: objectIntegrationPolicy(), Expectation: expectation})
			}
			assertCode(t, err, e.CodeUsage)
			if store.reads != 0 || len(store.data) != 0 {
				t.Fatal("invalid budget reached object I/O")
			}
		})
	}
}
