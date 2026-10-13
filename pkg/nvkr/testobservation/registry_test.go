// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	containerregistry "github.com/google/go-containerregistry/pkg/registry"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// This private fixture uses real local-key cryptography and OCI I/O. The public
// production API has no equivalent seam and requires keyless certificate trust.
type localKeyAttester struct {
	mu         sync.Mutex
	private    ed25519.PrivateKey
	public     ed25519.PublicKey
	signatures map[string][]byte
	attached   int
	fail       bool
}

func newLocalKeyAttester(t *testing.T) *localKeyAttester {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &localKeyAttester{private: private, public: public, signatures: map[string][]byte{}}
}
func (a *localKeyAttester) AttestTestObservation(ctx context.Context, subject sign.Subject, predicate []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attached++
	if a.fail {
		return e.New(e.CodeSignatureInvalid, "fixture attachment failed")
	}
	message, err := canonical.Marshal(struct {
		Subject   sign.Subject `json:"subject"`
		Predicate []byte       `json:"predicate"`
	}{subject, predicate})
	if err != nil {
		return err
	}
	a.signatures[subject.Ref] = ed25519.Sign(a.private, message)
	return nil
}
func (a *localKeyAttester) VerifyTestObservation(ctx context.Context, subject sign.Subject, predicate []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	message, err := canonical.Marshal(struct {
		Subject   sign.Subject `json:"subject"`
		Predicate []byte       `json:"predicate"`
	}{subject, predicate})
	if err != nil {
		return err
	}
	if !ed25519.Verify(a.public, message, a.signatures[subject.Ref]) {
		return e.New(e.CodeSignatureInvalid, "fixture signature identity or subject differs")
	}
	return nil
}

func TestObservationRegistrySignedRoundTrip(t *testing.T) {
	for _, state := range []string{"passing", "failed", "not-run"} {
		t.Run(state, func(t *testing.T) {
			o := observationFixture()
			root := ""
			switch state {
			case "failed":
				o.Execution = Outcome{Status: "failed", ExitCode: 1}
				root = emitFixture(t, o, `<testsuite name="suite" tests="1" failures="1"><testcase classname="class" name="case"><failure/></testcase></testsuite>`)
			case "not-run":
				o.Execution = Outcome{Status: "not-run"}
				root = t.TempDir() + "/bundle"
				if err := Emit(t.Context(), "", o, nil, root); err != nil {
					t.Fatal(err)
				}
			default:
				root = emitFixture(t, o, passingJUnit)
			}
			server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
			defer server.Close()
			repository := strings.TrimPrefix(server.URL, "http://") + "/observations"
			transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
			attester := newLocalKeyAttester(t)
			published, err := publishWith(t.Context(), root, repository, transport, attester)
			if err != nil {
				t.Fatal(err)
			}
			if !published.Successful || attester.attached != 1 {
				t.Fatalf("publication %+v", published)
			}
			bundle, err := Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer bundle.Close()
			expected := Expected{ObservationDigest: bundle.Digest(), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite}
			// Independent reader key, not the producer's private-key capability.
			consumer := &localKeyAttester{public: bytes.Clone(attester.public), signatures: attester.signatures}
			verified, err := verifyOCIWith(t.Context(), published.Locator.Reference, expected, transport, consumer)
			if err != nil {
				t.Fatal(err)
			}
			defer verified.Close()
			copiedReceipt := *verified
			result, err := verified.Description(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !result.SignerAuthenticated || !result.SelfConsistent || result.Passing != (state == "passing") {
				t.Fatalf("wrong outcome %+v", result)
			}
			wrongKey := newLocalKeyAttester(t)
			wrongKey.signatures = attester.signatures
			bad, err := verifyOCIWith(t.Context(), published.Locator.Reference, expected, transport, wrongKey)
			if bad != nil {
				t.Fatal("wrong signer issued receipt")
			}
			requireCode(t, err, e.CodeSignatureInvalid)
			wrong := expected
			wrong.ObservationDigest = rawPin("other")
			bad, err = verifyOCIWith(t.Context(), published.Locator.Reference, wrong, transport, consumer)
			if bad != nil {
				t.Fatal("wrong subject issued receipt")
			}
			requireCode(t, err, e.CodeVerifyFailed)
			if err := verified.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = verified.Description(t.Context())
			requireCode(t, err, e.CodeVerifyFailed)
			_, err = copiedReceipt.Description(t.Context())
			requireCode(t, err, e.CodeVerifyFailed)
			_, err = copiedReceipt.Observation(t.Context())
			requireCode(t, err, e.CodeVerifyFailed)
			_, err = copiedReceipt.Reference(t.Context())
			requireCode(t, err, e.CodeVerifyFailed)
			for _, receipt := range []*Verified{verified, &copiedReceipt} {
				_, err = receipt.Read(t.Context(), "results/junit.xml")
				requireCode(t, err, e.CodeVerifyFailed)
				_, _, err = receipt.Signer(t.Context())
				requireCode(t, err, e.CodeVerifyFailed)
			}

		})
	}
}

func TestObservationRegistryInterruptedAttachmentIsExplicit(t *testing.T) {
	o := observationFixture()
	root := emitFixture(t, o, passingJUnit)
	server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	a := newLocalKeyAttester(t)
	a.fail = true
	result, err := publishWith(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/observations", delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}, a)
	if result == nil || result.Successful || result.Locator.Reference == "" {
		t.Fatalf("partial effects lost: %+v %v", result, err)
	}
	requireCode(t, err, e.CodeSignatureInvalid)
}

func TestObservationRegistryTransportAdmission(t *testing.T) {
	for _, ref := range []string{"registry.example/repo:latest", "registry.example/repo", "registry.example/repo@sha256:bad", "https://registry.example/repo@" + rawPin("x")} {
		if _, err := pinnedReference(ref); err == nil {
			t.Fatalf("unbound reference admitted %q", ref)
		}
	}
	if _, err := observationRepository("registry.example/repo", delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}); err == nil {
		t.Fatal("plain HTTP outside loopback admitted")
	}
	repo, err := observationRepository("registry.example/repo", delivery.RegistryOptions{Anonymous: true})
	if err != nil {
		t.Fatal(err)
	}
	client := repo.Client.(*auth.Client).Client
	for _, target := range []string{"http://registry.example/file", "https://other.example/file"} {
		req, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if err := client.CheckRedirect(req, nil); err == nil {
			t.Fatal("redirect changed admitted origin", target)
		}
	}
	for _, desc := range []oci.Descriptor{
		{MediaType: FileMediaType, Digest: descriptor(FileMediaType, []byte("x")).Digest, Size: MaxFileBytes + 1},
		{MediaType: FileMediaType, Digest: descriptor(FileMediaType, []byte("x")).Digest, Size: 1, URLs: []string{"https://elsewhere.example/file"}},
	} {
		requireCode(t, validateDescriptor(desc, FileMediaType, "x", MaxFileBytes), e.CodeVerifyFailed)
	}
	var forged Verified
	_, err = forged.Description(t.Context())
	requireCode(t, err, e.CodeVerifyFailed)
	o := observationFixture()
	o.Execution.Status = "not-run"
	digest, err := o.Digest()
	if err != nil {
		t.Fatal(err)
	}
	expected := Expected{ObservationDigest: digest, Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite}
	_, err = VerifyOCI(t.Context(), "registry.example/repo@"+rawPin("x"), expected, RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}})
	requireCode(t, err, e.CodeTrustPolicyMissing)
}

func TestObservationRegistryCanceledReceipt(t *testing.T) {
	o := observationFixture()
	root := emitFixture(t, o, passingJUnit)
	server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	a := newLocalKeyAttester(t)
	published, err := publishWith(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/observations", transport, a)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	expected := Expected{ObservationDigest: bundle.Digest(), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite}
	ctx, cancel := context.WithCancel(t.Context())
	verified, err := verifyOCIWith(ctx, published.Locator.Reference, expected, transport, a)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	copied := *verified
	cancel()
	for _, receipt := range []*Verified{verified, &copied} {
		_, err = receipt.Read(t.Context(), "results/junit.xml")
		requireCode(t, err, e.CodeTimeout)
		_, _, err = receipt.Signer(t.Context())
		requireCode(t, err, e.CodeTimeout)
	}
	_, err = verified.Description(t.Context())
	requireCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause lost")
	}
}

func TestObservationRegistryRejectsChangedCoveredBytes(t *testing.T) {
	for _, state := range []string{"tampered", "missing"} {
		t.Run(state, func(t *testing.T) {
			o := observationFixture()
			root := emitFixture(t, o, passingJUnit)
			handler := containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0)))
			var reject sync.Mutex
			changed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reject.Lock()
				active := changed
				reject.Unlock()
				if active && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+rawPin(passingJUnit)) {
					if state == "missing" {
						http.Error(w, "missing covered member", http.StatusNotFound)
					} else {
						_, _ = w.Write([]byte(strings.Replace(passingJUnit, "case", "fake", 1)))
					}
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
			a := newLocalKeyAttester(t)
			result, err := publishWith(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/observations", transport, a)
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer bundle.Close()
			expected := Expected{ObservationDigest: bundle.Digest(), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite}
			reject.Lock()
			changed = true
			reject.Unlock()
			bad, err := verifyOCIWith(t.Context(), result.Locator.Reference, expected, transport, a)
			if bad != nil || err == nil {
				t.Fatal("changed or inaccessible covered member issued authority")
			}
		})
	}
}

func TestObservationRejectsNoncanonicalSignerBeforeWork(t *testing.T) {
	for _, v := range []string{" padded ", "identity\u202ehidden", "identity\x1b[2J"} {
		for _, field := range []string{"identity", "issuer"} {
			t.Run(field+v, func(t *testing.T) {
				options := RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}, Identity: "https://identity.example/issuer", Issuer: "https://issuer.example", CosignBinary: "/must-not-open", CosignDigest: rawPin("fixture")}
				if field == "identity" {
					options.Identity = v
				} else {
					options.Issuer = v
				}
				_, err := admittedAuthenticator(t.Context(), options)
				requireCode(t, err, e.CodeTrustPolicyMissing)
			})
		}
	}
}

func TestObservationRegistryRejectsCredentialExfiltrationRealm(t *testing.T) {
	var reached sync.Mutex
	calls := 0
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Lock()
		calls++
		reached.Unlock()
		t.Error("unadmitted token realm was contacted")
	}))
	defer realm.Close()
	registryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm.URL+`/token",service="fixture",scope="repository:observations:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer registryServer.Close()
	repo, err := observationRepository(strings.TrimPrefix(registryServer.URL, "http://")+"/observations", delivery.RegistryOptions{PlainHTTP: true, Username: "fixture-user", Password: "fixture-private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Resolve(t.Context(), rawPin("missing")); err == nil {
		t.Fatal("unadmitted realm accepted")
	}
	reached.Lock()
	defer reached.Unlock()
	if calls != 0 {
		t.Fatal("registry credential escaped admitted origin")
	}
}

func TestObservationCredentialsCannotFollowAnotherRegistryLocator(t *testing.T) {
	options := RegistryOptions{Transport: delivery.RegistryOptions{Username: "fixture", Password: "private"}, Credential: sign.Credential{Registry: "registry.example", Username: "fixture", Password: "private"}}
	if err := options.ValidateReference("untrusted.example/evidence@" + rawPin("x")); err == nil {
		t.Fatal("credential followed locator to a different host")
	}
	if err := options.ValidateReference("registry.example/evidence@" + rawPin("x")); err != nil {
		t.Fatal(err)
	}
	options.Credential = sign.Credential{}
	if err := options.ValidateReference("registry.example/evidence@" + rawPin("x")); err == nil {
		t.Fatal("transport credential lacks independent host scope")
	}
}

func TestObservationRequiresExplicitAuthenticationBeforeWork(t *testing.T) {
	for _, tt := range []struct {
		name      string
		transport delivery.RegistryOptions
		code      e.Code
	}{
		{"no-mode", delivery.RegistryOptions{}, e.CodeTrustPolicyMissing},
		{"username-only", delivery.RegistryOptions{Username: "fixture"}, e.CodeUsage},
		{"password-only", delivery.RegistryOptions{Password: "fixture"}, e.CodeUsage},
		{"anonymous-with-credentials", delivery.RegistryOptions{Anonymous: true, Username: "fixture", Password: "fixture"}, e.CodeUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := RegistryOptions{Transport: tt.transport, CosignBinary: "/must-not-open"}
			reference := "registry.invalid/observations@" + rawPin("fixture")
			requireCode(t, ValidateReference(reference, tt.transport), tt.code)
			requireCode(t, options.ValidateReference(reference), tt.code)
			_, err := observationRepository("registry.invalid/observations", tt.transport)
			requireCode(t, err, tt.code)
			_, err = admittedAuthenticator(t.Context(), options)
			requireCode(t, err, tt.code)
			_, err = Publish(t.Context(), "/must-not-open", "registry.invalid/observations", options)
			requireCode(t, err, tt.code)
			_, err = VerifyOCI(t.Context(), reference, Expected{}, options)
			requireCode(t, err, tt.code)
		})
	}
}

func TestObservationAuthenticationAdmissionPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := admittedAuthenticator(ctx, RegistryOptions{})
	requireCode(t, err, e.CodeTimeout)
	_, err = Publish(ctx, "/must-not-open", "registry.invalid/observations", RegistryOptions{})
	requireCode(t, err, e.CodeTimeout)
	_, err = VerifyOCI(ctx, "registry.invalid/observations@"+rawPin("fixture"), Expected{}, RegistryOptions{})
	requireCode(t, err, e.CodeTimeout)
}
