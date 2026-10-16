// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalEvidenceFileAdmission(t *testing.T) {
	for _, mode := range []string{"exact", "disk", "missing", "unknown", "empty", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			files := map[string][]byte{}
			for _, name := range []string{"build-evidence.json", "build-evidence.sig", "tool-smoke.json", "tool-smoke.sig", "qualification.json", "qualification.sig", "plan.json", "lifecycle.json", "compile-inputs.json"} {
				files[name] = []byte("fixture")
			}
			code := e.CodeReleaseIncomplete
			switch mode {
			case "disk":
				files["disk-materials.json"] = []byte("materials")
			case "missing":
				delete(files, "qualification.sig")
			case "unknown":
				files["../key"] = []byte("unknown")
			case "empty":
				files["tool-smoke.sig"] = nil
				code = e.CodeVerifyFailed
			case "oversized":
				files["qualification.sig"] = make([]byte, (64<<10)+1)
				code = e.CodeVerifyFailed
			}
			frozen, err := freezeLocalEvidenceFiles(files)
			if mode != "exact" && mode != "disk" {
				assertCode(t, err, code)
				if frozen != nil {
					t.Fatal("rejected set returned files")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, raw := range files {
				raw[0] ^= 1
				if bytes.Equal(raw, frozen[name]) {
					t.Fatal("snapshot aliases caller bytes")
				}
			}
		})
	}
}

// Real signatures over fixture payloads and simulated smoke. The consumer gets
// only copied retained files and public policy, never the producer's owners.
func checkLocalQualificationConsumerFixture(t *testing.T, workspace string, policy LocalQualificationPolicy, binary string, source *localVerifiedQualification) {
	t.Helper()
	for _, mode := range []string{"exact", "builder-signature", "smoke-signature", "qualifier-signature", "input-substitution", "missing-payload"} {
		t.Run("complete consumer/"+mode, func(t *testing.T) {
			bundlePath := filepath.Join(t.TempDir(), "evidence.json")
			if published, err := source.writeEvidenceBundle(t.Context(), bundlePath); err != nil || !published {
				t.Fatalf("persisting evidence: published=%v, err=%v", published, err)
			}
			files, err := readLocalEvidenceBundle(t.Context(), bundlePath)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(".nvkr", "qualification-consumer-"+mode, source.build.inputs.run.Attempt)
			if err := os.CopyFS(destination, os.DirFS(workspace)); err != nil {
				t.Fatal(err)
			}
			code := e.CodeSignatureInvalid
			switch mode {
			case "builder-signature":
				files["build-evidence.sig"] = []byte("invalid")
			case "smoke-signature":
				files["tool-smoke.sig"] = []byte("invalid")
			case "qualifier-signature":
				files["qualification.sig"] = []byte("invalid")
			case "input-substitution":
				files["compile-inputs.json"] = []byte("{}")
				code = e.CodeVerifyFailed
			case "missing-payload":
				if err := os.Rename(filepath.Join(destination, "packages", "cli"), filepath.Join(destination, "packages", "cli.saved")); err != nil {
					t.Fatal(err)
				}
				code = e.CodeVerifyFailed
			}
			consumer, err := openLocalQualificationConsumer(t.Context(), destination, policy, binary, files)
			if mode != "exact" {
				assertCode(t, err, code)
				if consumer != nil {
					_ = consumer.Close()
					t.Fatal("failed verification returned owner")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = consumer.Close() })
			if err := os.Rename(destination, destination+".moved"); err != nil {
				t.Fatal(err)
			}
			for _, raw := range files {
				raw[0] ^= 1
			}
			if err := consumer.qualified.requireLive(t.Context()); err != nil {
				t.Fatal(err)
			}
			copyOwner := *consumer
			if err := consumer.Close(); err != nil {
				t.Fatal(err)
			}
			assertCode(t, copyOwner.qualified.requireLive(t.Context()), e.CodeGateUnmet)
			if err := copyOwner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := source.requireLive(t.Context()); err != nil {
				t.Fatal("consumer closed producer owner", err)
			}
		})
	}
}
