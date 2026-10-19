// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// Model authenticated owners to test gate ordering on hosts without cosign.
// This does not establish signature verification or real smoke execution.
func checkLocalQualificationGateFixture(t *testing.T, receipt *localToolSmokeReceipt) {
	t.Helper()
	build := *receipt.build
	build.signature = "modeled-builder-signature"
	checkLocalEvidenceTransport(t, &build, receipt.raw)
	for _, mode := range []string{"exact", "missing-smoke", "closed-smoke", "missing-builder-signature", "missing-smoke-signature", "different-build", "changed-report"} {
		t.Run("qualification gate/"+mode, func(t *testing.T) {
			candidate := build
			smoke := &localVerifiedSmoke{raw: receipt.raw, signature: "modeled-smoke-signature", build: &candidate, closed: new(atomic.Bool)}
			code := e.CodeGateUnmet
			switch mode {
			case "missing-smoke":
				smoke = nil
			case "closed-smoke":
				_ = smoke.Close()
			case "missing-builder-signature":
				candidate.signature = ""
				code = e.CodeProvenanceMissing
			case "missing-smoke-signature":
				smoke.signature = ""
				code = e.CodeProvenanceMissing
			case "different-build":
				smoke.build = &build
			case "changed-report":
				smoke.raw = "{}"
				code = e.CodeVerifyFailed
			}
			calls := 0
			signed, err := signLocalQualificationWith(t.Context(), &candidate, smoke, nil, func(_ context.Context, raw, _, key []byte) (sign.LocalSignedDocument, error) {
				calls++
				if string(key) != candidate.policy.QualifierKey {
					t.Fatal("qualification used wrong key role")
				}
				var doc localQualificationEvidence
				if err := canonical.UnmarshalExact(raw, &doc); err != nil {
					t.Fatal(err)
				}
				if len(doc.Evidence) != 4 || doc.ContentSubject != candidate.inputs.run.ContentSubject || doc.GateVersion != "local-m3-v1" {
					t.Fatal("qualification lost complete evidence references")
				}
				return sign.LocalSignedDocument{}, nil
			})
			if mode == "exact" {
				code = e.CodeSignatureInvalid
				if calls != 1 {
					t.Fatal("complete gate did not reach signer")
				}
			} else if calls != 0 {
				t.Fatal("failed gate reached qualifier signer")
			}
			assertCode(t, err, code)
			if signed != nil {
				t.Fatal("unissued qualification signature returned authority")
			}
		})
	}
}

func checkRealLocalQualification(t *testing.T, workspace string, build *localVerifiedBuild, smoke *localVerifiedSmoke, binary string) {
	t.Helper()
	qualifierPath := os.Getenv("NVKR_TEST_LOCAL_QUALIFIER_KEY")
	if qualifierPath == "" {
		return
	}
	builderPrivate, err := os.ReadFile(os.Getenv("NVKR_TEST_LOCAL_BUILDER_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	signedBuild, err := signLocalBuildEvidence(t.Context(), build.inputs, build.capture, build.policy, binary, builderPrivate)
	if err != nil {
		t.Fatal(err)
	}
	buildSignature, err := signedBuild.signed.Signature()
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's source document now has a real, verified builder signature.
	// Production owners retain this only after independent input/payload replay.
	build.signature = string(buildSignature)
	private, err := os.ReadFile(qualifierPath)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signLocalQualification(t.Context(), build, smoke, binary, private)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signed.signed.Signature()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exact", "changed-document", "invalid-signature", "builder-signature"} {
		t.Run("real qualification/"+mode, func(t *testing.T) {
			document, sig := []byte(signed.document), signature
			switch mode {
			case "changed-document":
				document = append(document, '\n')
			case "invalid-signature":
				sig = []byte("invalid")
			case "builder-signature":
				adapter, err := sign.NewLocalCosign(binary, build.policy.CosignDigest)
				if err != nil {
					t.Fatal(err)
				}
				wrong, err := adapter.SignDocument(t.Context(), document, builderPrivate, []byte(build.policy.BuilderKey))
				if err != nil {
					t.Fatal(err)
				}
				sig, err = wrong.Signature()
				if err != nil {
					t.Fatal(err)
				}
			}
			verified, err := verifyLocalQualification(t.Context(), build, smoke, binary, document, sig)
			if mode != "exact" {
				code := e.CodeSignatureInvalid
				if mode == "changed-document" {
					code = e.CodeVerifyFailed
				}
				assertCode(t, err, code)
				if verified != nil {
					t.Fatal("failed qualification verification returned owner")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := verified.requireLive(t.Context()); err != nil {
				t.Fatal(err)
			}
			files, err := verified.evidenceFiles(t.Context())
			if err != nil || string(files["qualification.sig"]) != string(sig) {
				t.Fatalf("verified qualification lost transport signature: %v", err)
			}
			checkLocalQualificationConsumerFixture(t, workspace, build.policy, binary, verified)
			checkRealLocalLayoutConsumer(t, verified, binary)
			checkLocalQualificationEntrypoint(t, workspace, build.policy, binary, build.inputs.run.Attempt)
			copied := *verified
			_ = verified.Close()
			assertCode(t, copied.requireLive(t.Context()), e.CodeGateUnmet)
		})
	}
}

func checkLocalEvidenceTransport(t *testing.T, build *localVerifiedBuild, report string) {
	t.Helper()
	for _, mode := range []string{"exact", "missing-signature", "closed", "changed-document", "canceled"} {
		t.Run("evidence transport/"+mode, func(t *testing.T) {
			smoke := &localVerifiedSmoke{raw: report, signature: "modeled-smoke-signature", build: build, closed: new(atomic.Bool)}
			raw, err := prepareLocalQualification(t.Context(), build, smoke)
			if err != nil {
				t.Fatal(err)
			}
			owner := &localVerifiedQualification{raw: string(raw), signature: "modeled-qualifier-signature", build: build, smoke: smoke, closed: new(atomic.Bool)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeGateUnmet
			switch mode {
			case "missing-signature":
				owner.signature = ""
				code = e.CodeProvenanceMissing
			case "closed":
				_ = owner.Close()
			case "changed-document":
				owner.raw = "{}"
				code = e.CodeVerifyFailed
			case "canceled":
				cancel()
			}
			files, err := owner.evidenceFiles(ctx)
			if mode != "exact" {
				if mode == "canceled" {
					if err == nil {
						t.Fatal("canceled export succeeded")
					}
				} else {
					assertCode(t, err, code)
				}
				if files != nil {
					t.Fatal("failed export returned partial evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			checkLocalEvidenceBundleFixture(t, owner)
			checkLocalPublicationAdmission(t, owner)
			checkLocalLayoutCaptureFixture(t, owner)
			want := 9
			if build.capture.materialBytes != "" {
				want++
			}
			if len(files) != want {
				t.Fatalf("got %d files, want %d", len(files), want)
			}
			for _, name := range []string{"build-evidence.json", "build-evidence.sig", "tool-smoke.json", "tool-smoke.sig", "qualification.json", "qualification.sig", "plan.json", "lifecycle.json", "compile-inputs.json"} {
				if len(files[name]) == 0 {
					t.Fatalf("missing %s", name)
				}
			}
			for _, raw := range files {
				raw[0] ^= 1
			}
			fresh, err := owner.evidenceFiles(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if string(fresh["qualification.json"]) != owner.raw || string(fresh["plan.json"]) != string(build.inputs.planBytes) || string(fresh["compile-inputs.json"]) != string(build.inputs.inputBytes) {
				t.Fatal("export aliases authenticated source bytes")
			}
		})
	}
}
