// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"debug/elf"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/diskinspect"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const (
	maxDiskDiagnosticBytes    = diskinspect.MaxDiagnosticBytes
	MaxDiagnosticDiskBytes    = diskinspect.MaxDiagnosticDiskBytes
	maxDiskPackageReportBytes = diskinspect.MaxPackageReportBytes
	maxDiskInstalledPackages  = diskinspect.MaxInstalledPackages
)

type InstalledDiskPackage = diskinspect.InstalledDiskPackage

// DiskInstalledInventory is an immutable, process-local observation. It binds
// installed package metadata to exact QCOW2 and inspector bytes. It deliberately
// has no complete-material-closure or full-disk evidence verdict.
type DiskInstalledInventory struct {
	helperName      string
	helper          pipeline.ProducedExecutableBinding
	subjectDigest   string
	subjectSize     int64
	inspectorDigest string
	codecDigest     string
	runtimeDigest   string
	packages        []InstalledDiskPackage
}

// ToolBinding describes the exact produced helper retained by the observation.
// It cannot recreate an inventory or a tool capability.
func (inventory *DiskInstalledInventory) ToolBinding() (string, pipeline.ProducedExecutableBinding) {
	if inventory == nil {
		return "", pipeline.ProducedExecutableBinding{}
	}
	return inventory.helperName, inventory.helper
}

// RuntimeBinding describes the runtime-reported image identity retained when
// DiskInspectionRuntime issued this inventory. It is not a repository-approved
// image pin, provider-isolation proof, or production admission receipt. An
// executable-only diagnostic inventory returns an empty value.
func (inventory *DiskInstalledInventory) RuntimeBinding() string {
	if inventory == nil {
		return ""
	}
	return inventory.runtimeDigest
}

func (inventory *DiskInstalledInventory) Packages() []InstalledDiskPackage {
	if inventory == nil {
		return nil
	}
	return slices.Clone(inventory.packages)
}

// Binding returns descriptive exact-byte identities, not a serialized receipt.
func (inventory *DiskInstalledInventory) Binding() (subject string, size int64, inspector, codec string) {
	if inventory == nil {
		return "", 0, "", ""
	}
	return inventory.subjectDigest, inventory.subjectSize, inventory.inspectorDigest, inventory.codecDigest
}

// DiskInspector delegates executable-only inspection to the isolated helper owner.
type DiskInspector struct{ helper *diskinspect.DiskInspector }

func OpenDiskInspector(ctx context.Context, path, digest string, codec *assemble.QemuImgCodec) (*DiskInspector, error) {
	helper, err := diskinspect.OpenDiskInspector(ctx, path, digest, codec)
	if err != nil {
		return nil, err
	}
	return &DiskInspector{helper: helper}, nil
}

func (inspector *DiskInspector) Close() error {
	if inspector == nil {
		return nil
	}
	return inspector.helper.Close()
}

func (inspector *DiskInspector) InspectHeldInstalledPackages(ctx context.Context, subject *heldfile.File, format, encryption string) (*DiskInstalledInventory, error) {
	var helper *diskinspect.DiskInspector
	if inspector != nil {
		helper = inspector.helper
	}
	observation, err := helper.InspectHeldInstalledPackages(ctx, subject, format, encryption)
	if err != nil {
		return nil, err
	}
	digest, size, tool, codec := observation.Binding()
	return &DiskInstalledInventory{subjectDigest: digest, subjectSize: size, inspectorDigest: tool, codecDigest: codec, packages: observation.Packages()}, nil
}

func diskInspectorELF(image *elf.File) bool { return diskinspect.NativeELF(image) }
func installedPackagesFromXML(raw []byte) ([]InstalledDiskPackage, error) {
	return diskinspect.ParseInstalledPackagesXML(raw)
}
func checkedInstalledDiskPackages(packages []InstalledDiskPackage) ([]InstalledDiskPackage, error) {
	return diskinspect.CheckInstalledPackages(packages)
}
