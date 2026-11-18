// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BootDiskBinding describes the raw file bound by retained original metadata.
// It neither reads payload bytes nor authenticates signatures or membership.
func (owner *SelectedMetadata) BootDiskBinding(reference string, expected definition.BootDisk) (BootAssetBinding, error) {
	if owner == nil || owner.state == nil {
		return BootAssetBinding{}, e.New(e.CodeGateUnmet, "selected metadata is not open")
	}
	owner.state.mu.RLock()
	defer owner.state.mu.RUnlock()
	output, err := owner.bootOutputLocked(reference, expected.Output, expected.Platform, "disk-image")
	if err != nil {
		return BootAssetBinding{}, err
	}
	if output.Disk == nil || output.Disk.Format != "qcow2" || output.Disk.Size <= 0 {
		return BootAssetBinding{}, e.New(e.CodeVerifyFailed, "selected metadata has no exact raw QCOW2 binding")
	}
	binding := bootBinding(reference, output)
	binding.SHA256, binding.Size = output.Disk.PayloadDigest, output.Disk.Size
	return binding, nil
}

// BootImagerBinding joins the exact original regular member and compatibility
// claim without fetching its archive. The caller must authenticate the original
// subject and independently hash the raw object; this is not an EFI receipt.
func (owner *SelectedMetadata) BootImagerBinding(reference string, expected definition.BootImager) (BootAssetBinding, error) {
	if err := expected.Validate(); err != nil {
		return BootAssetBinding{}, err
	}
	if owner == nil || owner.state == nil {
		return BootAssetBinding{}, e.New(e.CodeGateUnmet, "selected metadata is not open")
	}
	owner.state.mu.RLock()
	defer owner.state.mu.RUnlock()
	output, err := owner.bootOutputLocked(reference, expected.Output, expected.Platform, "executable")
	if err != nil {
		return BootAssetBinding{}, err
	}
	bundle, exists := owner.binaries[expected.Output]
	if !exists {
		return BootAssetBinding{}, e.New(e.CodeVerifyFailed, "selected metadata has no original binary binding")
	}
	entry, err := bundle.BootMember(expected)
	if err != nil {
		return BootAssetBinding{}, err
	}
	binding := bootBinding(reference, output)
	binding.Member, binding.Profile = expected.Member, expected.Profile
	binding.SHA256, binding.Size = entry.SHA256, entry.Size
	return binding, nil
}

func (owner *SelectedMetadata) bootOutputLocked(reference, name, platform, kind string) (PublicationOutput, error) {
	if owner.state.closed {
		return PublicationOutput{}, e.New(e.CodeGateUnmet, "selected metadata is closed")
	}
	if err := ValidatePublicationReference(reference); err != nil {
		return PublicationOutput{}, err
	}
	if !owner.selection[name] {
		return PublicationOutput{}, e.New(e.CodeVerifyFailed, "output is outside the retained metadata selection")
	}
	description, err := publicationDescription(reference, owner.state.inspection, owner.state.outputs, owner.state.platforms)
	if err != nil {
		return PublicationOutput{}, err
	}
	for _, output := range description.Outputs {
		if output.Name == name && output.Kind == kind && output.OS+"/"+output.Arch == platform {
			return output, nil
		}
	}
	return PublicationOutput{}, e.New(e.CodeVerifyFailed, "selected metadata differs from the exact named output, kind or platform")
}
