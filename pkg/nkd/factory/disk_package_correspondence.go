// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"slices"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// DiskPackageCandidates groups source-reported capture metadata by exact name
// and architecture with independently inspected installed metadata. Candidates
// are not installation proof. Versions remain verbatim: no guessed Debian
// version normalization, package parser or file-content comparison occurs here.
type DiskPackageCandidates struct {
	Installed sbom.InstalledDiskPackage
	Captured  []material.AcquiredPackage
}

// DiskPackageCorrespondenceDescription is diagnostic. Coverage gaps are
// unconditional because neither staged producers, APT pre-install capture nor
// installed package metadata proves the complete software or file closure.
type DiskPackageCorrespondenceDescription struct {
	Inventory         DiskInstalledInventoryDescription
	ArchiveDigest     string
	ArchiveSize       int64
	ManifestDigest    string
	Capture           material.AcquisitionManifest
	Candidates        []DiskPackageCandidates
	UnmatchedCaptures []material.AcquiredPackage
	CoverageGaps      []string
}

// DiskPackageCorrespondenceReceipt binds one producer-owned acquisition carrier
// and issued installed inventory to the original live disk attempt. It borrows
// the Run and inspector lifetimes. Serialization cannot restore it; it grants no
// full-disk SBOM, repository-origin, publication or reproduction authority.
type DiskPackageCorrespondenceReceipt struct {
	owner       *LocalRunResult
	inventory   *DiskInstalledInventoryReceipt
	description DiskPackageCorrespondenceDescription
}

func (receipt *DiskPackageCorrespondenceReceipt) Description() DiskPackageCorrespondenceDescription {
	if receipt == nil || receipt.owner == nil || receipt.inventory == nil {
		return DiskPackageCorrespondenceDescription{}
	}
	view := receipt.description
	view.Inventory = receipt.inventory.Description()
	view.Capture.Packages = cloneCapturedPackages(view.Capture.Packages)
	view.Candidates = slices.Clone(view.Candidates)
	for index := range view.Candidates {
		view.Candidates[index].Captured = cloneCapturedPackages(view.Candidates[index].Captured)
	}
	view.UnmatchedCaptures = cloneCapturedPackages(view.UnmatchedCaptures)
	view.CoverageGaps = slices.Clone(view.CoverageGaps)
	return view
}

func cloneCapturedPackages(packages []material.AcquiredPackage) []material.AcquiredPackage {
	result := slices.Clone(packages)
	for index := range result {
		result[index].RepositoryURIs = slices.Clone(result[index].RepositoryURIs)
	}
	return result
}

type diskAcquisitionScan func(context.Context, *diskPayload) (*material.AcquisitionObservation, error)

func observeProducerAcquisitions(ctx context.Context, payload *diskPayload) (*material.AcquisitionObservation, error) {
	artifact, ok := payload.content.(*pipeline.PackerArtifact)
	if !ok || artifact == nil {
		return nil, e.New(e.CodeEngineCapability, "acquisition join requires the original Packer artifact")
	}
	return artifact.ObserveAcquisitionsContext(ctx)
}

// ObserveDiskPackageCorrespondence consumes no caller-selected archive. The
// exact carrier is retained by the same Packer artifact as the disk, and the
// inventory must have been issued for that output by this still-open Run.
func (owner *LocalRunResult) ObserveDiskPackageCorrespondence(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime, inventory *DiskInstalledInventoryReceipt,
) (*DiskPackageCorrespondenceReceipt, error) {
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "acquisition join requires the admitted inspector")
	}
	return owner.observeDiskPackageCorrespondence(ctx, plan, expected, runtime, inventory,
		runtimeInventoryBindings(runtime), observeProducerAcquisitions)
}

func (owner *LocalRunResult) observeDiskPackageCorrespondence(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime, inventory *DiskInstalledInventoryReceipt,
	bindings diskInventoryBindings, scan diskAcquisitionScan,
) (*DiskPackageCorrespondenceReceipt, error) {
	if ctx == nil || owner == nil || scan == nil {
		return nil, e.New(e.CodeUsage, "acquisition join requires context and the live Run")
	}
	ctx, release, err := owner.beginDiskInspection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := owner.validateDiskInstalledInventoryWhileHeld(ctx, plan, expected, runtime, inventory, bindings); err != nil {
		return nil, err
	}
	var platform string
	for _, build := range plan.Builds {
		if build.Name == expected.Name {
			platform = build.Platform
		}
	}
	architecture := strings.TrimPrefix(platform, "linux/")
	if architecture != "amd64" && architecture != "arm64" {
		return nil, e.New(e.CodeEngineCapability, "acquisition join requires an AMD64 or ARM64 consumer")
	}
	var payload *diskPayload
	owner.diskMu.Lock()
	for _, completion := range owner.diskCompletions {
		if completion.NodeID == expected.Name+"/"+inventory.description.Subject.Producer {
			payload = completion.Payload
		}
	}
	owner.diskMu.Unlock()
	if payload == nil {
		return nil, e.New(e.CodeVerifyFailed, "acquisition join lost its original disk")
	}
	capture, err := scan(ctx, payload)
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk acquisition join canceled")
	}
	if err != nil {
		return nil, err
	}
	if capture == nil {
		return nil, e.New(e.CodeVerifyFailed, "producer returned no acquisition observation")
	}
	manifest := capture.Description()
	if manifest.Architecture != architecture {
		return nil, e.New(e.CodeVerifyFailed, "acquisition carrier does not match the consumer architecture")
	}
	archive, size, document := capture.Binding()
	view := DiskPackageCorrespondenceDescription{Inventory: inventory.Description(), ArchiveDigest: archive,
		ArchiveSize: size, ManifestDigest: document, Capture: manifest,
		CoverageGaps: []string{"base-installer-content", "installed-files-and-staged-archives", "private-source-and-non-apt-acquisitions"}}
	matched := make(map[string]bool)
	type packageKey struct{ name, architecture string }
	byPackage := make(map[packageKey][]material.AcquiredPackage)
	for _, acquired := range manifest.Packages {
		key := packageKey{acquired.Name, acquired.Architecture}
		byPackage[key] = append(byPackage[key], acquired)
	}
	for _, installed := range view.Inventory.Packages {
		if installed.Architecture != "" && installed.Architecture != "all" && installed.Architecture != architecture {
			return nil, e.New(e.CodeVerifyFailed, "installed package %q disagrees with the consumer architecture", installed.Name)
		}
		row := DiskPackageCandidates{Installed: installed,
			Captured: byPackage[packageKey{installed.Name, installed.Architecture}]}
		for _, acquired := range row.Captured {
			matched[acquired.Digest] = true
		}
		view.Candidates = append(view.Candidates, row)
	}
	for _, acquired := range manifest.Packages {
		if !matched[acquired.Digest] {
			view.UnmatchedCaptures = append(view.UnmatchedCaptures, acquired)
		}
	}
	if err := owner.validateDiskInstalledInventoryWhileHeld(ctx, plan, expected, runtime, inventory, bindings); err != nil {
		return nil, err
	}
	return &DiskPackageCorrespondenceReceipt{owner: owner, inventory: inventory, description: view}, nil
}

// ValidateDiskPackageCorrespondence re-observes both the live disk/inspector
// binding and its still-held carrier. Equal bytes from a sibling attempt cannot
// restore the issuing owner, and closing the Run revokes shallow copies too.
func (owner *LocalRunResult) ValidateDiskPackageCorrespondence(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime, receipt *DiskPackageCorrespondenceReceipt,
) error {
	if runtime == nil {
		return e.New(e.CodeEngineCapability, "validating correspondence requires its admitted inspector")
	}
	return owner.validateDiskPackageCorrespondence(ctx, plan, expected, runtime, receipt,
		runtimeInventoryBindings(runtime), observeProducerAcquisitions)
}

func (owner *LocalRunResult) validateDiskPackageCorrespondence(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime, receipt *DiskPackageCorrespondenceReceipt,
	bindings diskInventoryBindings, scan diskAcquisitionScan,
) error {
	if receipt == nil || receipt.owner != owner || receipt.inventory == nil {
		return e.New(e.CodeVerifyFailed, "package correspondence belongs to a different Run")
	}
	current, err := owner.observeDiskPackageCorrespondence(ctx, plan, expected, runtime, receipt.inventory, bindings, scan)
	if err != nil {
		return err
	}
	want, got := receipt.description, current.description
	if want.ArchiveDigest != got.ArchiveDigest || want.ArchiveSize != got.ArchiveSize || want.ManifestDigest != got.ManifestDigest {
		return e.New(e.CodeVerifyFailed, "package correspondence carrier changed")
	}
	return nil
}
