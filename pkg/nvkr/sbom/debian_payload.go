// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// DebianPayloadMethod observes authoritative package identity and the complete
// exported payload with the existing pinned reader and scanner. It does not
// claim a host installation, runtime compatibility or package origin.
const DebianPayloadMethod = "syft-debian-payload/v1"

// InspectHeldDebianDependencies reuses the disk material owner's bounded Debian
// export, safe filesystem inventory, offline payload scan and nested-content
// budgets. The original DEB remains held and is never rebuilt or installed.
func InspectHeldDebianDependencies(ctx context.Context, subject *heldfile.File, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64) (*DependencyScanReceipt, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return nil, e.New(e.CodeEngineCapability, "Debian payload inspection requires a supported native Linux platform")
	}
	if subject == nil || subject.Size() <= 0 || subject.Size() > 1<<30 || maxBytes <= 0 || maxBytes > 16<<30 {
		return nil, e.New(e.CodeEngineCapability, "Debian payload inspection requires bounded held bytes and expansion")
	}
	if !canonical.IsDigest(scanner.Digest) || scanner.Path == "" || !canonical.IsDigest(packageReaderDigest) {
		return nil, e.New(e.CodeUnverifiedRef, "Debian payload inspection requires pinned scanner and package reader")
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "Debian payload inspection requires held executable support")
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "checking held Debian package")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	inspection, err := inspectDebianMaterial(ctx, subject, platform, scanner, packageReaderDigest, maxBytes)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "Debian payload inspection canceled")
		}
		return nil, err
	}
	if inspection.subject != subject.Digest() || inspection.size != subject.Size() || inspection.scanner != scanner.Digest {
		return nil, e.New(e.CodeVerifyFailed, "Debian payload inspection differs from exact held package")
	}
	components := inspection.Components()
	for index := range components {
		components[index] = diskDocumentComponent(components[index])
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking held Debian package")
	}
	return &DependencyScanReceipt{subject: subject.Digest(), size: subject.Size(), kind: "host-package", inventory: DependencyInventory{Components: components, Method: DebianPayloadMethod, ScannerDigest: scanner.Digest}, packageReader: packageReaderDigest, platform: platform}, nil
}

// WithDebianPayloadDependencies consumes only the scan owner's live receipt and
// binds both executable pins and target platform to the exact existing subject.
func WithDebianPayloadDependencies(bom *BOM, receipt *DependencyScanReceipt) (*BOM, error) {
	if receipt == nil || receipt.kind != "host-package" || receipt.inventory.Method != DebianPayloadMethod || !canonical.IsDigest(receipt.subject) || receipt.size <= 0 {
		return nil, e.New(e.CodeProvenanceMissing, "Debian payload dependencies require their exact owning scan receipt")
	}
	if err := bom.Validate(); err != nil {
		return nil, err
	}
	if len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != receipt.subject {
		return nil, e.New(e.CodeVerifyFailed, "Debian payload receipt differs from SBOM subject")
	}
	return withDependencies(bom, receipt.inventory.Components, DebianPayloadMethod, receipt.inventory.ScannerDigest, &debianInspectionBinding{packageReader: receipt.packageReader, platform: receipt.platform})
}

func ValidateDebianPayloadEvidence(bom *BOM, platform, subject string) error {
	if err := bom.Validate(); err != nil {
		return err
	}
	properties := map[string]string{}
	for _, property := range bom.Metadata.Props {
		properties[property.Name] = property.Value
	}
	if properties["nvkr:dependencyInspection"] != DebianPayloadMethod || properties["nvkr:dependencyPlatform"] != platform || len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != subject {
		return e.New(e.CodeProvenanceMissing, "Debian output requires its original package payload inspection")
	}
	return nil
}
