// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// This opt-in interoperability test runs a real cosign against a loopback
// registry. Local keys and disabled transparency are fixture-only: this tests
// discovery and subject binding, not hosted keyless or transparency acceptance.
func TestCosignProvenanceWithSBOMReferrer(t *testing.T) {
	binary := os.Getenv("NVKR_TEST_COSIGN_BINARY")
	if binary == "" {
		t.Skip("set NVKR_TEST_COSIGN_BINARY to the pinned cosign executable")
	}
	for _, tc := range []struct {
		name string
		sbom bool
	}{{"root without SBOM referrer", false}, {"deliverable with SBOM referrer", true}} {
		t.Run(tc.name, func(t *testing.T) {
			testCosignProvenanceDiscovery(t, binary, tc.sbom)
		})
	}
}

func testCosignProvenanceDiscovery(t *testing.T, binary string, withSBOM bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	workspace := t.TempDir()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workspace
		cmd.Env = append(ociCosignEnvironment(os.Environ(), workspace), "COSIGN_PASSWORD=", "HOME="+workspace)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, stderr.String())
		}
		return out, nil
	}
	requireRun := func(args ...string) []byte {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("cosign %s: %v\n%s", args[0], err, out)
		}
		return out
	}
	for path, raw := range map[string]string{
		"sbom.json":          `{"bomFormat":"CycloneDX","specVersion":"1.5","version":1}`,
		"provenance.json":    `{"buildDefinition":{"buildType":"https://nvkr.dev/buildtypes/wrapped-build/v1","externalParameters":{}},"runDetails":{"builder":{"id":"local-fixture"}}}`,
		"qualification.json": `{"schemaVersion":"nvkr.dev/qualification-evidence/v1","gateVersion":"local-key-interop-fixture"}`,
		"trusted-root.json":  `{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[],"certificateAuthorities":[],"ctlogs":[],"timestampAuthorities":[]}`,
	} {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requireRun("generate-key-pair")
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	ref, err := name.NewTag(strings.TrimPrefix(server.URL, "http://") + "/artifact:test")
	if err != nil {
		t.Fatal(err)
	}
	var digest v1.Hash
	if withSBOM {
		if err := remote.Write(ref, empty.Image, remote.WithContext(ctx)); err != nil {
			t.Fatal(err)
		}
		digest, err = empty.Image.Digest()
	} else {
		index := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: empty.Image})
		if err := remote.WriteIndex(ref, index, remote.WithContext(ctx)); err != nil {
			t.Fatal(err)
		}
		digest, err = index.Digest()
	}
	if err != nil {
		t.Fatal(err)
	}
	subject := ref.Context().Digest(digest.String()).String()
	// Signing uses an ephemeral key without public services only in this fixture.
	signArgs := func(args []string) []string {
		return append(args[:len(args)-1], "--key", "cosign.key", "--use-signing-config=false", "--tlog-upload=false", args[len(args)-1])
	}
	if withSBOM {
		// Exercise NVKR's actual SBOM creation and verification adapter too. The
		// injected runner substitutes only local-key trust for external services.
		bom, err := sbom.NewSubject(ociTestDigest, "fixture", digest.String())
		if err != nil {
			t.Fatal(err)
		}
		document, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		adapter, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer,
			Runner: EnvRunnerFunc(func(_ context.Context, _ string, args, _ []string, stdout, _ io.Writer) error {
				if args[0] == "attest-blob" {
					args = signArgs(args)
				} else {
					args = append(args[:9], "--key", "cosign.pub", "--insecure-ignore-tlog", "--offline", "--trusted-root", "trusted-root.json")
				}
				out, err := run(args...)
				if err == nil {
					_, err = stdout.Write(out)
				}
				return err
			})})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.AttestSBOM(ctx, digest.String(), document); err != nil {
			t.Fatal(err)
		}
		requireRun(signArgs([]string{"attest", "--yes", "--new-bundle-format", "--type", "cyclonedx", "--predicate", "sbom.json", subject})...)
	}
	if _, err := run(ociDownloadBundleArgs(subject, CosignSignaturePredicateType)...); err == nil {
		t.Fatal("missing signature was accepted")
	}
	requireRun(signArgs(ociSignArgs(subject))...)
	if _, err := run(ociDownloadBundleArgs(subject, ProvenancePredicateType)...); err == nil {
		t.Fatal("missing provenance was accepted")
	}
	requireRun(signArgs(ociAttestArgs(subject, "provenance.json"))...)
	requireRun(signArgs(qualificationAttestArgs(subject, "qualification.json"))...)
	for _, predicate := range []string{CosignSignaturePredicateType, ProvenancePredicateType, QualificationPredicateType} {
		bundles, err := decodeOCIBundles(requireRun(ociDownloadBundleArgs(subject, predicate)...))
		if err != nil {
			t.Fatal(err)
		}
		if len(bundles) != 1 {
			t.Fatalf("expected one %s bundle, got %d", predicate, len(bundles))
		}
		var document struct {
			MediaType string `json:"mediaType"`
		}
		if err := json.Unmarshal(bundles[0], &document); err != nil {
			t.Fatal(err)
		}
		if document.MediaType != sbom.BundleMediaType {
			t.Fatalf("unexpected bundle format: %s", document.MediaType)
		}
		path := filepath.Join(workspace, "verify.json")
		if err := os.WriteFile(path, bundles[0], 0o600); err != nil {
			t.Fatal(err)
		}
		// These are keyed fixtures; production takes identity from a verified
		// embedded certificate and never admits a public-key bundle as keyless.
		args := ociVerifyBundleArgs(path, digest.String(), predicate, VerifyPolicy{IdentityRegexp: ociTestIdentity, Issuer: ociTestIssuer})
		args = args[:9]
		args = append(args, "--key", "cosign.pub", "--insecure-ignore-tlog", "--offline", "--trusted-root", "trusted-root.json")
		requireRun(args...)
		args[6] = strings.TrimPrefix(ociTestDigest, "sha256:")
		if _, err := run(args...); err == nil {
			t.Fatal("bundle accepted for a different digest")
		}
	}
	for _, suffix := range []string{"sig", "att"} {
		legacy := ref.Context().Tag("sha256-" + digest.Hex + "." + suffix)
		_, err := remote.Head(legacy, remote.WithContext(ctx))
		var registryErr *transport.Error
		if !errors.As(err, &registryErr) || registryErr.StatusCode != 404 {
			t.Fatalf("legacy attachment %s exists or lookup failed: %v", suffix, err)
		}
	}
}
