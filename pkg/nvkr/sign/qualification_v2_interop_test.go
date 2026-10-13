// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Local-key interoperability tests stock cosign transport only. Production v2
// requires keyless certificate identity/issuer and never falls back to this key.
func TestQualificationV2CosignInterop(t *testing.T) {
	binary := os.Getenv("NVKR_TEST_COSIGN_BINARY")
	if binary == "" {
		t.Skip("set NVKR_TEST_COSIGN_BINARY for real local-key transport")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	workspace := t.TempDir()
	run := func(args []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workspace
		cmd.Env = append(ociCosignEnvironment(os.Environ(), workspace), "COSIGN_PASSWORD=", "HOME="+workspace)
		return cmd.CombinedOutput()
	}
	require := func(args []string) []byte {
		t.Helper()
		out, err := run(args)
		if err != nil {
			t.Fatalf("cosign %s: %v %s", args[0], err, out)
		}
		return out
	}
	require([]string{"generate-key-pair"})
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	ref, err := name.NewTag(strings.TrimPrefix(server.URL, "http://") + "/qualification:v2")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, empty.Image, remote.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
	digest, err := empty.Image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	subject := ref.Context().Digest(digest.String()).String()
	raw := []byte(`{"schemaVersion":"nvkr.dev/qualification-evidence/v2","testFixture":true}`)
	for path, document := range map[string][]byte{"predicate.json": raw, "trusted-root.json": []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[],"certificateAuthorities":[],"ctlogs":[],"timestampAuthorities":[]}`)} {
		if err := os.WriteFile(filepath.Join(workspace, path), document, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := qualificationAttestArgsV2(subject, "predicate.json")
	args = append(args[:len(args)-1], "--key", "cosign.key", "--use-signing-config=false", "--tlog-upload=false", subject)
	require(args)
	bundles, err := decodeOCIBundles(require(ociDownloadBundleArgs(subject, QualificationPredicateTypeV2)))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 1 {
		t.Fatalf("v2 bundles=%d", len(bundles))
	}
	path := filepath.Join(workspace, "bundle.json")
	if err := os.WriteFile(path, bundles[0], 0600); err != nil {
		t.Fatal(err)
	}
	args = ociVerifyBundleArgs(path, digest.String(), QualificationPredicateTypeV2, VerifyPolicy{})[:9]
	args = append(args, "--key", "cosign.pub", "--insecure-ignore-tlog", "--offline", "--trusted-root", "trusted-root.json")
	require(args)
	wrong := append([]string(nil), args...)
	wrong[4] = QualificationPredicateType
	if _, err := run(wrong); err == nil {
		t.Fatal("v2 attachment accepted as v1 qualification")
	}
	wrong = append([]string(nil), args...)
	wrong[6] = strings.Repeat("a", 64)
	if _, err := run(wrong); err == nil {
		t.Fatal("v2 attachment accepted for another subject")
	}
	var bundle map[string]any
	if err := json.Unmarshal(bundles[0], &bundle); err != nil {
		t.Fatal(err)
	}
	envelope := bundle["dsseEnvelope"].(map[string]any)
	payload, err := base64.StdEncoding.DecodeString(envelope["payload"].(string))
	if err != nil {
		t.Fatal(err)
	}
	envelope["payload"] = base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(string(payload), `"testFixture":true`, `"testFixture":false`)))
	mutated, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, mutated, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(args); err == nil {
		t.Fatal("v2 attachment tampering accepted")
	}
}
