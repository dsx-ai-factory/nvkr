// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// AICR v1.1.2 linux/amd64 release evidence; testdata/upstream-evidence/aicr-v1.1.2/README.md
// records each source URL.
const (
	aicrFixtureDir            = "../../../testdata/upstream-evidence/aicr-v1.1.2/"
	aicrExecutableDigest      = "sha256:05c2c6ff0bec39d989a2b5c87930e7d57efdb82172b9f49fd2241b5ea9bc9c46"
	aicrAttestationDigest     = "sha256:57a1cb3771174c07dd38ae11172027400d6d22643f38f6083bcf73ecf7c8c91f"
	aicrSBOMDigest            = "sha256:f56130e11d37bcd24031fcf7da317260e53392dfbbbcfceaecaeb8f0472df403"
	aicrSBOMURL               = "https://github.com/NVIDIA/aicr/releases/download/v1.1.2/aicr_1.1.2_linux_amd64.sbom.json"
	aicrArchiveURL            = "https://github.com/NVIDIA/aicr/releases/download/v1.1.2/aicr_1.1.2_linux_amd64.tar.gz"
	aicrSignerIssuer          = "https://token.actions.githubusercontent.com"
	aicrSignerIdentity        = "https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml@refs/tags/v1.1.2"
	aicrAttestationArchiveRef = "archive:aicr-attestation.sigstore.json"
)

func aicrFixtures(t testing.TB) (attestation, spdx []byte) {
	t.Helper()
	attestation, err := os.ReadFile(aicrFixtureDir + "aicr-attestation.sigstore.json")
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := os.ReadFile(aicrFixtureDir + "aicr_1.1.2_linux_amd64.sbom.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	if spdx, err = io.ReadAll(gz); err != nil {
		t.Fatal(err)
	}
	if evidenceDigest(attestation) != aicrAttestationDigest || evidenceDigest(spdx) != aicrSBOMDigest {
		t.Fatal("AICR v1.1.2 fixture bytes drifted from their recorded release digests")
	}
	return attestation, spdx
}

// aicrArchive packages the real attestation beside a stand-in executable; the
// 27 MB release archive is not vendored, and its digest pin is synthetic here.
func aicrArchive(t testing.TB, attestation []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, member := range []struct {
		name string
		body []byte
	}{{"aicr-attestation.sigstore.json", attestation}, {"aicr", []byte("stand-in executable")}} {
		if err := tw.WriteHeader(&tar.Header{Name: member.name, Size: int64(len(member.body)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(member.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func aicrResolved(t testing.TB, evidence *catalog.ExternalEvidence, archiveDigest, executableDigest string) (*buildgraph.Resolved, string) {
	t.Helper()
	tool := catalog.ToolPin{Format: "tar-gzip", URL: aicrArchiveURL, Platform: "linux/amd64", ExecutablePath: "aicr",
		ArchiveDigest: archiveDigest, ExecutableDigest: executableDigest}
	resolved := &buildgraph.Resolved{
		Platform: "linux/amd64",
		Components: []catalog.NamedComponent{{Name: "aicr-linux-amd64", Component: catalog.Component{Platforms: []string{"linux/amd64"},
			Tag: "v1.1.2", SourceDate: 1, OutputType: "host-package",
			Prebuilt: &catalog.Prebuilt{URL: tool.URL, Executable: &tool, Digests: map[string]string{"amd64": executableDigest}, Evidence: evidence},
		}}},
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	buildDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return resolved, buildDigest
}

type recordingBundleVerifier struct {
	calls    int
	bundle   []byte
	policy   EvidenceBundlePolicy
	response error
}

func (v *recordingBundleVerifier) VerifyEvidenceBundle(_ context.Context, bundle []byte, policy EvidenceBundlePolicy) error {
	v.calls++
	v.bundle, v.policy = bundle, policy
	return v.response
}

func TestBuildVerifiedAuthenticatesAICRSigstoreProvenanceAndSPDX(t *testing.T) {
	attestation, spdx := aicrFixtures(t)
	archive := aicrArchive(t, attestation)
	unsigned := externalProvenanceEvidence("aicr", aicrExecutableDigest)
	signed := func() *catalog.ExternalEvidence {
		return &catalog.ExternalEvidence{SBOMRef: aicrSBOMURL, SBOMDigest: aicrSBOMDigest,
			ProvenanceRef: aicrAttestationArchiveRef, ProvenanceDigest: aicrAttestationDigest,
			Signer: &catalog.EvidenceSigner{Issuer: aicrSignerIssuer, Identity: aicrSignerIdentity}}
	}
	for _, tc := range []struct {
		name             string
		change           func(*catalog.ExternalEvidence)
		executableDigest string
		archive          []byte
		response         error
		noVerifier       bool
		want             nvkrerr.Code
		wantCalls        int
	}{
		{name: "AICR v1.1.2 evidence verifies", wantCalls: 1},
		{name: "wrong signer identity", response: nvkrerr.New(nvkrerr.CodeUntrustedSigner, "untrusted identity"),
			want: nvkrerr.CodeUntrustedSigner, wantCalls: 1},
		{name: "uncoded verifier failure", response: errors.New("cosign exited 1"), want: nvkrerr.CodeSignatureInvalid, wantCalls: 1},
		{name: "wrong subject digest", executableDigest: evidenceDigest([]byte("other aicr")), want: nvkrerr.CodeUnverifiedRef},
		{name: "bundle without signer policy", change: func(ev *catalog.ExternalEvidence) { ev.Signer = nil },
			want: nvkrerr.CodeTrustPolicyMissing},
		{name: "signer policy over unsigned statement", change: func(ev *catalog.ExternalEvidence) {
			ev.ProvenanceRef, ev.ProvenanceDigest = "https://example.invalid/aicr.intoto.json", evidenceDigest(unsigned)
		}, want: nvkrerr.CodeSignatureInvalid},
		{name: "unsigned statement without signer", change: func(ev *catalog.ExternalEvidence) {
			ev.ProvenanceRef, ev.ProvenanceDigest, ev.Signer = "https://example.invalid/aicr.intoto.json", evidenceDigest(unsigned), nil
		}},
		{name: "no bundle verifier", noVerifier: true, want: nvkrerr.CodeUnavailable},
		{name: "substituted archive", archive: aicrArchive(t, append(append([]byte(nil), attestation...), ' ')),
			want: nvkrerr.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := signed()
			if tc.change != nil {
				tc.change(evidence)
			}
			executableDigest := aicrExecutableDigest
			if tc.executableDigest != "" {
				executableDigest = tc.executableDigest
			}
			resolved, buildDigest := aicrResolved(t, evidence, evidenceDigest(archive), executableDigest)
			served := archive
			if tc.archive != nil {
				served = tc.archive
			}
			reader := EvidenceReaderFunc(func(_ context.Context, ref string) ([]byte, error) {
				switch ref {
				case aicrSBOMURL:
					return spdx, nil
				case aicrArchiveURL:
					return served, nil
				case "https://example.invalid/aicr.intoto.json":
					return unsigned, nil
				}
				return nil, errors.New("unexpected reference " + ref)
			})
			verifier := &recordingBundleVerifier{response: tc.response}
			var seam EvidenceBundleVerifier = verifier
			if tc.noVerifier {
				seam = nil
			}
			set, err := BuildVerified(context.Background(), resolved, buildDigest,
				map[string]string{"aicr-linux-amd64": executableDigest}, reader, seam)
			if sbomCode(err) != tc.want {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			if verifier.calls != tc.wantCalls {
				t.Fatalf("bundle verifier calls = %d, want %d", verifier.calls, tc.wantCalls)
			}
			if tc.wantCalls == 1 && (!bytes.Equal(verifier.bundle, attestation) || verifier.policy != (EvidenceBundlePolicy{
				SubjectDigest: aicrExecutableDigest, PredicateType: SLSAProvenanceV1PredicateType,
				Issuer: aicrSignerIssuer, Identity: aicrSignerIdentity})) {
				t.Fatalf("verifier received %+v over %d bytes", verifier.policy, len(verifier.bundle))
			}
			if tc.want != "" {
				return
			}
			aggregate, err := set.Aggregate()
			if err != nil {
				t.Fatal(err)
			}
			properties := map[string]string{}
			for _, property := range aggregate.Components[0].Properties {
				properties[property.Name] = property.Value
			}
			if properties["nvkr:externalEvidence"] != "verified" || properties["nvkr:externalSbomDigest"] != aicrSBOMDigest ||
				properties["nvkr:externalProvenanceRef"] != evidence.ProvenanceRef ||
				properties["nvkr:externalProvenanceDigest"] != evidence.ProvenanceDigest {
				t.Fatalf("verified upstream bindings missing: %+v", properties)
			}
		})
	}
}

func TestExternalSBOMFormats(t *testing.T) {
	_, aicrSPDX := aicrFixtures(t)
	digest := evidenceDigest([]byte("artifact"))
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	spdx := func(body string) []byte {
		return []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT",` + body + `}`)
	}
	root := `{"SPDXID":"SPDXRef-root","name":"tool","checksums":[{"algorithm":"SHA256","checksumValue":"` + hexDigest + `"}]}`
	describes := `"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relatedSpdxElement":"SPDXRef-root","relationshipType":"DESCRIBES"}]`
	for _, tc := range []struct {
		name, subject, digest string
		body                  []byte
		want                  nvkrerr.Code
	}{
		{name: "AICR v1.1.2 SPDX 2.3", subject: "aicr", digest: aicrExecutableDigest, body: aicrSPDX},
		{name: "AICR SPDX wrong subject digest", subject: "aicr", digest: digest, body: aicrSPDX, want: nvkrerr.CodeUnverifiedRef},
		{name: "AICR SPDX wrong subject name", subject: "aicrd", digest: aicrExecutableDigest, body: aicrSPDX, want: nvkrerr.CodeUnverifiedRef},
		{name: "SPDX documentDescribes", subject: "tool", digest: digest,
			body: spdx(`"documentDescribes":["SPDXRef-root"],"packages":[` + root + `]`)},
		{name: "SPDX DESCRIBED_BY file", subject: "tool", digest: digest,
			body: spdx(`"files":[{"SPDXID":"SPDXRef-root","fileName":"tool","checksums":[{"algorithm":"SHA256","checksumValue":"` + hexDigest +
				`"}]}],"relationships":[{"spdxElementId":"SPDXRef-root","relatedSpdxElement":"SPDXRef-DOCUMENT","relationshipType":"DESCRIBED_BY"}]`)},
		{name: "SPDX describes two elements", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: spdx(`"documentDescribes":["SPDXRef-other"],"packages":[` + root + `,{"SPDXID":"SPDXRef-other","name":"tool"}],` + describes)},
		{name: "SPDX describes nothing", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: spdx(`"packages":[` + root + `]`)},
		{name: "SPDX duplicate described SPDXID", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: spdx(`"packages":[` + root + `,` + root + `],` + describes)},
		{name: "SPDX conflicting SHA256", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: spdx(`"packages":[{"SPDXID":"SPDXRef-root","name":"tool","checksums":[{"algorithm":"SHA256","checksumValue":"` + hexDigest +
				`"},{"algorithm":"SHA256","checksumValue":"` + strings.Repeat("0", 64) + `"}]}],` + describes)},
		{name: "SPDX missing SHA256", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: spdx(`"packages":[{"SPDXID":"SPDXRef-root","name":"tool","checksums":[{"algorithm":"SHA1","checksumValue":"` + strings.Repeat("0", 40) + `"}]}],` + describes)},
		{name: "SPDX 2.2", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: bytes.Replace(spdx(`"packages":[`+root+`],`+describes), []byte("SPDX-2.3"), []byte("SPDX-2.2"), 1)},
		{name: "SPDX 3.0 JSON-LD", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: []byte(`{"@context":"https://spdx.org/rdf/3.0.1/spdx-context.jsonld","@graph":[]}`)},
		{name: "SPDX tag-value", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: []byte("SPDXVersion: SPDX-2.3\nDataLicense: CC0-1.0\nSPDXID: SPDXRef-DOCUMENT\n")},
		{name: "CycloneDX", subject: "tool", digest: digest, body: externalSBOMEvidence("tool", digest)},
		{name: "ambiguous CycloneDX and SPDX", subject: "tool", digest: digest, want: nvkrerr.CodeUnverifiedRef,
			body: []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","spdxVersion":"SPDX-2.3"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateExternalSBOM(tc.body, tc.subject, tc.digest); sbomCode(err) != tc.want {
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}
}

func TestExternalProvenanceStatementVersions(t *testing.T) {
	digest := evidenceDigest([]byte("artifact"))
	v1 := externalProvenanceEvidence("tool", digest)
	for _, tc := range []struct {
		name string
		body []byte
		want nvkrerr.Code
	}{
		{name: "in-toto v1", body: v1},
		{name: "in-toto v0.1", body: bytes.Replace(v1, []byte("Statement/v1"), []byte("Statement/v0.1"), 1)},
		{name: "in-toto v0.2", body: bytes.Replace(v1, []byte("Statement/v1"), []byte("Statement/v0.2"), 1), want: nvkrerr.CodeUnverifiedRef},
		{name: "SLSA v0.2 predicate", body: bytes.Replace(v1, []byte("slsa.dev/provenance/v1"), []byte("slsa.dev/provenance/v0.2"), 1),
			want: nvkrerr.CodeUnverifiedRef},
		{name: "older Sigstore bundle", body: []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.2","dsseEnvelope":{}}`),
			want: nvkrerr.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateExternalProvenance(context.Background(), tc.body, "tool", digest, nil, nil); sbomCode(err) != tc.want {
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}
}
