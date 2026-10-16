// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func checkLocalBuildDocumentAdmission(t *testing.T, raw []byte, policy LocalQualificationPolicy) {
	t.Helper()
	for _, mode := range []string{"exact", "origin", "lane", "policy", "unknown-field", "trailing"} {
		t.Run("document/"+mode, func(t *testing.T) {
			var doc localBuildEvidence
			if err := canonical.UnmarshalExact(raw, &doc); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "origin":
				doc.Origin = "github"
			case "lane":
				doc.Lane = "qualified"
			case "policy":
				doc.PolicyDigest = nativeSmokeTestDigest("9")
			}
			changed, err := canonical.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unknown-field" {
				changed = append([]byte(`{"extra":true,`), changed[1:]...)
			}
			if mode == "trailing" {
				changed = append(changed, '\n')
			}
			err = validateLocalBuildDocument(changed, policy)
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			code := e.CodeTrustPolicyMissing
			if mode == "unknown-field" || mode == "trailing" {
				code = e.CodeVerifyFailed
			}
			assertCode(t, err, code)
		})
	}
}

// Called only by the opt-in real-cosign fixture. The consumer receives a copied
// attempt and public policy, with no producer capture or signing receipt.
func checkLocalBuildConsumerFixture(t *testing.T, workspace string, inputs *localQualificationInputs, policy LocalQualificationPolicy, binary string, document, signature []byte) {
	t.Helper()
	for _, mode := range []string{"exact", "signature", "changed-inputs", "changed-run", "changed-payload"} {
		t.Run("consumer/"+mode, func(t *testing.T) {
			destination := filepath.Join(".nvkr", "consumer-"+mode, inputs.run.Attempt)
			if err := os.CopyFS(destination, os.DirFS(workspace)); err != nil {
				t.Fatal(err)
			}
			sig := bytes.Clone(signature)
			switch mode {
			case "signature":
				sig = []byte("invalid")
			case "changed-inputs":
				if err := os.WriteFile(filepath.Join(destination, "compile-inputs.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed-run":
				run := inputs.run
				run.Attempt = "another"
				writeCanonicalTestFile(t, filepath.Join(destination, "lifecycle.json"), run)
			case "changed-payload":
				// Ordinary payloads are always present in both selected fixtures.
				if err := os.Rename(filepath.Join(destination, "packages", "cli"), filepath.Join(destination, "packages", "cli.saved")); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := openLocalVerifiedBuild(t.Context(), destination, policy, binary, document, sig)
			if mode != "exact" {
				if err == nil {
					_ = owner.Close()
					t.Fatal("consumer accepted changed evidence or bytes")
				}
				if mode == "signature" {
					assertCode(t, err, e.CodeSignatureInvalid)
				}
				if mode == "changed-run" || mode == "changed-payload" {
					assertCode(t, err, e.CodeVerifyFailed)
				}
				if mode == "changed-inputs" {
					assertCode(t, err, e.CodePipelineDrift)
				}
				if owner != nil {
					t.Fatal("failed verification returned a consumer owner")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			if owner.document != string(document) || len(owner.inputs.run.Observed) != len(inputs.run.Observed) {
				t.Fatal("consumer lost the complete signed binding")
			}
			if err := os.Rename(destination, destination+".moved"); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.capture.ordinary.WriteExecutableMember(t.Context(), "cli", "kubectl", io.Discard); err != nil {
				t.Fatal(err)
			}
			for _, disk := range owner.capture.disks.members {
				if _, err := disk.WriteToContext(t.Context(), io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			copied := *owner
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = copied.capture.ordinary.Outputs()
			assertCode(t, err, e.CodeGateUnmet)
		})
	}
}
