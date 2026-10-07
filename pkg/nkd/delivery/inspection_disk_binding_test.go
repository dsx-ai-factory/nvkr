// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDiskBindingUsesOnlyCurrentDescriptorWalk(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			root, payload, manifest := diskFixture(t, "linux/"+arch)
			index, _, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			descriptor := index.Manifests[0]
			disks := map[string]DiskArtifactInspection{}
			visited := map[string]bool{}
			if err := verifyDescriptorForInspection(t.Context(), root, descriptor, visited, disks); err != nil {
				t.Fatal(err)
			}
			if len(disks) != 1 || disks[manifest].PayloadDigest != digestBytes(payload) || disks[manifest].Size != int64(len(payload)) {
				t.Fatalf("descriptor walk lost its exact disk observation: %+v", disks)
			}
			object := PublishedObject{Name: "disk", Kind: "disk-image", ArtifactType: testDiskType,
				DescriptorDigest: manifest, BlobDigest: digestBytes(payload), OS: "linux", Arch: arch}
			if err := verifyPublishedObjectBinding(t.Context(), root, object, descriptor, disks); err != nil {
				t.Fatal(err)
			}
			assertRegistryCode(t, verifyPublishedObjectBinding(t.Context(), root, object, descriptor, nil), e.CodeVerifyFailed)
			object.Arch = "other"
			assertRegistryCode(t, verifyPublishedObjectBinding(t.Context(), root, object, descriptor, disks), e.CodeVerifyFailed)
			// Reuse is scoped to one walk; an independent verifier must hash again.
			if err := os.WriteFile(diskBlobPath(root, digestBytes(payload)), []byte("changed payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			assertRegistryCode(t, verifyDescriptor(t.Context(), root, descriptor, map[string]bool{}), e.CodeVerifyFailed)
		})
	}
}

func TestOCIReadBoundariesRejectDuplicateJSONNames(t *testing.T) {
	root := t.TempDir()
	index := []byte(`{"schemaVersion":2,"schemaVersion":2,"mediaType":"` + MediaOCIIndex + `","manifests":[]}`)
	if err := os.WriteFile(filepath.Join(root, "index.json"), index, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := readIndex(t.Context(), root)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	manifest := []byte(`{"schemaVersion":2,"schemaVersion":2,"mediaType":"` + MediaOCIManifest + `","artifactType":"application/example","config":{},"layers":[]}`)
	descriptor := Descriptor{Digest: digestBytes(manifest), Size: int64(len(manifest)), MediaType: MediaOCIManifest}
	path := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(descriptor.Digest, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	assertRegistryCode(t, verifyDescriptor(t.Context(), root, descriptor, map[string]bool{}), e.CodeVerifyFailed)
}
