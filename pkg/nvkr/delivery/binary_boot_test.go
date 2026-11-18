// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func bootBundleDescription() BinaryBundle {
	return BinaryBundle{SchemaVersion: BinaryBootSchemaVersion, OS: "linux", Arch: "amd64", Purpose: "imager-amd64",
		Boot:    &BinaryBootBinding{Member: "qcow-imager.efi", Profile: "test-profile-v1"},
		Entries: []BinaryEntry{{Name: "qcow-imager.efi", InstallPath: "/efi/qcow-imager.efi", Mode: 0o644, SHA256: digestBytes([]byte("synthetic bytes")), Size: 15}}}
}

func TestBinaryBootOriginalBinding(t *testing.T) {
	expected := definition.BootImager{Output: "imager-amd64", Member: "qcow-imager.efi", Platform: "linux/amd64", Profile: "test-profile-v1"}
	for _, tc := range []struct {
		name string
		edit func(*BinaryBundle)
		code e.Code
	}{
		{"exact", func(*BinaryBundle) {}, ""},
		{"old bundle", func(b *BinaryBundle) { b.SchemaVersion, b.Boot = BinaryBundleSchemaVersion, nil }, e.CodeEngineCapability},
		{"old schema with claim", func(b *BinaryBundle) { b.SchemaVersion = BinaryBundleSchemaVersion }, e.CodeVerifyFailed},
		{"new schema without claim", func(b *BinaryBundle) { b.Boot = nil }, e.CodeVerifyFailed},
		{"same bytes sibling output", func(b *BinaryBundle) { b.Purpose = "other-imager" }, e.CodeVerifyFailed},
		{"wrong architecture", func(b *BinaryBundle) { b.Arch = "arm64" }, e.CodeVerifyFailed},
		{"unsupported architecture", func(b *BinaryBundle) { b.Arch = "riscv64" }, e.CodeInvalidCatalog},
		{"wrong profile", func(b *BinaryBundle) { b.Boot.Profile = "test-profile-v2" }, e.CodeVerifyFailed},
		{"missing profile", func(b *BinaryBundle) { b.Boot.Profile = "" }, e.CodeInvalidCatalog},
		{"missing member", func(b *BinaryBundle) { b.Boot.Member = "missing.efi" }, e.CodeVerifyFailed},
		{"same bytes sibling member", func(b *BinaryBundle) { b.Boot.Member, b.Entries[0].Name = "other.efi", "other.efi" }, e.CodeVerifyFailed},
		{"ambiguous member", func(b *BinaryBundle) { b.Entries = append(b.Entries, b.Entries[0]) }, e.CodeVerifyFailed},
		{"empty payload", func(b *BinaryBundle) { b.Entries[0].Size = 0 }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := bootBundleDescription()
			tc.edit(&bundle)
			entry, err := bundle.BootMember(expected)
			if tc.code != "" {
				assertRegistryCode(t, err, tc.code)
			} else if err != nil || entry != bundle.Entries[0] {
				t.Fatalf("bound member = %+v, %v", entry, err)
			}
		})
	}
}

func TestBinaryBootConfigBindsProfileWithoutChangingRawBytes(t *testing.T) {
	// These bytes test signed metadata construction, not PE inspection or boot.
	raw := []byte("synthetic imager bytes")
	input := filepath.Join(t.TempDir(), "imager")
	if err := os.WriteFile(input, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var previous Result
	for _, profile := range []string{"test-profile-v1", "test-profile-v2"} {
		out := filepath.Join(t.TempDir(), "bundle")
		binding := &BinaryBootBinding{Member: "qcow-imager.efi", Profile: profile}
		result, err := BuildBinaryBundle(t.Context(), out, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "imager-amd64", Boot: binding,
			Files: []BinaryFileInput{{Name: binding.Member, Path: input, InstallPath: "/efi/imager.efi", Mode: 0o644}}})
		if err != nil {
			t.Fatal(err)
		}
		binding.Profile = "caller-mutation"
		bundle, checked, err := InspectBinaryBundle(t.Context(), out)
		if err != nil || checked.RootDigest != result.RootDigest || bundle.Boot == nil || bundle.Boot.Profile != profile {
			t.Fatalf("original profile binding = %+v, %v", bundle, err)
		}
		entry, err := bundle.BootMember(definition.BootImager{Output: "imager-amd64", Member: "qcow-imager.efi", Platform: "linux/amd64", Profile: profile})
		if err != nil || entry.SHA256 != digestBytes(raw) || entry.Size != int64(len(raw)) {
			t.Fatalf("raw member changed = %+v, %v", entry, err)
		}
		if previous.RootDigest != "" && (result.RootDigest == previous.RootDigest || result.SemanticDigest == previous.SemanticDigest) {
			t.Fatal("changed original profile did not change artifact identity")
		}
		previous = result
	}
}

func TestBinaryBootAdmissionPrecedesFileAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		boot *BinaryBootBinding
	}{
		{"missing profile", &BinaryBootBinding{Member: "imager"}},
		{"unknown member", &BinaryBootBinding{Member: "absent", Profile: "test-profile-v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "bundle")
			_, err := BuildBinaryBundle(t.Context(), out, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "imager", Boot: tc.boot,
				Files: []BinaryFileInput{{Name: "imager", Path: "/does-not-exist/never-open", InstallPath: "/efi/imager"}}})
			assertRegistryCode(t, err, e.CodeInvalidCatalog)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("failed admission created output: %v", err)
			}
		})
	}
}

func TestBinaryBundleV1CanonicalBytesUnchanged(t *testing.T) {
	bundle := BinaryBundle{SchemaVersion: BinaryBundleSchemaVersion, OS: "linux", Arch: "amd64", Purpose: "tools",
		Entries: []BinaryEntry{{Name: "member", InstallPath: "/test/member", Mode: 0o644, SHA256: "sha256:" + strings.Repeat("a", 64), Size: 7}}}
	raw, err := canonical.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"arch":"amd64","entries":[{"installPath":"/test/member","mode":420,"name":"member","sha256":"sha256:` + strings.Repeat("a", 64) + `","size":7}],"os":"linux","purpose":"tools","schemaVersion":"nvkr.dev/binary-bundle/v1"}`
	if string(raw) != want {
		t.Fatalf("legacy canonical config changed: %s", raw)
	}
	var decoded BinaryBundle
	withNull := strings.Replace(want, `"entries":`, `"boot":null,"entries":`, 1)
	assertRegistryCode(t, canonical.UnmarshalExact([]byte(withNull), &decoded), e.CodeVerifyFailed)
}

func TestBinaryBootV2CanonicalBytes(t *testing.T) {
	bundle := bootBundleDescription()
	raw, err := canonical.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"arch":"amd64","boot":{"member":"qcow-imager.efi","profile":"test-profile-v1"},"entries":[{"installPath":"/efi/qcow-imager.efi","mode":420,"name":"qcow-imager.efi","sha256":"` + digestBytes([]byte("synthetic bytes")) + `","size":15}],"os":"linux","purpose":"imager-amd64","schemaVersion":"nvkr.dev/binary-bundle/v2"}`
	if string(raw) != want {
		t.Fatal("v2 original member/profile wire bytes changed")
	}
}
