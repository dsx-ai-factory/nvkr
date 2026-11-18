// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSelectedBootMetadataBindsRawFileWithoutPayload(t *testing.T) {
	for _, kind := range []string{"disk", "imager"} {
		t.Run(kind, func(t *testing.T) {
			var root, digest, name string
			var publication *Publication
			var err error
			disk := definition.BootDisk{Output: "node-image", Platform: "linux/amd64"}
			imager := definition.BootImager{Output: "tools", Member: "cli", Platform: "linux/amd64", Profile: "test-profile-v1"}
			if kind == "disk" {
				request, _ := diskPublicationRequest(t, "amd64")
				root = filepath.Join(t.TempDir(), "publication")
				result, err := BuildPublicationRoot(t.Context(), root, request)
				if err != nil {
					t.Fatal(err)
				}
				digest, name = result.RootDigest, disk.Output
				publication, err = openDiskPublicationFixture(t.Context(), root, digest)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				root, digest = bootImagerPublicationFixture(t)
				name = imager.Output
				publication, err = OpenPublication(t.Context(), root, digest)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = publication.Close() })
			reference := "registry.example.test/selected@" + digest
			var asset *BootAsset
			if kind == "disk" {
				asset, err = publication.OpenBootDisk(t.Context(), reference, disk)
			} else {
				asset, err = publication.OpenBootImager(t.Context(), reference, imager)
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := asset.Binding()
			if err != nil {
				t.Fatal(err)
			}
			if err := asset.Close(); err != nil {
				t.Fatal(err)
			}
			sparse := filepath.Join(t.TempDir(), "sparse")
			if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), root, sparse, digest, []string{name}); err != nil {
				t.Fatal(err)
			}
			owner, err := OpenSelectedMetadata(t.Context(), sparse, digest, []string{name}, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			if err := publication.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(sparse); err != nil {
				t.Fatal(err)
			}
			var got BootAssetBinding
			binding := func() (BootAssetBinding, error) {
				if kind == "disk" {
					return owner.BootDiskBinding(reference, disk)
				}
				return owner.BootImagerBinding(reference, imager)
			}
			got, err = binding()
			if err != nil || got != want {
				t.Fatalf("retained binding = %+v, want %+v: %v", got, want, err)
			}
			if kind == "disk" {
				disk.Output = "sibling"
			} else {
				imager.Profile = "wrong-profile"
			}
			_, err = binding()
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = binding()
			assertRegistryCode(t, err, e.CodeGateUnmet)
		})
	}
}
