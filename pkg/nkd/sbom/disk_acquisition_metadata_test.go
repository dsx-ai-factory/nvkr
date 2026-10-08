// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
)

// The material owner authenticates these fixture bytes. Reader metadata is
// controlled separately to test the exact digest join, not real dpkg execution.
func TestCapturedPackageMetadataRequiresOwnExactBytes(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, scenario := range []string{"valid", "sibling metadata", "swapped digests", "missing observation", "different version", "different architecture", "wrong consumer"} {
			t.Run(architecture+"/"+scenario, func(t *testing.T) {
				manifest := material.AcquisitionManifest{SchemaVersion: material.AcquisitionSchemaVersion, Architecture: architecture, Scope: "apt-pre-install-pkgs"}
				payloads := make(map[string][]byte)
				observed := make(map[string]diskPackageKey)
				for _, name := range []string{"package-a", "package-b"} {
					payload := []byte(name + " controlled package bytes")
					digest := rawDigest(payload)
					path := "packages/" + strings.TrimPrefix(digest, "sha256:") + ".deb"
					payloads[path] = payload
					manifest.Packages = append(manifest.Packages, material.AcquiredPackage{Name: name, Version: "2:1.0-1ubuntu4", Architecture: architecture, Digest: digest,
						Size: int64(len(payload)), Path: path, RepositoryURIs: []string{"https://example.invalid/" + name + ".deb"}})
					observed[digest] = diskPackageKey{name, "2:1.0-1ubuntu4", "deb", architecture}
				}
				first, second := manifest.Packages[0], &manifest.Packages[1]
				platform := "linux/" + architecture
				switch scenario {
				case "sibling metadata":
					second.Name = first.Name // A exists elsewhere, but these are B's bytes.
				case "swapped digests":
					observed[first.Digest], observed[second.Digest] = observed[second.Digest], observed[first.Digest]
				case "missing observation":
					delete(observed, second.Digest)
				case "different version":
					second.Version = "1.0-1ubuntu4"
				case "different architecture":
					identity := observed[second.Digest]
					identity.architecture = "all"
					observed[second.Digest] = identity
				case "wrong consumer":
					platform = map[string]string{"amd64": "linux/arm64", "arm64": "linux/amd64"}[architecture]
				}
				slices.SortFunc(manifest.Packages, func(a, b material.AcquiredPackage) int { return strings.Compare(a.Digest, b.Digest) })
				raw := capturedPackageTestArchive(t, manifest, payloads)
				held, err := heldfile.FromBytesContext(t.Context(), raw, heldfile.Options{Label: "metadata-join", MaxBytes: int64(len(raw))})
				if err != nil {
					t.Fatal(err)
				}
				defer held.Close()
				capture, err := material.ObserveAcquisitionArchive(t.Context(), held)
				if err != nil {
					t.Fatal(err)
				}
				err = checkCapturedPackageMetadata(capture, observed, platform)
				want := e.CodeVerifyFailed
				if scenario == "valid" {
					want = ""
				}
				if sbomCode(err) != want {
					t.Fatalf("digest-bound metadata check = %v, want %s", err, want)
				}
			})
		}
	}
}

func capturedPackageTestArchive(t *testing.T, manifest material.AcquisitionManifest, payloads map[string][]byte) []byte {
	t.Helper()
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	write := func(name string, content []byte) {
		t.Helper()
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o444, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", raw)
	for _, entry := range manifest.Packages {
		write(entry.Path, payloads[entry.Path])
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
