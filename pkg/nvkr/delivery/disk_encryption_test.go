// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDiskTransportBindsEncryptionDeclaration(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			payload := []byte("opaque transport bytes do not prove guest encryption")
			path := filepath.Join(t.TempDir(), "payload")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			manifests := map[string]bool{}
			for _, encryption := range []string{"none", "luks2"} {
				root := filepath.Join(t.TempDir(), "wrapper")
				_, err := BuildDiskArtifact(t.Context(), root, DiskArtifactRequest{Path: path, Digest: digestBytes(payload),
					Size: int64(len(payload)), Platform: platform, Format: "qcow2", Encryption: encryption, ArtifactType: MediaDiskImage})
				if err != nil {
					t.Fatal(err)
				}
				held, err := OpenSingleDiskArtifact(t.Context(), root, platform, int64(len(payload)))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = held.Close() })
				if err := held.RequireStandardProfile(t.Context()); err != nil {
					t.Fatal(err)
				}
				observed := held.Inspection()
				if observed.Encryption != encryption || observed.PayloadDigest != digestBytes(payload) || observed.Size != int64(len(payload)) || manifests[observed.ManifestDigest] {
					t.Fatalf("wrapper lost encryption or exact payload binding: %+v", observed)
				}
				manifests[observed.ManifestDigest] = true
			}
		})
	}
}

func TestDiskPublicationEncryptionAdmissionPrecedesIO(t *testing.T) {
	for _, encryption := range []string{"", "luks", "luks2"} {
		t.Run(encryption, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			request.Objects[0].Output.Disk.Encryption = encryption
			request.Objects[0].Path = filepath.Join(t.TempDir(), "absent-source")
			parent := t.TempDir()
			_, err := BuildPublicationRoot(t.Context(), filepath.Join(parent, "output"), request)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unsupported publication left output: %v, %v", entries, err)
			}
		})
	}
}
