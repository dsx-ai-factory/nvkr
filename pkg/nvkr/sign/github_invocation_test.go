// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

const (
	invocationWorkerIdentity = "https://github.com/NVIDIA/nke-recipes/.github/workflows/nvkr-smoke.yml@refs/heads/main"
	invocationIssuer         = "https://token.actions.githubusercontent.com"
)

func invocationConstraints() trust.GitHubInvocationConstraints {
	return trust.GitHubInvocationConstraints{
		RunInvocationURI:  "https://github.com/NVIDIA/nke-recipes/actions/runs/42/attempts/2",
		BuildConfigURI:    "https://github.com/NVIDIA/nke-recipes/.github/workflows/nvkr-qualify.yml@refs/heads/main",
		BuildConfigDigest: strings.Repeat("a", 40),
	}
}

func invocationExtensions(t *testing.T) []pkix.Extension {
	t.Helper()
	value := invocationConstraints()
	return []pkix.Extension{
		utf8Extension(t, fulcioRunInvocationURI, value.RunInvocationURI),
		utf8Extension(t, fulcioBuildConfigURI, value.BuildConfigURI),
		utf8Extension(t, fulcioBuildConfigDigest, value.BuildConfigDigest),
	}
}

func utf8Extension(t *testing.T, oid asn1.ObjectIdentifier, value string) pkix.Extension {
	t.Helper()
	encoded, err := asn1.MarshalWithParams(value, "utf8")
	if err != nil {
		t.Fatal(err)
	}
	return pkix.Extension{Id: oid, Value: encoded}
}

// The fixed key, validity, SAN and extension bytes keep the certificate fixtures
// independent of the wall clock, GitHub and Fulcio. The diagnostic runner below
// tests the adapter contract, not external cryptographic or hosted acceptance.
func invocationCertificate(t *testing.T, extensions []pkix.Extension, identity, issuer string, emailSAN bool) []byte {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	identityURI, err := url.Parse(identity)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "fixed worker certificate"},
		NotBefore:    time.Unix(1, 0), NotAfter: time.Unix(4_102_444_800, 0),
		KeyUsage: x509.KeyUsageDigitalSignature,
		URIs:     []*url.URL{identityURI},
		ExtraExtensions: append([]pkix.Extension{{Id: fulcioOIDCIssuer, Value: []byte(issuer)}},
			extensions...),
	}
	if emailSAN {
		template.URIs = nil
		template.EmailAddresses = []string{identity}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func invocationRequest(t *testing.T, certificate []byte) trust.DocumentVerificationRequest {
	t.Helper()
	req, material := qualifiedRequest(t, invocationWorkerIdentity, invocationIssuer, true)
	block, _ := pem.Decode(certificate)
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","verificationMaterial":{"certificate":{"rawBytes":"` +
		base64.StdEncoding.EncodeToString(block.Bytes) + `"}}}`)
	if err := os.WriteFile(material.CertificatePath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(material.BundlePath, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	expected := invocationConstraints()
	req.GitHubInvocation = &expected
	req.ExactBytes = []byte(`{"name":"signed"}`)
	return req
}

func TestGitHubInvocationRequiresExactVerifiedLeafClaims(t *testing.T) {
	type certificateCase struct {
		name     string
		change   func([]pkix.Extension) []pkix.Extension
		identity string
		issuer   string
		emailSAN bool
		without  bool
		want     e.Code
	}
	cases := []certificateCase{
		{name: "exact"},
		{name: "wrong run", change: func(x []pkix.Extension) []pkix.Extension {
			x[0] = utf8Extension(t, fulcioRunInvocationURI, strings.ReplaceAll(invocationConstraints().RunInvocationURI, "/42/", "/43/"))
			return x
		}, want: e.CodeUntrustedSigner},
		{name: "wrong attempt", change: func(x []pkix.Extension) []pkix.Extension {
			x[0] = utf8Extension(t, fulcioRunInvocationURI, strings.ReplaceAll(invocationConstraints().RunInvocationURI, "/attempts/2", "/attempts/1"))
			return x
		}, want: e.CodeUntrustedSigner},
		{name: "wrong source commit", change: func(x []pkix.Extension) []pkix.Extension {
			x[2] = utf8Extension(t, fulcioBuildConfigDigest, strings.Repeat("b", 40))
			return x
		}, want: e.CodeUntrustedSigner},
		{name: "worker instead of initiating workflow", change: func(x []pkix.Extension) []pkix.Extension {
			x[1] = utf8Extension(t, fulcioBuildConfigURI, invocationWorkerIdentity)
			return x
		}, want: e.CodeUntrustedSigner},
		{name: "wrong repository", change: func(x []pkix.Extension) []pkix.Extension {
			x[0] = utf8Extension(t, fulcioRunInvocationURI, strings.ReplaceAll(invocationConstraints().RunInvocationURI, "NVIDIA", "fork"))
			return x
		}, want: e.CodeUntrustedSigner},
		{name: "wrong worker SAN", identity: invocationConstraints().BuildConfigURI, want: e.CodeUntrustedSigner},
		{name: "wrong issuer", issuer: "https://issuer.example.test", want: e.CodeUntrustedSigner},
		{name: "workflow email is not URI SAN", emailSAN: true, want: e.CodeUntrustedSigner},
		{name: "unrequested claims do not change legacy behavior", without: true, change: func(x []pkix.Extension) []pkix.Extension {
			x[0].Value = []byte("not DER")
			return x
		}},
	}
	for _, claim := range []struct {
		name  string
		index int
	}{
		{"run", 0}, {"config", 1}, {"commit", 2},
	} {
		cases = append(cases,
			certificateCase{name: "missing " + claim.name, change: func(x []pkix.Extension) []pkix.Extension {
				return append(x[:claim.index], x[claim.index+1:]...)
			}, want: e.CodeSignatureInvalid},
			certificateCase{name: "duplicate " + claim.name, change: func(x []pkix.Extension) []pkix.Extension {
				return append(x, x[claim.index])
			}, want: e.CodeSignatureInvalid})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extensions := invocationExtensions(t)
			if tc.change != nil {
				extensions = tc.change(extensions)
			}
			identity, issuer := tc.identity, tc.issuer
			if identity == "" {
				identity = invocationWorkerIdentity
			}
			if issuer == "" {
				issuer = invocationIssuer
			}
			req := invocationRequest(t, invocationCertificate(t, extensions, identity, issuer, tc.emailSAN))
			if tc.without {
				req.GitHubInvocation = nil
			}
			verifier := newTestCosignVerifier("fixture", RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error { return nil }))
			evidence, err := verifier.VerifyDocument(context.Background(), req)
			if signCode(err) != tc.want {
				t.Fatalf("code=%s want=%s: %v", signCode(err), tc.want, err)
			}
			if tc.want != "" {
				if !reflect.DeepEqual(evidence, trust.VerificationEvidence{}) {
					t.Fatalf("failed verification returned claims: %+v", evidence)
				}
				return
			}
			if tc.without {
				if evidence.GitHubInvocation != nil {
					t.Fatalf("unexpected claims: %+v", evidence.GitHubInvocation)
				}
				return
			}
			if evidence.GitHubInvocation == nil || *evidence.GitHubInvocation != invocationConstraints() || evidence.OIDCIssuer != invocationIssuer {
				t.Fatalf("inexact evidence: %+v", evidence)
			}
			if evidence.GitHubInvocation == req.GitHubInvocation {
				t.Fatal("diagnostic aliases request")
			}
		})
	}
}

func TestGitHubInvocationRejectsMalformedClaimEncoding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		encoded []byte
	}{
		{"raw", []byte(invocationConstraints().RunInvocationURI)},
		{"empty", []byte{0x0c, 0}},
		{"printable", []byte{0x13, 1, 'x'}},
		{"trailing", []byte{0x0c, 1, 'x', 0}},
		{"invalid utf8", []byte{0x0c, 1, 0xff}},
		{"constructed", []byte{0x2c, 1, 'x'}},
		{"nonminimal length", []byte{0x0c, 0x81, 1, 'x'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extensions := invocationExtensions(t)
			extensions[0].Value = tc.encoded
			req := invocationRequest(t, invocationCertificate(t, extensions, invocationWorkerIdentity, invocationIssuer, false))
			verifier := newTestCosignVerifier("fixture", RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error { return nil }))
			if _, err := verifier.VerifyDocument(context.Background(), req); signCode(err) != e.CodeSignatureInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestGitHubInvocationRejectsInvalidPolicyBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*trust.DocumentVerificationRequest)
	}{
		{"empty run", func(r *trust.DocumentVerificationRequest) { r.GitHubInvocation.RunInvocationURI = "" }},
		{"missing attempt", func(r *trust.DocumentVerificationRequest) {
			r.GitHubInvocation.RunInvocationURI = "https://github.com/NVIDIA/nke-recipes/actions/runs/42"
		}},
		{"query", func(r *trust.DocumentVerificationRequest) { r.GitHubInvocation.RunInvocationURI += "?attempt=2" }},
		{"leading zero", func(r *trust.DocumentVerificationRequest) {
			r.GitHubInvocation.RunInvocationURI = strings.ReplaceAll(r.GitHubInvocation.RunInvocationURI, "/42/", "/042/")
		}},
		{"repository mismatch", func(r *trust.DocumentVerificationRequest) {
			r.GitHubInvocation.BuildConfigURI = strings.ReplaceAll(r.GitHubInvocation.BuildConfigURI, "NVIDIA", "fork")
		}},
		{"tag config", func(r *trust.DocumentVerificationRequest) {
			r.GitHubInvocation.BuildConfigURI = strings.ReplaceAll(r.GitHubInvocation.BuildConfigURI, "refs/heads", "refs/tags")
		}},
		{"branch traversal", func(r *trust.DocumentVerificationRequest) { r.GitHubInvocation.BuildConfigURI += "/../other" }},
		{"uppercase commit", func(r *trust.DocumentVerificationRequest) {
			r.GitHubInvocation.BuildConfigDigest = strings.Repeat("A", 40)
		}},
		{"prefixed commit", func(r *trust.DocumentVerificationRequest) {
			r.GitHubInvocation.BuildConfigDigest = "sha1:" + r.GitHubInvocation.BuildConfigDigest
		}},
		{"wrong issuer", func(r *trust.DocumentVerificationRequest) { r.OIDCIssuer = "https://issuer.example.test" }},
		{"no signer", func(r *trust.DocumentVerificationRequest) { r.AllowedIdentities = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := invocationConstraints()
			req := trust.DocumentVerificationRequest{SnapshotPath: "/must-not-open", OIDCIssuer: invocationIssuer, AllowedIdentities: []string{invocationWorkerIdentity}, GitHubInvocation: &expected}
			tc.change(&req)
			verifier := newTestCosignVerifier("must-not-run", RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
				t.Fatal("runner reached invalid policy")
				return nil
			}))
			if _, err := verifier.VerifyDocument(context.Background(), req); signCode(err) != e.CodeTrustPolicyMissing {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestGitHubInvocationSnapshotsExpectedClaimsAndRequiresVerification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		fail   bool
	}{
		{name: "request mutation"}, {name: "canceled after runner", cancel: true}, {name: "failed signature", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := invocationRequest(t, invocationCertificate(t, invocationExtensions(t), invocationWorkerIdentity, invocationIssuer, false))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			verifier := newTestCosignVerifier("fixture", RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
				req.GitHubInvocation.BuildConfigDigest = strings.Repeat("b", 40)
				req.AllowedIdentities[0] = "https://attacker.invalid"
				if tc.cancel {
					cancel()
				}
				if tc.fail {
					return errors.New("signature rejected")
				}
				return nil
			}))
			evidence, err := verifier.VerifyDocument(ctx, req)
			want := e.Code("")
			if tc.cancel {
				want = e.CodeTimeout
			} else if tc.fail {
				want = e.CodeSignatureInvalid
			}
			if signCode(err) != want {
				t.Fatalf("code=%s want=%s err=%v", signCode(err), want, err)
			}
			if want == "" && (evidence.GitHubInvocation == nil || *evidence.GitHubInvocation != invocationConstraints()) {
				t.Fatalf("evidence=%+v", evidence)
			}
			if want != "" && !reflect.DeepEqual(evidence, trust.VerificationEvidence{}) {
				t.Fatalf("failure returned evidence=%+v", evidence)
			}
		})
	}
}
