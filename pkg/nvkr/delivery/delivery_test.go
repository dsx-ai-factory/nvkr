// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
)

func TestPublicationInspectionRejectsTamperedNestedBlob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "tool")
	if err := os.WriteFile(binary, []byte("exact"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := BuildBinaryBundle(ctx, bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools", Files: []BinaryFileInput{{Name: "tool", Path: binary, InstallPath: "/usr/bin/tool", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(root, "publication")
	_, err = BuildPublicationRoot(ctx, publicationPath, PublicationRequest{Objects: []ObjectInput{{Name: "tools", Kind: "binary-bundle", Path: bundlePath, ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, Layout: true}}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(publicationPath, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join(publicationPath, "blobs", "sha256", entry.Name()))
		if readErr == nil && strings.Contains(string(raw), "binary-bundle/v1") {
			target = entry.Name()
			break
		}
	}
	if target == "" {
		t.Fatal("did not find binary bundle config blob")
	}
	if err := os.WriteFile(filepath.Join(publicationPath, "blobs", "sha256", target), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, publicationPath); err == nil {
		t.Fatal("tampered recursive blob passed inspection")
	}
}

func TestPublicationInspectionRejectsUnreachableBlob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "tool")
	if err := os.WriteFile(binary, []byte("exact"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := BuildBinaryBundle(ctx, bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools", Files: []BinaryFileInput{{Name: "tool", Path: binary, InstallPath: "/usr/bin/tool", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(root, "publication")
	_, err = BuildPublicationRoot(ctx, publicationPath, PublicationRequest{Objects: []ObjectInput{{Name: "tools", Kind: "binary-bundle", Path: bundlePath, ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, Layout: true}}})
	if err != nil {
		t.Fatal(err)
	}
	extra := strings.Repeat("0", 64)
	if err := os.WriteFile(filepath.Join(publicationPath, "blobs", "sha256", extra), []byte("smuggled"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, publicationPath); err == nil {
		t.Fatal("unreachable OCI blob passed exact-closure inspection")
	}
}

func TestAPTRepositoryProjectsExactDebianPackage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "crictl")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(root, "crictl_1.34.0_amd64.deb")
	if _, err := assemble.Deb(binary, "crictl", "1.34.0", 1_748_736_000, "amd64", deb); err != nil { //nolint:staticcheck // creates an explicitly unqualified APT-projection fixture
		t.Fatal(err)
	}
	out := filepath.Join(root, "apt")
	result, err := BuildAPTRepository(ctx, out, APTRequest{Distribution: "stable", Component: "main", Epoch: 1_748_736_000, Packages: []DebianPackageInput{{Path: deb}}})
	if err != nil {
		t.Fatal(err)
	}
	if !isDigest(result.Digest) || len(result.Repository.Packages) != 1 || result.Repository.Packages[0].Name != "crictl" {
		t.Fatalf("unexpected APT result: %#v", result)
	}
	for _, path := range []string{
		"dists/stable/Release", "dists/stable/main/binary-amd64/Packages",
		"dists/stable/main/binary-amd64/Packages.gz", "nvkr-apt-repository.json",
		result.Repository.Packages[0].Filename,
	} {
		if info, err := os.Stat(filepath.Join(out, filepath.FromSlash(path))); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("missing APT file %s: %v", path, err)
		}
	}
	packages, err := os.ReadFile(filepath.Join(out, "dists/stable/main/binary-amd64/Packages"))
	if err != nil || !strings.Contains(string(packages), "SHA256: "+strings.TrimPrefix(result.Repository.Packages[0].SHA256, "sha256:")) {
		t.Fatalf("Packages did not bind exact deb: %v\n%s", err, packages)
	}
}

func TestRegistryOptionsRejectPartialOrConflictingCredentialsBeforeLayoutIO(t *testing.T) {
	t.Parallel()
	for name, options := range map[string]RegistryOptions{
		"password-only":  {Password: "secret"},
		"anonymous-auth": {Anonymous: true, Username: "user", Password: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Push(context.Background(), filepath.Join(t.TempDir(), "missing"),
				"registry.example/nvkr:test", options); err == nil ||
				(!strings.Contains(err.Error(), "credential") && !strings.Contains(err.Error(), "authentication")) {
				t.Fatalf("unsafe registry options did not fail at admission: %v", err)
			}
		})
	}
}
