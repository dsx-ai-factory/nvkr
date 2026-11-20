// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func packageInventoryFixture(arch string) *DiskInstalledInventory {
	return &DiskInstalledInventory{subjectDigest: "sha256:" + strings.Repeat("a", 64), subjectSize: 1024,
		runtimeDigest: "sha256:" + strings.Repeat("b", 64), inspectorDigest: "sha256:" + strings.Repeat("c", 64),
		codecDigest: "sha256:" + strings.Repeat("d", 64), packages: []InstalledDiskPackage{
			{Name: "zlib1g", Version: "1.2.13", Release: "0ubuntu1-2", Epoch: "1", Architecture: arch, Format: "deb"},
			{Name: "base-files", Version: "12ubuntu4", Epoch: "0", Architecture: "all", Format: "deb"},
		}}
}

func TestDiskPackageDocument(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			inventory := packageInventoryFixture(arch)
			build := "sha256:" + strings.Repeat("e", 64)
			bom, err := DiskPackageDocument(build, "node", inventory)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateDiskInstalledPackages(bom); err != nil {
				t.Fatal(err)
			}
			if len(bom.Components) != 2 || bom.Metadata.Component.Hashes[0].Content != strings.Repeat("a", 64) {
				t.Fatal("inventory lost its subject or components")
			}
			if got := bom.Components[1]; got.Name != "zlib1g" || got.Version != "1:1.2.13-0ubuntu1-2" || got.PURL != "pkg:deb/zlib1g@1%3A1.2.13-0ubuntu1-2?arch="+arch {
				t.Fatalf("package identity lost: %+v", got)
			}
			first, err := bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			slices.Reverse(inventory.packages)
			repeated, err := DiskPackageDocument(build, "node", inventory)
			if err != nil {
				t.Fatal(err)
			}
			second, err := repeated.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Fatal("package traversal order changed evidence")
			}
			inventory.subjectDigest = "sha256:" + strings.Repeat("f", 64)
			other, err := DiskPackageDocument(build, "node", inventory)
			if err != nil {
				t.Fatal(err)
			}
			left, _ := bom.Digest()
			right, _ := other.Digest()
			if left == right {
				t.Fatal("different disk reused evidence identity")
			}
		})
	}
}

func TestDiskPackageDocumentRejectsIncompleteObservation(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*DiskInstalledInventory)
		code e.Code
	}{
		{"unissued runtime", func(i *DiskInstalledInventory) { i.runtimeDigest = "" }, e.CodeProvenanceMissing},
		{"missing inspector", func(i *DiskInstalledInventory) { i.inspectorDigest = "" }, e.CodeProvenanceMissing},
		{"empty packages", func(i *DiskInstalledInventory) { i.packages = nil }, e.CodeProvenanceMissing},
		{"missing version", func(i *DiskInstalledInventory) { i.packages[0].Version = "" }, e.CodeProvenanceMissing},
		{"missing architecture", func(i *DiskInstalledInventory) { i.packages[0].Architecture = "" }, e.CodeProvenanceMissing},
		{"invalid epoch", func(i *DiskInstalledInventory) { i.packages[0].Epoch = "-1" }, e.CodeVerifyFailed},
		{"conflicting package", func(i *DiskInstalledInventory) {
			p := i.packages[0]
			p.Version = "other"
			i.packages = append(i.packages, p)
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := packageInventoryFixture("arm64")
			test.edit(inventory)
			bom, err := DiskPackageDocument("sha256:"+strings.Repeat("e", 64), "node", inventory)
			if bom != nil || sbomCode(err) != test.code {
				t.Fatalf("bom=%v err=%v, want %s", bom, err, test.code)
			}
		})
	}
}

func TestDiskPackageDocumentRejectsSignedClaimSubstitution(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*BOM)
		code e.Code
	}{
		{"wrong purl", func(b *BOM) { b.Components[0].PURL = "pkg:deb/other@1?arch=all" }, e.CodeVerifyFailed},
		{"missing architecture", func(b *BOM) { b.Components[0].Properties = b.Components[0].Properties[1:] }, e.CodeVerifyFailed},
		{"file claim", func(b *BOM) { b.Components[0].Type = "file" }, e.CodeVerifyFailed},
		{"strict claim", func(b *BOM) {
			for i := range b.Metadata.Props {
				if b.Metadata.Props[i].Name == "nvkr:dependencyInspection" {
					b.Metadata.Props[i].Value = "disk-reconciled/v1"
				}
			}
		}, e.CodeProvenanceMissing},
		{"missing codec", func(b *BOM) {
			b.Metadata.Props = slices.DeleteFunc(b.Metadata.Props, func(p Property) bool { return p.Name == "nvkr:diskCodecDigest" })
		}, e.CodeProvenanceMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			bom, err := DiskPackageDocument("sha256:"+strings.Repeat("e", 64), "node", packageInventoryFixture("amd64"))
			if err != nil {
				t.Fatal(err)
			}
			test.edit(bom)
			if err := ValidateDiskInstalledPackages(bom); sbomCode(err) != test.code {
				t.Fatalf("err=%v, want %s", err, test.code)
			}
		})
	}
}
