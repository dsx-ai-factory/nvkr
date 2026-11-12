// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Exercise the actual explicit-key arguments, cryptography and OCI discovery.
// The injected process runner permits macOS interoperability testing; it does
// not establish the Linux held-executable or Kubernetes execution boundary.
func TestTektonKeyedCosignInteroperability(t *testing.T) {
	binary := os.Getenv("NVKR_TEST_COSIGN_BINARY")
	if binary == "" {
		t.Skip("set NVKR_TEST_COSIGN_BINARY to the pinned cosign executable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	keyPath := filepath.Join(workspace, "cosign")
	cmd := exec.CommandContext(ctx, binary, "generate-key-pair", "--output-key-prefix", keyPath)
	cmd.Env = keyCosignEnvironment(os.Environ())
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture key generation: %v: %s", err, raw)
	}
	public, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	ref, err := name.NewTag(strings.TrimPrefix(server.URL, "http://") + "/core:test")
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
	subject := Subject{Ref: ref.Context().Digest(digest.String()).String(), Digest: digest.String()}
	identity := "https://cluster.example.invalid/api/v1/namespaces/nvkr/serviceaccounts/nvkr-publisher"
	options := CosignOCIOptions{KeyTrust: &OCIKeyTrust{PublicKey: public, Identity: identity}, PrivateKeyPath: keyPath + ".key", PlainHTTP: true,
		Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: "https://cluster.example.invalid",
		Runner: EnvRunnerFunc(func(ctx context.Context, _ string, args, env []string, stdout, stderr io.Writer) error {
			command := exec.CommandContext(ctx, binary, args...)
			command.Env, command.Stdout, command.Stderr = env, stdout, stderr
			return command.Run()
		})}
	signer, err := NewCosignOCISigner(options)
	if err != nil {
		t.Fatal(err)
	}
	predicate := []byte(`{"buildDefinition":{"buildType":"https://nvkr.dev/buildtypes/wrapped-build/v1","externalParameters":{}},"runDetails":{"builder":{"id":"tekton-key-interop"}}}`)
	if _, err := signer.SignAndAttest(ctx, subject, predicate); err != nil {
		t.Fatal(err)
	}
	policy := VerifyPolicy{RequireAttestation: true, ExpectedPredicate: predicate}
	if record, err := signer.Verify(ctx, subject, policy); err != nil || !record.SignatureVerified || !record.AttestationVerified || record.PublicKeySHA256 == "" {
		t.Fatalf("keyed signature and provenance: %+v: %v", record, err)
	}
	bom, err := sbom.NewSubject(ociTestDigest, "core", digest.String())
	if err != nil {
		t.Fatal(err)
	}
	document, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := signer.AttestSBOM(ctx, digest.String(), document)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.VerifySBOM(ctx, digest.String(), bundle); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		attest func(context.Context, Subject, []byte) error
		verify func(context.Context, Subject, []byte) error
	}{
		{"qualification", signer.AttestQualification, signer.VerifyQualification},
		{"release manifest", signer.AttestReleaseManifest, signer.VerifyReleaseManifest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			predicate := []byte(`{"fixture":"exact published predicate"}`)
			if err := tc.attest(ctx, subject, predicate); err != nil {
				t.Fatal(err)
			}
			if err := tc.verify(ctx, subject, predicate); err != nil {
				t.Fatal(err)
			}
			requireSignCode(t, tc.verify(ctx, subject, []byte(`{"fixture":"substituted"}`)), e.CodeVerifyFailed)
		})
	}
	options.PrivateKeyPath = ""
	options.KeyTrust = &OCIKeyTrust{PublicKey: testOCIKeyTrust(t).PublicKey, Identity: identity}
	wrong, err := NewCosignOCISigner(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrong.Verify(ctx, subject, policy)
	requireSignCode(t, err, e.CodeSignatureInvalid)
}
