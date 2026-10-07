// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestBinaryBundleDescriptionRejectsAmbiguousIdentity(t *testing.T) {
	digest := digestBytes([]byte("fixture"))
	for _, tc := range []struct {
		name   string
		change func(*BinaryBundle)
		code   e.Code
	}{
		{"schema", func(b *BinaryBundle) { b.SchemaVersion = "future" }, e.CodeVerifyFailed},
		{"empty purpose", func(b *BinaryBundle) { b.Purpose = "" }, e.CodeInvalidCatalog},
		{"incomplete release binding", func(b *BinaryBundle) { b.ReleaseSetDigest = digest }, e.CodeUnverifiedRef},
		{"invalid release binding", func(b *BinaryBundle) { b.ReleaseSetDigest = digest; b.InventoryDigest = "tag" }, e.CodeUnverifiedRef},
		{"no files", func(b *BinaryBundle) { b.Entries = nil }, e.CodeReleaseIncomplete},
		{"duplicate name", func(b *BinaryBundle) { b.Entries = append(b.Entries, b.Entries[0]) }, e.CodeVerifyFailed},
		{"duplicate install path", func(b *BinaryBundle) { entry := b.Entries[0]; entry.Name = "z"; b.Entries = append(b.Entries, entry) }, e.CodeVerifyFailed},
		{"escaping install path", func(b *BinaryBundle) { b.Entries[0].InstallPath = "/bin/../tool" }, e.CodeVerifyFailed},
		{"noncanonical install path", func(b *BinaryBundle) { b.Entries[0].InstallPath = "/bin//tool" }, e.CodeVerifyFailed},
		{"relative install path", func(b *BinaryBundle) { b.Entries[0].InstallPath = "bin/tool" }, e.CodeVerifyFailed},
		{"setuid mode", func(b *BinaryBundle) { b.Entries[0].Mode = 0o4755 }, e.CodeVerifyFailed},
		{"negative size", func(b *BinaryBundle) { b.Entries[0].Size = -1 }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := BinaryBundle{SchemaVersion: BinaryBundleSchemaVersion, OS: "linux", Arch: "amd64", Purpose: "tools", Entries: []BinaryEntry{{Name: "tool", InstallPath: "/usr/bin/tool", Mode: 0o755, SHA256: digest, Size: 7}}}
			if err := bundle.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.change(&bundle)
			assertRegistryCode(t, bundle.Validate(), tc.code)
			_, err := bundle.Digest()
			assertRegistryCode(t, err, tc.code)
		})
	}
}

func TestPublicationDescriptionCannotSmuggleAuthorityOrAmbiguousObjects(t *testing.T) {
	digest := digestBytes([]byte("fixture"))
	for _, tc := range []struct {
		name   string
		change func(*PublicationRoot)
		code   e.Code
	}{
		{"schema", func(r *PublicationRoot) { r.SchemaVersion = "future" }, e.CodeVerifyFailed},
		{"unknown qualification", func(r *PublicationRoot) { r.Qualification = "trusted" }, e.CodeVerifyFailed},
		{"diagnostic clearance", func(r *PublicationRoot) { r.ClearanceDigest = digest }, e.CodeGateUnmet},
		{"diagnostic publication authority", func(r *PublicationRoot) { r.PublicationIdentity = digest }, e.CodeGateUnmet},
		{"incomplete qualified description", func(r *PublicationRoot) { r.Qualification = QualificationQualified }, e.CodeGateUnmet},
		{"incomplete release binding", func(r *PublicationRoot) { r.ReleaseSetDigest = digest }, e.CodeUnverifiedRef},
		{"empty objects", func(r *PublicationRoot) { r.Objects = nil }, e.CodeReleaseIncomplete},
		{"duplicate descriptor", func(r *PublicationRoot) { o := r.Objects[0]; o.Name = "z"; r.Objects = append(r.Objects, o) }, e.CodeVerifyFailed},
		{"invalid media type", func(r *PublicationRoot) { r.Objects[0].ArtifactType = "application/bad;parameter=1" }, e.CodeVerifyFailed},
		{"incomplete platform", func(r *PublicationRoot) { r.Objects[0].OS = "linux" }, e.CodeVerifyFailed},
		{"noncanonical platform", func(r *PublicationRoot) { r.Objects[0].OS = "linux"; r.Objects[0].Arch = " amd64" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := PublicationRoot{SchemaVersion: PublicationRootSchema, Qualification: QualificationDiagnostic, Objects: []PublishedObject{{Name: "record", Kind: "record", ArtifactType: MediaDeliveryObject, DescriptorDigest: digest, SemanticDigest: digest, BlobDigest: digest, Size: 7}}}
			if err := root.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.change(&root)
			assertRegistryCode(t, root.Validate(), tc.code)
			_, err := root.Digest()
			assertRegistryCode(t, err, tc.code)
		})
	}
}
