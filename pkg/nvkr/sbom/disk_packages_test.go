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
			{Name: "zlib1g", Version: "1.2.13", Release: "0ubuntu1-2+b1", Epoch: "1", Architecture: arch, Format: "deb", Vendor: "ubuntu"},
			{Name: "base-files", Version: "12ubuntu4", Epoch: "0", Architecture: "all", Format: "deb", Vendor: "ubuntu"},
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
			if got := bom.Components[1]; got.Name != "zlib1g" || got.Version != "1:1.2.13-0ubuntu1-2+b1" || got.PURL != "pkg:deb/ubuntu/zlib1g@1:1.2.13-0ubuntu1-2%2Bb1?arch="+arch {
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
		{"missing vendor", func(i *DiskInstalledInventory) { i.packages[0].Vendor = "" }, e.CodeProvenanceMissing},
		{"unknown vendor", func(i *DiskInstalledInventory) { i.packages[0].Vendor = "unknown" }, e.CodeProvenanceMissing},
		{"invalid vendor", func(i *DiskInstalledInventory) { i.packages[0].Vendor = "../ubuntu" }, e.CodeProvenanceMissing},
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

func TestInstalledDebPURLCanonicalExamples(t *testing.T) {
	for _, test := range []struct{ vendor, name, version, arch, want string }{
		{"debian", "attr", "1:2.4.47-2+b1", "amd64", "pkg:deb/debian/attr@1:2.4.47-2%2Bb1?arch=amd64"},
		{"ubuntu", "dpkg", "1.19.0.4", "amd64", "pkg:deb/ubuntu/dpkg@1.19.0.4?arch=amd64"},
	} {
		t.Run(test.vendor, func(t *testing.T) {
			if got := installedDebPURL(test.vendor, test.name, test.version, test.arch); got != test.want {
				t.Fatalf("purl %q, want %q", got, test.want)
			}
		})
	}
}

func TestDiskPackageDocumentDebianVersions(t *testing.T) {
	for _, test := range []struct {
		name, epoch, version, release, joined string
		valid                                 bool
	}{
		{"native", "0", "1.0+really0.9~rc1", "", "1.0+really0.9~rc1", true},
		{"epoch", "2", "1.0", "", "2:1.0", true},
		{"revision", "0", "1.0", "1~bpo12+1", "1.0-1~bpo12+1", true},
		{"upstream hyphens", "1", "1.0", "beta-2+b1", "1:1.0-beta-2+b1", true},
		{"version slash", "0", "1/2", "", "1/2", false},
		{"revision slash", "0", "1.0", "1/2", "1.0-1/2", false},
		{"empty revision", "0", "1.0-", "", "1.0-", false},
		{"revision underscore", "0", "1.0", "1_2", "1.0-1_2", false},
		{"non-numeric start", "0", "release1", "", "release1", false},
		{"noncanonical epoch", "01", "1.0", "", "01:1.0", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := packageInventoryFixture("amd64")
			inventory.packages = inventory.packages[:1]
			pkg := &inventory.packages[0]
			pkg.Epoch, pkg.Version, pkg.Release = test.epoch, test.version, test.release
			build := "sha256:" + strings.Repeat("e", 64)
			bom, err := DiskPackageDocument(build, "node", inventory)
			if test.valid {
				if err != nil || bom.Components[0].Version != test.joined {
					t.Fatalf("valid Debian version lost: bom=%v err=%v", bom, err)
				}
			} else if bom != nil || sbomCode(err) != e.CodeVerifyFailed {
				t.Fatalf("malformed Debian version accepted: bom=%v err=%v", bom, err)
			}

			// Exercise serialized validation independently, with matching purl,
			// reference and edge so those checks cannot mask version syntax.
			pkg.Epoch, pkg.Version, pkg.Release = "0", "1.0", ""
			bom, err = DiskPackageDocument(build, "node", inventory)
			if err != nil {
				t.Fatal(err)
			}
			component := &bom.Components[0]
			component.Version = test.joined
			component.PURL = installedDebPURL(pkg.Vendor, pkg.Name, test.joined, pkg.Architecture)
			component.BOMRef = component.PURL
			bom.Dependencies[0].DependsOn[0] = component.BOMRef
			err = ValidateDiskInstalledPackages(bom)
			if test.valid && err != nil || !test.valid && sbomCode(err) != e.CodeVerifyFailed {
				t.Fatalf("serialized Debian version %q: %v", test.joined, err)
			}
		})
	}
}

func TestDiskPackageInventoryRequiresArtifactLabel(t *testing.T) {
	build := "sha256:" + strings.Repeat("e", 64)
	bom, err := DiskPackageDocument(build, "node", packageInventoryFixture("amd64"))
	if err != nil {
		t.Fatal(err)
	}
	bom.Metadata.Props = slices.DeleteFunc(bom.Metadata.Props, func(p Property) bool {
		return p.Name == "nvkr:artifact" || p.Name == "nvkr:evidenceStage"
	})
	bom.SerialNumber = deriveURN(build)
	if err := bom.Validate(); sbomCode(err) != e.CodeVerifyFailed {
		t.Fatalf("unlabeled serialized package inventory: %v", err)
	}
}

func TestDiskPackageDocumentRejectsSignedClaimSubstitution(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*BOM)
		code e.Code
	}{
		{"wrong purl", func(b *BOM) { b.Components[0].PURL = "pkg:deb/other@1?arch=all" }, e.CodeVerifyFailed},
		{"missing vendor", func(b *BOM) {
			b.Components[0].Properties = slices.DeleteFunc(b.Components[0].Properties, func(p Property) bool { return p.Name == "nvkr:packageVendor" })
		}, e.CodeVerifyFailed},
		{"encoded epoch", func(b *BOM) { b.Components[1].PURL = strings.Replace(b.Components[1].PURL, "@1:", "@1%3A", 1) }, e.CodeVerifyFailed},
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
