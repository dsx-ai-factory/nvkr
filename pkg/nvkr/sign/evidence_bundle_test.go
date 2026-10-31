// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// The AICR v1.1.2 linux/amd64 provenance bundle; see
// testdata/upstream-evidence/aicr-v1.1.2/README.md for its release source.
const aicrBundleFixture = "../../../testdata/upstream-evidence/aicr-v1.1.2/aicr-attestation.sigstore.json"

func aicrBundlePolicy() sbom.EvidenceBundlePolicy {
	return sbom.EvidenceBundlePolicy{
		SubjectDigest: "sha256:05c2c6ff0bec39d989a2b5c87930e7d57efdb82172b9f49fd2241b5ea9bc9c46",
		PredicateType: sbom.SLSAProvenanceV1PredicateType,
		Issuer:        "https://token.actions.githubusercontent.com",
		Identity:      "https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml@refs/tags/v1.1.2",
	}
}

func TestCosignEvidenceVerifierChecksAICRBundleClaimsBeforeCosign(t *testing.T) {
	bundle, err := os.ReadFile(aicrBundleFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		change   func(*sbom.EvidenceBundlePolicy)
		response error
		want     nvkrerr.Code
		runs     int
	}{
		{name: "AICR v1.1.2 signer", runs: 1},
		{name: "wrong signer identity", want: nvkrerr.CodeUntrustedSigner,
			change: func(p *sbom.EvidenceBundlePolicy) {
				p.Identity = "https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml@refs/tags/v1.1.1"
			}},
		{name: "identity is matched exactly", want: nvkrerr.CodeUntrustedSigner,
			change: func(p *sbom.EvidenceBundlePolicy) {
				p.Identity = "https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml"
			}},
		{name: "wrong issuer", want: nvkrerr.CodeUntrustedSigner,
			change: func(p *sbom.EvidenceBundlePolicy) { p.Issuer = "https://accounts.google.com" }},
		{name: "wrong subject digest", want: nvkrerr.CodeVerifyFailed,
			change: func(p *sbom.EvidenceBundlePolicy) {
				p.SubjectDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
			}},
		{name: "wrong predicate", want: nvkrerr.CodeVerifyFailed,
			change: func(p *sbom.EvidenceBundlePolicy) { p.PredicateType = "https://slsa.dev/provenance/v0.2" }},
		{name: "missing identity", want: nvkrerr.CodeTrustPolicyMissing,
			change: func(p *sbom.EvidenceBundlePolicy) { p.Identity = "" }},
		{name: "cosign rejects", response: errors.New("exit status 1"), want: nvkrerr.CodeSignatureInvalid, runs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := aicrBundlePolicy()
			if tc.change != nil {
				tc.change(&policy)
			}
			var runs [][]string
			runner := EnvRunnerFunc(func(_ context.Context, binary string, args, _ []string, _, _ io.Writer) error {
				runs = append(runs, append([]string{binary}, args...))
				if held, err := os.ReadFile(args[2]); err != nil || !bytes.Equal(held, bundle) {
					t.Errorf("cosign did not receive the exact bundle bytes: %v", err)
				}
				return tc.response
			})
			verifier, err := NewCosignEvidenceVerifier("cosign", "", runner)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifier.VerifyEvidenceBundle(t.Context(), bundle, policy); signCode(err) != tc.want {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			if len(runs) != tc.runs {
				t.Fatalf("cosign ran %d times, want %d", len(runs), tc.runs)
			}
			if tc.runs == 1 {
				want := []string{"cosign", "verify-blob-attestation", "--bundle", runs[0][3], "--type", "https://slsa.dev/provenance/v1",
					"--digest", "05c2c6ff0bec39d989a2b5c87930e7d57efdb82172b9f49fd2241b5ea9bc9c46", "--digestAlg", "sha256",
					"--certificate-identity-regexp", `^https://github\.com/NVIDIA/aicr/\.github/workflows/on-tag\.yaml@refs/tags/v1\.1\.2$`,
					"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com"}
				if !slices.Equal(runs[0], want) {
					t.Fatalf("cosign argv %q, want %q", runs[0], want)
				}
			}
		})
	}
}

func TestCosignEvidenceVerifierRejectsUnpinnedStockExecution(t *testing.T) {
	if _, err := NewCosignEvidenceVerifier("cosign", "sha256:short", nil); signCode(err) != nvkrerr.CodeUnpinnedInput {
		t.Fatalf("malformed digest: %v", err)
	}
	verifier, err := NewCosignEvidenceVerifier("cosign", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = verifier.VerifyEvidenceBundle(t.Context(), []byte("{}"), aicrBundlePolicy())
	if code := signCode(err); code != nvkrerr.CodeUnpinnedInput && code != nvkrerr.CodeEngineCapability {
		t.Fatalf("stock execution without a pinned cosign: %v", err)
	}
}

// TestCosignEvidenceVerifierAICRWithInstalledCosign runs a real cosign against
// the public-good Sigstore trust root. It needs network access and is opt-in:
// NVKR_TEST_UPSTREAM_COSIGN=/path/to/cosign go test ./pkg/nvkr/sign -run InstalledCosign
func TestCosignEvidenceVerifierAICRWithInstalledCosign(t *testing.T) {
	binary := os.Getenv("NVKR_TEST_UPSTREAM_COSIGN")
	if binary == "" {
		t.Skip("set NVKR_TEST_UPSTREAM_COSIGN to a cosign executable to verify the AICR bundle")
	}
	bundle, err := os.ReadFile(aicrBundleFixture)
	if err != nil {
		t.Fatal(err)
	}
	runner := EnvRunnerFunc(func(ctx context.Context, binary string, args, env []string, stdout, stderr io.Writer) error {
		command := exec.CommandContext(ctx, binary, args...)
		command.Env, command.Stdout, command.Stderr = env, stdout, stderr
		return command.Run()
	})
	verifier, err := NewCosignEvidenceVerifier(binary, "", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyEvidenceBundle(t.Context(), bundle, aicrBundlePolicy()); err != nil {
		t.Fatalf("AICR v1.1.2 bundle did not verify: %v", err)
	}
	tampered := bytes.Replace(bundle, []byte(`"sig":"`), []byte(`"sig":"A`), 1)
	if err := verifier.VerifyEvidenceBundle(t.Context(), tampered, aicrBundlePolicy()); signCode(err) != nvkrerr.CodeSignatureInvalid {
		t.Fatalf("tampered signature: %v", err)
	}
}
