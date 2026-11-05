// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func TestDebianPayloadReceiptAttachmentBindsOriginalSubjectAndPins(t *testing.T) {
	base := dependencySubject(t)
	subject := "sha256:" + base.Metadata.Component.Hashes[0].Content
	receipt := &DependencyScanReceipt{subject: subject, size: 100, kind: "host-package", inventory: DependencyInventory{Components: []Component{{Type: "library", Name: "payload-library", Version: "1.2", PURL: "pkg:generic/payload-library@1.2"}}, Method: DebianPayloadMethod, ScannerDigest: "sha256:" + strings.Repeat("b", 64)}, packageReader: "sha256:" + strings.Repeat("c", 64), platform: "linux/amd64"}
	bom, err := WithDebianPayloadDependencies(base, receipt)
	if err != nil {
		t.Fatal(err)
	}
	properties := map[string]string{}
	for _, property := range bom.Metadata.Props {
		properties[property.Name] = property.Value
	}
	if properties["nvkr:dependencyInspection"] != DebianPayloadMethod || properties["nvkr:packageReaderDigest"] != receipt.packageReader || properties["nvkr:dependencyPlatform"] != receipt.platform {
		t.Fatal("Debian payload scanner/reader/platform not bound")
	}
	raw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded BOM
	if err := canonical.UnmarshalExact([]byte(strings.TrimSuffix(string(raw), "\n")), &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	_, err = WithDependencies(base, receipt.Inventory().Components, DebianPayloadMethod, receipt.inventory.ScannerDigest)
	assertDependencyCode(t, err, e.CodeUnverifiedRef)
	for _, test := range []struct {
		name   string
		mutate func(*DependencyScanReceipt)
		code   e.Code
	}{
		{"subject", func(receipt *DependencyScanReceipt) { receipt.subject = "sha256:" + strings.Repeat("d", 64) }, e.CodeVerifyFailed},
		{"kind", func(receipt *DependencyScanReceipt) { receipt.kind = "oci-image" }, e.CodeProvenanceMissing},
		{"metadata diagnostic", func(receipt *DependencyScanReceipt) { receipt.inventory.Method = debianArchiveMetadataMethod }, e.CodeProvenanceMissing},
		{"reader pin", func(receipt *DependencyScanReceipt) { receipt.packageReader = "" }, e.CodeUnverifiedRef},
		{"platform", func(receipt *DependencyScanReceipt) { receipt.platform = "darwin/arm64" }, e.CodeUnverifiedRef},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *receipt
			test.mutate(&changed)
			_, err := WithDebianPayloadDependencies(base, &changed)
			assertDependencyCode(t, err, test.code)
		})
	}
	if len(base.Components) != 0 {
		t.Fatal("typed attachment mutated input BOM")
	}
}

func TestDebianPayloadInspectionUsesExistingPackageAndPayloadOwners(t *testing.T) {
	for _, payload := range []bool{false, true} {
		subject, scanner, reader := debianMaterialOperationFixture(t, payload)
		receipt, err := InspectHeldDebianDependencies(t.Context(), subject, "linux/"+runtime.GOARCH, scanner, reader, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		got, size, kind := receipt.Binding()
		if got != subject.Digest() || size != subject.Size() || kind != "host-package" {
			t.Fatal("package receipt changed original DEB subject")
		}
		inventory := receipt.Inventory()
		if inventory.Method != DebianPayloadMethod || len(inventory.Components) == 0 || inventory.ScannerDigest != scanner.Digest {
			t.Fatalf("payload owner missing: %+v", inventory)
		}
		inventory.Components[0].Name = "caller-change"
		if receipt.Inventory().Components[0].Name == "caller-change" {
			t.Fatal("inventory aliases owning receipt")
		}
		bom, err := NewSubject("sha256:"+strings.Repeat("a", 64), "package", subject.Digest())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := WithDebianPayloadDependencies(bom, receipt); err != nil {
			t.Fatal(err)
		}
		_, err = InspectHeldDebianDependencies(t.Context(), subject, "linux/"+runtime.GOARCH, scanner, "sha256:"+strings.Repeat("f", 64), 1<<20)
		assertDependencyCode(t, err, e.CodeUnverifiedRef)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = InspectHeldDebianDependencies(ctx, subject, "linux/"+runtime.GOARCH, scanner, reader, 1<<20)
		assertDependencyCode(t, err, e.CodeTimeout)
		corrupt, err := heldfile.FromBytesContext(t.Context(), []byte("not a Debian package"), heldfile.Options{Label: "bad-debian", MaxBytes: 100})
		if err != nil {
			t.Fatal(err)
		}
		_, err = InspectHeldDebianDependencies(t.Context(), corrupt, "linux/"+runtime.GOARCH, scanner, reader, 1<<20)
		assertDependencyCode(t, err, e.CodeVerifyFailed)
		if err := corrupt.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDebianPayloadInspectionAdmitsBeforeTools(t *testing.T) {
	for _, test := range []struct {
		name     string
		ctx      context.Context
		platform string
		pin      string
		bound    int64
		code     e.Code
	}{
		{"missing context", nil, "linux/amd64", "", 0, e.CodeUsage},
		{"unsupported platform", t.Context(), "darwin/arm64", "", 0, e.CodeEngineCapability},
		{"missing subject", t.Context(), "linux/amd64", "", 1, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt, err := InspectHeldDebianDependencies(test.ctx, nil, test.platform, Scanner{}, test.pin, test.bound)
			assertDependencyCode(t, err, test.code)
			if receipt != nil {
				t.Fatal("refused inspection returned receipt")
			}
		})
	}
}
