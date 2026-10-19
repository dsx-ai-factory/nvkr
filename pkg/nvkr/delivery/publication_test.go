// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func releasePublicationFixture(t *testing.T) (string, string) {
	t.Helper()
	return releasePublicationPlatformFixture(t, "", "")
}

func TestPublicationOutputsRemainDetachedAndLocationFree(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publication.Close() })
	outputs, err := publication.Outputs()
	if err != nil || len(outputs) != 1 || outputs[0].Reference != "" {
		t.Fatalf("unexpected local output descriptions: %+v, %v", outputs, err)
	}
	outputs[0].Name, outputs[0].Components[0] = "changed", "changed"
	again, err := publication.Outputs()
	if err != nil || again[0].Name != "tools" || again[0].Components[0] != "cli" {
		t.Fatalf("mutated descriptions affected held state: %+v, %v", again, err)
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	for _, closed := range []*Publication{nil, {}, publication} {
		_, err := closed.Outputs()
		assertRegistryCode(t, err, e.CodeGateUnmet)
	}
}

func releasePublicationPlatformFixture(t *testing.T, osName, arch string) (string, string) {
	t.Helper()
	request := releasePublicationRequestFixture(t, osName, arch)
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	return root, result.RootDigest
}

func releasePublicationRequestFixture(t *testing.T, osName, arch string) PublicationRequest {
	t.Helper()
	source := filepath.Join(t.TempDir(), "cli")
	payload := []byte("exact executable")
	if err := os.WriteFile(source, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "bundle")
	bundle, err := BuildBinaryBundle(t.Context(), bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools", Files: []BinaryFileInput{{Name: "cli", Path: source, InstallPath: "/usr/local/bin/cli", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := PayloadDescriptor(t.Context(), bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	build := digestBytes([]byte("build"))
	output := Output{Name: "tools", Kind: "executable", Digest: descriptor.Digest, BuildDigest: build, Components: []string{"cli"}}
	content, err := ContentDigest([]Output{output})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(build, "cli", digestBytes(payload))
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, digestBytes(payload), "build-output", digestBytes(payload))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return PublicationRequest{Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build,
		PlanDigest: digestBytes([]byte("plan")), ContentSubject: content}, Objects: []ObjectInput{
		{Name: "tools", Kind: "binary-bundle", Path: bundlePath, Layout: true, ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest, OS: osName, Arch: arch, Output: &OutputBinding{Repository: "tools", Format: "executable", BuildDigest: build, Components: []string{"cli"}, BuildSubjects: []BuildSubject{{Component: "cli", Digest: digestBytes(payload)}}}},
		{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw, Evidence: &EvidenceBinding{Outputs: []string{"tools"}, Component: "cli", Stage: "build-output", SubjectKind: "file", SubjectDigest: digestBytes(payload)}},
	}}
}

func TestPublicationOwnsHeldClosureLifetime(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	held := append([]*heldfile.File(nil), publication.files...)
	if len(held) < 2 {
		t.Fatal("publication has no held closure")
	}
	reference := "registry.example.test/release@" + digest
	if _, _, err := publication.Artifacts(reference); err != nil {
		t.Fatal(err)
	}
	assertRegistryCode(t, publication.RequireAttestedEvidence(), e.CodeProvenanceMissing)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 20 {
				_, _, err := publication.Artifacts(reference)
				if err != nil {
					assertRegistryCode(t, err, e.CodeGateUnmet)
				}
			}
		})
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	for _, file := range held {
		if file.OSFile() != nil {
			t.Fatal("Close leaked anonymous bytes")
		}
	}
	_, _, err = publication.Artifacts(reference)
	assertRegistryCode(t, err, e.CodeGateUnmet)
}

func TestPublicationRootRequiresAndBindsPlanIdentity(t *testing.T) {
	for _, planDigest := range []string{"", "sha256:bad"} {
		t.Run(planDigest, func(t *testing.T) {
			request := releasePublicationRequestFixture(t, "", "")
			request.Release.PlanDigest = planDigest
			_, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "publication"), request)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
	firstRequest := releasePublicationRequestFixture(t, "", "")
	first, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "first"), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := releasePublicationRequestFixture(t, "", "")
	secondRequest.Release.PlanDigest = digestBytes([]byte("other plan"))
	second, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "second"), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if first.RootDigest == second.RootDigest {
		t.Fatal("changing the Plan identity did not change the publication root identity")
	}
}

func TestPublicationRejectsCorruptionAndUnreachableBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{"replaced index", func(t *testing.T, root, _ string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("other"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced nested config", func(t *testing.T, root, _ string) {
			t.Helper()
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range inspection.Root.Objects {
				if object.Kind != "binary-bundle" {
					continue
				}
				manifest, err := flatManifest(t.Context(), root, object)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fixtureBlobPath(root, manifest.Config.Digest), []byte("other"), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			t.Fatal("fixture lacks a nested binary bundle")
		}},
		{"unreachable blob", func(t *testing.T, root, _ string) {
			t.Helper()
			payload := []byte("extra")
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digestBytes(payload), "sha256:")), payload, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink blob", func(t *testing.T, root, _ string) {
			t.Helper()
			if err := os.Symlink(filepath.Join(root, "index.json"), filepath.Join(root, "blobs", "sha256", strings.Repeat("f", 64))); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, digest := releasePublicationFixture(t)
			tc.mutate(t, root, digest)
			_, err := Inspect(t.Context(), root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			publication, err := OpenPublication(t.Context(), root, digest)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if publication != nil {
				t.Fatal("rejected closure returned capability")
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := OpenPublication(canceled, "missing", digestBytes(nil))
	assertRegistryCode(t, err, e.CodeTimeout)
	_, err = OpenPublication(t.Context(), "missing", "latest")
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}
