// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func checkLocalSmokeAuthentication(t *testing.T, workspace string, receipt *localToolSmokeReceipt) {
	t.Helper()
	build := receipt.build
	checkLocalQualificationGateFixture(t, receipt)
	for _, mode := range []string{"exact", "source", "policy", "platform", "runtime", "missing-member", "duplicate-member", "subject", "version", "unknown-field"} {
		t.Run("smoke authentication/"+mode, func(t *testing.T) {
			report, err := receipt.description(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			code := e.CodeVerifyFailed
			switch mode {
			case "source":
				report.BuildEvidenceDigest = nativeSmokeTestDigest("9")
			case "policy":
				report.PolicyDigest = nativeSmokeTestDigest("9")
			case "platform":
				report.Platform = "darwin/arm64"
			case "runtime":
				report.RuntimeIdentity = "missing"
			case "missing-member":
				report.Members = nil
				code = e.CodeReleaseIncomplete
			case "duplicate-member":
				report.Members = append(report.Members, report.Members[0])
				code = e.CodeReleaseIncomplete
			case "subject":
				report.Members[0].PayloadDigest = nativeSmokeTestDigest("9")
				code = e.CodeGateUnmet
			case "version":
				report.Members[0].ObservedVersion = "v99.0.0"
				code = e.CodeGateUnmet
			}
			raw, err := canonical.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unknown-field" {
				raw = append([]byte(`{"extra":true,`), raw[1:]...)
			}
			_, err = validateLocalSmokeReport(t.Context(), build, raw)
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, code)
			}
		})
	}
	_, err := signLocalSmoke(t.Context(), nil, "", nil)
	assertCode(t, err, e.CodeGateUnmet)
	calls := 0
	signer := func(_ context.Context, document, _, key []byte) (sign.LocalSignedDocument, error) {
		calls++
		if string(document) != receipt.raw || string(key) != build.policy.SmokeKey {
			t.Fatal("smoke signer received wrong role or bytes")
		}
		return sign.LocalSignedDocument{}, nil
	}
	signed, err := signLocalSmokeWith(t.Context(), receipt, nil, signer)
	assertCode(t, err, e.CodeSignatureInvalid)
	if signed != nil || calls != 1 {
		t.Fatal("unissued smoke signature accepted")
	}
	empty := &localToolSmokeReceipt{build: build}
	signed, err = signLocalSmokeWith(t.Context(), empty, nil, signer)
	assertCode(t, err, e.CodeGateUnmet)
	if signed != nil || calls != 1 {
		t.Fatal("unissued execution reached signer")
	}
	binary, keyPath := os.Getenv("NVKR_TEST_COSIGN_BINARY"), os.Getenv("NVKR_TEST_LOCAL_SMOKE_KEY")
	if binary == "" || keyPath == "" {
		return
	}
	private, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signed, err = signLocalSmoke(t.Context(), receipt, binary, private)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signed.signed.Signature()
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifyLocalSmoke(t.Context(), build, binary, []byte(signed.document), signature)
	if err != nil {
		t.Fatal(err)
	}
	report, err := verified.description(t.Context())
	if err != nil || len(report.Members) != 1 {
		t.Fatalf("verified smoke lost observations: %v", err)
	}
	checkRealLocalQualification(t, workspace, build, verified, binary)
	copyReceipt := *verified
	if err := verified.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = copyReceipt.description(t.Context())
	assertCode(t, err, e.CodeGateUnmet)
	_, err = verifyLocalSmoke(t.Context(), build, binary, []byte(signed.document), []byte("invalid"))
	assertCode(t, err, e.CodeSignatureInvalid)
	// A valid builder-role signature over the same report is still not a smoke signature.
	builderPrivate, err := os.ReadFile(os.Getenv("NVKR_TEST_LOCAL_BUILDER_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	builderPublic, err := os.ReadFile(os.Getenv("NVKR_TEST_LOCAL_BUILDER_PUB"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := sign.NewLocalCosign(binary, build.policy.CosignDigest)
	if err != nil {
		t.Fatal(err)
	}
	wrongRole, err := adapter.SignDocument(t.Context(), []byte(signed.document), builderPrivate, builderPublic)
	if err != nil {
		t.Fatal(err)
	}
	wrongSignature, err := wrongRole.Signature()
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifyLocalSmoke(t.Context(), build, binary, []byte(signed.document), wrongSignature)
	assertCode(t, err, e.CodeSignatureInvalid)
}
