// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func bootImagerPublicationFixture(t *testing.T) (string, string) {
	t.Helper()
	request := releasePublicationRequestFixture(t, "linux", "amd64")
	input := filepath.Join(t.TempDir(), "imager")
	if err := os.WriteFile(input, []byte("exact executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleRoot := filepath.Join(t.TempDir(), "bundle")
	bundle, err := BuildBinaryBundle(t.Context(), bundleRoot, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools",
		Boot:  &BinaryBootBinding{Member: "cli", Profile: "test-profile-v1"},
		Files: []BinaryFileInput{{Name: "cli", Path: input, InstallPath: "/efi/imager.efi", Mode: 0o644}}})
	if err != nil {
		t.Fatal(err)
	}
	request.Objects[0].Path, request.Objects[0].SemanticDigest, request.Objects[0].BlobDigest = bundleRoot, bundle.SemanticDigest, bundle.RootDigest
	descriptor, err := PayloadDescriptor(t.Context(), bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	request.Release.ContentSubject, err = ContentDigest([]Output{{Name: "tools", Kind: "executable", Digest: descriptor.Digest,
		BuildDigest: request.Objects[0].Output.BuildDigest, Components: []string{"cli"}}})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	return root, result.RootDigest
}

func TestBootAssetStreamsRawBytesAndRevokesCopies(t *testing.T) {
	for _, kind := range []string{"disk", "imager"} {
		t.Run(kind, func(t *testing.T) {
			var publication *Publication
			var asset *BootAsset
			var root, pin string
			var raw []byte
			var err error
			if kind == "disk" {
				request, _ := diskPublicationRequest(t, "amd64")
				raw, err = os.ReadFile(fixtureBlobPath(request.Objects[0].Path, request.Objects[0].Output.Disk.PayloadDigest))
				if err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(t.TempDir(), "publication")
				result, err := BuildPublicationRoot(t.Context(), root, request)
				if err != nil {
					t.Fatal(err)
				}
				pin = result.RootDigest
				publication, err = openDiskPublicationFixture(t.Context(), root, pin)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				// Synthetic metadata/raw-byte fixture; no PE or compatibility claim.
				root, pin = bootImagerPublicationFixture(t)
				raw = []byte("exact executable")
				publication, err = OpenPublication(t.Context(), root, pin)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = publication.Close() })
			reference := "registry.example.test/releases-output@" + pin
			if kind == "disk" {
				asset, err = publication.OpenBootDisk(t.Context(), reference, definition.BootDisk{Output: "node-image", Platform: "linux/amd64"})
			} else {
				asset, err = publication.OpenBootImager(t.Context(), reference, definition.BootImager{Output: "tools", Member: "cli", Platform: "linux/amd64", Profile: "test-profile-v1"})
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = asset.Close() })
			shared := *asset
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if kind == "imager" {
				if err := publication.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var borrowed io.ReadSeeker
			visits := 0
			err = shared.Visit(t.Context(), func(_ context.Context, binding BootAssetBinding, source io.ReadSeeker) error {
				visits++
				borrowed = source
				got, err := io.ReadAll(source)
				if err != nil || !bytes.Equal(got, raw) || binding.SHA256 != digestBytes(raw) || binding.Size != int64(len(raw)) || binding.Publication != reference {
					t.Fatalf("raw asset differs: %+v, %v", binding, err)
				}
				return nil
			})
			if err != nil || visits != 1 {
				t.Fatalf("raw visits = %d: %v", visits, err)
			}
			_, err = borrowed.Read(make([]byte, 1))
			assertRegistryCode(t, err, e.CodeGateUnmet)
			if kind == "disk" {
				if err := publication.Close(); err != nil {
					t.Fatal(err)
				}
				_, err = shared.Binding()
				assertRegistryCode(t, err, e.CodeGateUnmet)
			}
			if err := asset.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = shared.Binding()
			assertRegistryCode(t, err, e.CodeGateUnmet)
			err = shared.Visit(t.Context(), func(context.Context, BootAssetBinding, io.ReadSeeker) error { return nil })
			assertRegistryCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestBootAssetRejectsUnknownOriginalMemberAndProfile(t *testing.T) {
	root, pin := bootImagerPublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, pin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publication.Close() })
	for _, tc := range []struct {
		name string
		edit func(*definition.BootImager)
	}{
		{"sibling", func(i *definition.BootImager) { i.Output = "other" }},
		{"member", func(i *definition.BootImager) { i.Member = "other" }},
		{"profile", func(i *definition.BootImager) { i.Profile = "test-profile-v2" }},
		{"platform", func(i *definition.BootImager) { i.Platform = "linux/arm64" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := definition.BootImager{Output: "tools", Member: "cli", Platform: "linux/amd64", Profile: "test-profile-v1"}
			tc.edit(&expected)
			asset, err := publication.OpenBootImager(t.Context(), "registry.example.test/releases-output@"+pin, expected)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if asset != nil {
				t.Fatal("mismatched original binding issued raw asset")
			}
		})
	}
}
