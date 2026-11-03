// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/authn"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type bundleRegistryResponse struct {
	body      []byte
	mediaType string
	status    int
	link      string
}

// The registry fixture serves exact external OCI bytes and records every read;
// cryptographic trust remains a separate injected diagnostic runner in tests.
type bundleRegistryFixture struct {
	server         *httptest.Server
	subject        Subject
	routes         map[string]bundleRegistryResponse
	mu             sync.Mutex
	reads          map[string]int
	authorizations []string
	requireBasic   bool
}

func newBundleRegistryFixture(t *testing.T) *bundleRegistryFixture {
	t.Helper()
	f := &bundleRegistryFixture{routes: make(map[string]bundleRegistryResponse), reads: make(map[string]int)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reads[r.Method+" "+r.URL.Path]++
		f.authorizations = append(f.authorizations, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if f.requireBasic {
			user, password, ok := r.BasicAuth()
			if !ok || user != "fixture-user" || password != "fixture-password" {
				w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		response, ok := f.routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", response.mediaType)
		if response.link != "" {
			w.Header().Set("Link", response.link)
		}
		if response.status != 0 {
			w.WriteHeader(response.status)
		}
		_, _ = w.Write(response.body)
	}))
	f.subject = Subject{Ref: strings.TrimPrefix(f.server.URL, "http://") + "/fixture/output@" + ociTestDigest, Digest: ociTestDigest}
	t.Cleanup(f.server.Close)
	return f
}

func bundleRegistryJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func bundleRegistryHash(t *testing.T, raw []byte) v1.Hash {
	t.Helper()
	hash, err := v1.NewHash(digestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func (f *bundleRegistryFixture) add(t *testing.T, bundle []byte, hint string, change func(*v1.Manifest)) v1.Descriptor {
	t.Helper()
	subject, err := v1.NewHash(f.subject.Digest)
	if err != nil {
		t.Fatal(err)
	}
	layer := v1.Descriptor{MediaType: sbom.BundleMediaType, Size: int64(len(bundle)), Digest: bundleRegistryHash(t, bundle)}
	manifest := v1.Manifest{
		SchemaVersion: 2, MediaType: types.OCIManifestSchema1, ArtifactType: sbom.BundleMediaType,
		Subject: &v1.Descriptor{MediaType: types.OCIManifestSchema1, Size: 123, Digest: subject},
		Config:  v1.Descriptor{MediaType: ociEmptyConfigMediaType, Size: 2, Digest: bundleRegistryHash(t, []byte("{}"))},
		Layers:  []v1.Descriptor{layer},
	}
	if change != nil {
		change(&manifest)
	}
	raw := bundleRegistryJSON(t, manifest)
	descriptor := v1.Descriptor{MediaType: types.OCIManifestSchema1, ArtifactType: sbom.BundleMediaType, Size: int64(len(raw)), Digest: bundleRegistryHash(t, raw), Annotations: map[string]string{ociPredicateAnnotation: hint}}
	f.routes["/v2/fixture/output/manifests/"+descriptor.Digest.String()] = bundleRegistryResponse{body: raw, mediaType: string(types.OCIManifestSchema1)}
	f.routes["/v2/fixture/output/blobs/"+layer.Digest.String()] = bundleRegistryResponse{body: bundle, mediaType: sbom.BundleMediaType}
	return descriptor
}

func (f *bundleRegistryFixture) index(t *testing.T, descriptors []v1.Descriptor) {
	t.Helper()
	f.routes["/v2/fixture/output/referrers/"+f.subject.Digest] = bundleRegistryResponse{body: bundleRegistryJSON(t, v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex, Manifests: descriptors}), mediaType: string(types.OCIImageIndex)}
}

func (f *bundleRegistryFixture) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads["GET "+path]
}

func (f *bundleRegistryFixture) assertNoLegacyReads(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for path := range f.reads {
		if strings.HasSuffix(path, ".sig") || strings.HasSuffix(path, ".att") {
			t.Fatalf("legacy attachment read: %s", path)
		}
	}
}

func newRegistryDiagnosticSigner(t *testing.T, verify func([]byte, string) error) *CosignOCISigner {
	t.Helper()
	signer, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer,
		Runner: EnvRunnerFunc(func(_ context.Context, _ string, args, _ []string, stdout, _ io.Writer) error {
			switch args[0] {
			case "verify-blob-attestation":
				raw, err := os.ReadFile(args[2])
				if err != nil {
					return err
				}
				return verify(raw, args[4])
			case "version":
				_, err := io.WriteString(stdout, cosignVersionJSON)
				return err
			default:
				return fmt.Errorf("unexpected cosign registry subprocess: %s", args[0])
			}
		})})
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestRegistryBundleVerifyFetchesOnlyRequiredHistory(t *testing.T) {
	for _, history := range []int{15, 42} {
		t.Run(fmt.Sprintf("%d mixed referrers", history), func(t *testing.T) {
			f := newBundleRegistryFixture(t)
			signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
			provenance := []byte(qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, []byte(ociTestPredicate)))
			descriptors := []v1.Descriptor{f.add(t, signature, CosignSignaturePredicateType, nil), f.add(t, provenance, ProvenancePredicateType, nil)}
			for i := 2; i < history; i++ {
				raw := []byte(qualificationBundle(t, ociTestDigest, sbom.CycloneDXPredicateType, ociObservedIdentity, ociTestIssuer, fmt.Appendf(nil, `{"history":%d}`, i)))
				descriptors = append(descriptors, f.add(t, raw, sbom.CycloneDXPredicateType, nil))
			}
			f.index(t, descriptors)
			var verified [][]byte
			signer := newRegistryDiagnosticSigner(t, func(raw []byte, _ string) error { verified = append(verified, bytes.Clone(raw)); return nil })
			record, err := signer.Verify(t.Context(), f.subject, VerifyPolicy{RequireAttestation: true, ExpectedPredicate: []byte(ociTestPredicate)})
			if err != nil {
				t.Fatal(err)
			}
			if !record.SignatureVerified || !record.AttestationVerified || len(verified) != 2 || !bytes.Equal(verified[0], signature) || !bytes.Equal(verified[1], provenance) {
				t.Fatalf("verification did not use exact required bundles: %+v, %d calls", record, len(verified))
			}
			if got := f.count("/v2/fixture/output/referrers/" + ociTestDigest); got != 1 {
				t.Fatalf("discovery reads = %d, want one per Verify", got)
			}
			for i, descriptor := range descriptors {
				want := 0
				if i < 2 {
					want = 1
				}
				if got := f.count("/v2/fixture/output/manifests/" + descriptor.Digest.String()); got != want {
					t.Errorf("history %d manifest reads = %d, want %d", i, got, want)
				}
			}
			if f.count("/v2/fixture/output/blobs/"+digestBytes(signature)) != 1 || f.count("/v2/fixture/output/blobs/"+digestBytes(provenance)) != 1 {
				t.Fatal("required bundle bytes were not fetched exactly once")
			}
			f.assertNoLegacyReads(t)
		})
	}
}

func TestRegistryBundleVerifyAnnotationsAreOnlyHints(t *testing.T) {
	for _, hint := range []string{"", "https://unknown.example/predicate", sbom.CycloneDXPredicateType} {
		t.Run(hint, func(t *testing.T) {
			f := newBundleRegistryFixture(t)
			signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
			provenance := []byte(qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, []byte(ociTestPredicate)))
			misleading := []byte(testOCIBundle(t, ociTestDigest, sbom.CycloneDXPredicateType, ociObservedIdentity, ociTestIssuer))
			descriptors := []v1.Descriptor{f.add(t, misleading, CosignSignaturePredicateType, nil), f.add(t, signature, hint, nil), f.add(t, provenance, hint, nil)}
			f.index(t, descriptors)
			calls := 0
			signer := newRegistryDiagnosticSigner(t, func(raw []byte, _ string) error {
				calls++
				if bytes.Equal(raw, misleading) {
					t.Error("annotation bypassed exact predicate admission")
				}
				return nil
			})
			if _, err := signer.Verify(t.Context(), f.subject, VerifyPolicy{RequireAttestation: true, ExpectedPredicate: []byte(ociTestPredicate)}); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("crypto calls = %d, want two", calls)
			}
			for _, descriptor := range descriptors {
				if got := f.count("/v2/fixture/output/manifests/" + descriptor.Digest.String()); got != 1 {
					t.Fatalf("shared candidate fetched %d times", got)
				}
			}
		})
	}
}

func TestRegistryBundleVerifyRejectsStaleProvenance(t *testing.T) {
	f := newBundleRegistryFixture(t)
	signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
	stale := []byte(qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, []byte(`{"buildDefinition":{"run":"old"}}`)))
	current := []byte(qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, []byte(ociTestPredicate)))
	f.index(t, []v1.Descriptor{f.add(t, signature, CosignSignaturePredicateType, nil), f.add(t, stale, ProvenancePredicateType, nil), f.add(t, current, "", nil)})
	var verified [][]byte
	signer := newRegistryDiagnosticSigner(t, func(raw []byte, _ string) error { verified = append(verified, bytes.Clone(raw)); return nil })
	if _, err := signer.Verify(t.Context(), f.subject, VerifyPolicy{RequireAttestation: true, ExpectedPredicate: []byte(ociTestPredicate)}); err != nil {
		t.Fatal(err)
	}
	if len(verified) != 2 || !bytes.Equal(verified[1], current) {
		t.Fatal("stale provenance reached or replaced the exact-predicate crypto check")
	}
	if f.count("/v2/fixture/output/blobs/"+digestBytes(stale)) != 1 {
		t.Fatal("fixture did not exercise a stale provenance candidate")
	}
}

func TestRegistryBundleRejectsMalformedClosure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		manifest   func(*v1.Manifest)
		descriptor func(*v1.Descriptor)
		response   string
	}{
		{name: "wrong manifest subject", manifest: func(m *v1.Manifest) { m.Subject.Digest.Hex = strings.Repeat("8", 64) }},
		{name: "missing manifest subject", manifest: func(m *v1.Manifest) { m.Subject = nil }},
		{name: "wrong artifact profile", manifest: func(m *v1.Manifest) { m.ArtifactType = "application/example" }},
		{name: "multiple layers", manifest: func(m *v1.Manifest) { m.Layers = append(m.Layers, m.Layers[0]) }},
		{name: "wrong empty config", manifest: func(m *v1.Manifest) { m.Config.Digest.Hex = strings.Repeat("8", 64) }},
		{name: "config URL", manifest: func(m *v1.Manifest) { m.Config.URLs = []string{"https://untrusted.invalid/config"} }},
		{name: "wrong layer type", manifest: func(m *v1.Manifest) { m.Layers[0].MediaType = types.OCILayer }},
		{name: "layer size", manifest: func(m *v1.Manifest) { m.Layers[0].Size++ }},
		{name: "oversized layer", manifest: func(m *v1.Manifest) { m.Layers[0].Size = (8 << 20) + 1 }},
		{name: "layer URL", manifest: func(m *v1.Manifest) { m.Layers[0].URLs = []string{"https://untrusted.invalid/bundle"} }},
		{name: "subject URL", manifest: func(m *v1.Manifest) { m.Subject.URLs = []string{"https://untrusted.invalid/subject"} }},
		{name: "manifest descriptor size", descriptor: func(d *v1.Descriptor) { d.Size++ }},
		{name: "manifest descriptor URL", descriptor: func(d *v1.Descriptor) { d.URLs = []string{"https://untrusted.invalid/manifest"} }},
		{name: "manifest descriptor data", descriptor: func(d *v1.Descriptor) { d.Data = []byte("untrusted") }},
		{name: "zero manifest descriptor size", descriptor: func(d *v1.Descriptor) { d.Size = 0 }},
		{name: "unsupported digest algorithm", descriptor: func(d *v1.Descriptor) { d.Digest.Algorithm = "sha512" }},
		{name: "oversized manifest descriptor", descriptor: func(d *v1.Descriptor) { d.Size = (1 << 20) + 1 }},
		{name: "manifest digest", response: "manifest"},
		{name: "layer digest", response: "layer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBundleRegistryFixture(t)
			raw := []byte(`{"bundle":"exact"}`)
			descriptor := f.add(t, raw, "", tc.manifest)
			if tc.descriptor != nil {
				tc.descriptor(&descriptor)
			}
			if tc.response != "" {
				path := "/v2/fixture/output/manifests/" + descriptor.Digest.String()
				if tc.response == "layer" {
					path = "/v2/fixture/output/blobs/" + digestBytes(raw)
				}
				response := f.routes[path]
				response.body = bytes.Replace(response.body, []byte("exact"), []byte("other"), 1)
				if tc.response == "manifest" {
					response.body = bytes.Replace(response.body, []byte("schemaVersion"), []byte("schemaVersioN"), 1)
				}
				f.routes[path] = response
			}
			f.index(t, []v1.Descriptor{descriptor})
			source, err := newRegistryBundleSource(t.Context(), f.subject, Credential{}, nil)
			if err == nil {
				defer source.close()
				_, err = source.bundle(t.Context(), descriptor)
			}
			requireSignCode(t, err, e.CodeSignatureInvalid)
			f.assertNoLegacyReads(t)
		})
	}
}

func TestRegistryBundleDiscoveryLimitsAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		count                           int
		oversized, pagination, fallback bool
		valid                           bool
	}{
		{name: "maximum discovery", count: 256, valid: true},
		{name: "too many descriptors", count: 257},
		{name: "oversized index", oversized: true},
		{name: "paginated index", pagination: true},
		{name: "standard referrers tag", count: 1, fallback: true, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBundleRegistryFixture(t)
			var descriptors []v1.Descriptor
			for i := 0; i < tc.count; i++ {
				descriptors = append(descriptors, f.add(t, fmt.Appendf(nil, `{"candidate":%d}`, i), "", nil))
			}
			f.index(t, descriptors)
			path := "/v2/fixture/output/referrers/" + ociTestDigest
			response := f.routes[path]
			if tc.oversized {
				response.body = append(response.body, bytes.Repeat([]byte(" "), (4<<20)+1)...)
			}
			if tc.pagination {
				response.link = `</v2/fixture/output/referrers/next>; rel="next"`
			}
			f.routes[path] = response
			if tc.fallback {
				delete(f.routes, path)
				f.routes["/v2/fixture/output/manifests/sha256-"+strings.TrimPrefix(ociTestDigest, "sha256:")] = response
			}
			source, err := newRegistryBundleSource(t.Context(), f.subject, Credential{}, nil)
			if source != nil {
				defer source.close()
			}
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if got := len(source.candidates(CosignSignaturePredicateType)); got != tc.count {
					t.Fatalf("candidates = %d, want %d", got, tc.count)
				}
				if tc.fallback {
					if _, err := source.bundle(t.Context(), descriptors[0]); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				requireSignCode(t, err, e.CodeSignatureInvalid)
			}
			f.assertNoLegacyReads(t)
		})
	}
}

func TestRegistryBundleCredentialsAreExplicitAndScoped(t *testing.T) {
	for _, tc := range []struct {
		name           string
		credential     Credential
		basic, ambient bool
		want           e.Code
	}{
		{name: "anonymous ignores ambient", ambient: true},
		{name: "explicit scoped credential", basic: true},
		{name: "different registry", credential: Credential{Registry: "other.invalid", Username: "fixture-user", Password: "fixture-password"}, want: e.CodeUsage},
		{name: "partial credential", credential: Credential{Username: "fixture-user"}, want: e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBundleRegistryFixture(t)
			f.requireBasic = tc.basic
			descriptor := f.add(t, []byte(`{"bundle":"exact"}`), "", nil)
			f.index(t, []v1.Descriptor{descriptor})
			credential := tc.credential
			if tc.basic {
				credential = Credential{Registry: strings.TrimPrefix(f.server.URL, "http://"), Username: "fixture-user", Password: "fixture-password"}
			}
			if tc.ambient {
				dir := t.TempDir()
				config := fmt.Sprintf(`{"auths":{%q:{"auth":%q}},"credsStore":"must-not-run"}`, strings.TrimPrefix(f.server.URL, "http://"), base64.StdEncoding.EncodeToString([]byte("ambient-user:ambient-password")))
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("DOCKER_CONFIG", dir)
			}
			source, err := newRegistryBundleSource(t.Context(), f.subject, credential, nil)
			if source != nil {
				defer source.close()
			}
			if tc.want != "" {
				requireSignCode(t, err, tc.want)
				if len(f.reads) != 0 {
					t.Fatal("invalid credentials performed registry I/O")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.bundle(t.Context(), descriptor); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, authorization := range f.authorizations {
				if tc.ambient && authorization != "" {
					t.Fatal("ambient credentials reached registry")
				}
			}
		})
	}
}

func TestRegistryBundleKeychainResolution(t *testing.T) {
	for _, tc := range []struct {
		name              string
		private, keychain bool
		ambientPassword   string
		wantAuthorization bool
		want              e.Code
	}{
		{name: "private registry through default keychain", private: true, keychain: true, ambientPassword: "fixture-password", wantAuthorization: true},
		{name: "anonymous registry without keychain"},
		{name: "anonymous registry with empty keychain", keychain: true},
		{name: "private registry without keychain", private: true, want: e.CodeFetchFailed},
		{name: "keychain credential rejected", private: true, keychain: true, ambientPassword: "wrong-password", want: e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBundleRegistryFixture(t)
			f.requireBasic = tc.private
			descriptor := f.add(t, []byte(`{"bundle":"exact"}`), "", nil)
			f.index(t, []v1.Descriptor{descriptor})
			dir := t.TempDir()
			config := `{"auths":{}}`
			if tc.ambientPassword != "" {
				config = fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, strings.TrimPrefix(f.server.URL, "http://"), base64.StdEncoding.EncodeToString([]byte("fixture-user:"+tc.ambientPassword)))
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_CONFIG", dir)
			var keychain authn.Keychain
			if tc.keychain {
				keychain = authn.DefaultKeychain
			}
			source, err := newRegistryBundleSource(t.Context(), f.subject, Credential{}, keychain)
			if source != nil {
				defer source.close()
			}
			if tc.want != "" {
				requireSignCode(t, err, tc.want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.bundle(t.Context(), descriptor); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			authorized := false
			for _, authorization := range f.authorizations {
				authorized = authorized || authorization != ""
			}
			if authorized != tc.wantAuthorization {
				t.Fatalf("authorization sent=%v, want %v", authorized, tc.wantAuthorization)
			}
		})
	}
}

func TestOCIVerifierRejectsCredentialWithKeychain(t *testing.T) {
	_, err := NewCosignOCISigner(CosignOCIOptions{Identity: "^fixture$", Issuer: ociTestIssuer, Runner: &cosignStub{},
		Credential: Credential{Registry: "registry.invalid", Username: "fixture-user", Password: "fixture-password"}, Keychain: authn.DefaultKeychain})
	requireSignCode(t, err, e.CodeUsage)
}

func TestRegistryBundleCancellationStopsBodyRead(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			return
		}
		w.Header().Set("Content-Type", string(types.OCIImageIndex))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		source, err := newRegistryBundleSource(ctx, Subject{Ref: strings.TrimPrefix(server.URL, "http://") + "/fixture/output@" + ociTestDigest, Digest: ociTestDigest}, Credential{}, nil)
		if source != nil {
			source.close()
		}
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("registry read did not start")
	}
	cancel()
	select {
	case err := <-finished:
		requireSignCode(t, err, e.CodeTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled discovery did not return")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled request body was not closed")
	}
}

func TestRegistryBundleCachesFailedReadsAndHonorsCancellation(t *testing.T) {
	f := newBundleRegistryFixture(t)
	descriptor := f.add(t, []byte(`{"bundle":"exact"}`), "", nil)
	f.index(t, []v1.Descriptor{descriptor})
	path := "/v2/fixture/output/manifests/" + descriptor.Digest.String()
	response := f.routes[path]
	response.body = []byte("broken manifest")
	f.routes[path] = response
	source, err := newRegistryBundleSource(t.Context(), f.subject, Credential{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	for range 2 {
		_, err := source.bundle(t.Context(), descriptor)
		requireSignCode(t, err, e.CodeSignatureInvalid)
	}
	if f.count(path) != 1 {
		t.Fatal("failed candidate was downloaded again")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = source.bundle(ctx, descriptor)
	requireSignCode(t, err, e.CodeTimeout)
}

func TestRegistryBundleVerifyCapsMatchingCandidates(t *testing.T) {
	f := newBundleRegistryFixture(t)
	var descriptors []v1.Descriptor
	for i := 0; i < 33; i++ {
		raw := []byte(qualificationBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer, fmt.Appendf(nil, `{"attempt":%d}`, i)))
		descriptors = append(descriptors, f.add(t, raw, CosignSignaturePredicateType, nil))
	}
	f.index(t, descriptors)
	verified := 0
	signer := newRegistryDiagnosticSigner(t, func([]byte, string) error { verified++; return errors.New("synthetic crypto rejection") })
	_, err := signer.Verify(t.Context(), f.subject, VerifyPolicy{})
	requireSignCode(t, err, e.CodeSignatureInvalid)
	if verified != 32 {
		t.Fatalf("crypto attempts = %d, want the 32 matching-candidate bound", verified)
	}
}

func TestRegistryBundleVerifyUnrelatedHistoryDoesNotConsumeTypeBudget(t *testing.T) {
	f := newBundleRegistryFixture(t)
	var descriptors []v1.Descriptor
	for i := 0; i < 33; i++ {
		raw := []byte(qualificationBundle(t, ociTestDigest, sbom.CycloneDXPredicateType, ociObservedIdentity, ociTestIssuer, fmt.Appendf(nil, `{"history":%d}`, i)))
		// Deliberately misleading hints force traversal of all unrelated history
		// before the real signature, without granting it signature authority.
		descriptors = append(descriptors, f.add(t, raw, CosignSignaturePredicateType, nil))
	}
	signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
	descriptors = append(descriptors, f.add(t, signature, "", nil))
	f.index(t, descriptors)
	verified := 0
	signer := newRegistryDiagnosticSigner(t, func(raw []byte, _ string) error {
		verified++
		if !bytes.Equal(raw, signature) {
			t.Error("unrelated history reached signature cryptography")
		}
		return nil
	})
	record, err := signer.Verify(t.Context(), f.subject, VerifyPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !record.SignatureVerified || verified != 1 {
		t.Fatalf("signature verification = %t, crypto calls = %d", record.SignatureVerified, verified)
	}
	for _, descriptor := range descriptors {
		if got := f.count("/v2/fixture/output/manifests/" + descriptor.Digest.String()); got != 1 {
			t.Errorf("mixed history manifest reads = %d, want one", got)
		}
	}
}

func TestRegistryBundleRejectsAmbiguousJSON(t *testing.T) {
	for _, location := range []string{"index", "manifest", "bundle"} {
		t.Run(location, func(t *testing.T) {
			f := newBundleRegistryFixture(t)
			bundle := []byte(`{"bundle":"exact"}`)
			if location == "bundle" {
				bundle = []byte(`{"bundle":"first","bundle":"second"}`)
			}
			descriptor := f.add(t, bundle, "", nil)
			if location == "manifest" {
				response := f.routes["/v2/fixture/output/manifests/"+descriptor.Digest.String()]
				response.body = bytes.Replace(response.body, []byte(`"schemaVersion":2`), []byte(`"schemaVersion":1,"schemaVersion":2`), 1)
				// Re-pin the ambiguous bytes so this isolates JSON ambiguity rather
				// than an ordinary raw manifest digest failure.
				descriptor.Digest = bundleRegistryHash(t, response.body)
				descriptor.Size = int64(len(response.body))
				f.routes["/v2/fixture/output/manifests/"+descriptor.Digest.String()] = response
			}
			f.index(t, []v1.Descriptor{descriptor})
			if location == "index" {
				path := "/v2/fixture/output/referrers/" + ociTestDigest
				response := f.routes[path]
				response.body = bytes.Replace(response.body, []byte(`"schemaVersion":2`), []byte(`"schemaVersion":1,"schemaVersion":2`), 1)
				f.routes[path] = response
			}
			source, err := newRegistryBundleSource(t.Context(), f.subject, Credential{}, nil)
			if source != nil {
				defer source.close()
			}
			if err == nil {
				_, err = source.bundle(t.Context(), descriptor)
			}
			requireSignCode(t, err, e.CodeSignatureInvalid)
		})
	}
}

func TestRegistryBundleVerifyRecoversFromEarlierCryptoFailure(t *testing.T) {
	f := newBundleRegistryFixture(t)
	earlier := []byte(qualificationBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer, []byte(`{"attempt":1}`)))
	valid := []byte(qualificationBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer, []byte(`{"attempt":2}`)))
	f.index(t, []v1.Descriptor{f.add(t, earlier, CosignSignaturePredicateType, nil), f.add(t, valid, "", nil)})
	var verified [][]byte
	signer := newRegistryDiagnosticSigner(t, func(raw []byte, _ string) error {
		verified = append(verified, bytes.Clone(raw))
		if bytes.Equal(raw, earlier) {
			return errors.New("synthetic earlier signature failure")
		}
		return nil
	})
	record, err := signer.Verify(t.Context(), f.subject, VerifyPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !record.SignatureVerified || len(verified) != 2 || !bytes.Equal(verified[0], earlier) || !bytes.Equal(verified[1], valid) {
		t.Fatal("verification did not continue from earlier failed signature to later valid bytes")
	}
}

func TestRegistryBundleVerifyMissingProvenanceReturnsNoAuthority(t *testing.T) {
	f := newBundleRegistryFixture(t)
	signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
	f.index(t, []v1.Descriptor{f.add(t, signature, CosignSignaturePredicateType, nil)})
	verified := 0
	signer := newRegistryDiagnosticSigner(t, func([]byte, string) error { verified++; return nil })
	record, err := signer.Verify(t.Context(), f.subject, VerifyPolicy{RequireAttestation: true})
	requireSignCode(t, err, e.CodeVerifyFailed)
	if record.SignatureVerified || record.AttestationVerified || len(record.Ran) != 0 || verified != 1 {
		t.Fatalf("missing provenance returned partial authority or skipped signature fixture: %+v, %d calls", record, verified)
	}
	if f.count("/v2/fixture/output/blobs/"+digestBytes(signature)) != 1 {
		t.Fatal("failed provenance search redownloaded previously read signature")
	}
}
