// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/google/go-containerregistry/pkg/name"
)

// DiskInspectionProfile selects observation infrastructure independently of a
// output's producer/toolchain. The Environment and frozen Plan bind it; changing
// it does not change the logical source Build identity.
type DiskInspectionProfile struct {
	Image               string                    `json:"image"`
	InspectorDigest     string                    `json:"inspectorDigest"`
	CodecDigest         string                    `json:"codecDigest"`
	GuestfishDigest     string                    `json:"guestfishDigest"`
	ScannerDigest       string                    `json:"scannerDigest"`
	PackageReaderDigest string                    `json:"packageReaderDigest"`
	Resources           execution.ResourceRequest `json:"resources"`
	TimeoutSeconds      uint64                    `json:"timeoutSeconds"`
	MaxFilesystemBytes  uint64                    `json:"maxFilesystemBytes"`
	ScratchStorage      string                    `json:"scratchStorage"`
}

// Validate admits only an explicit native host profile and exact closure pins.
// The adapter separately authenticates image/runtime/tool bytes and capacity.
func (profile DiskInspectionProfile) Validate(platform string) error {
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "disk inspection profile requires a native Linux AMD64 or ARM64 platform")
	}
	image, err := name.NewDigest(profile.Image, name.StrictValidation)
	if err != nil || image.Name() != profile.Image || !canonical.IsDigest(image.DigestStr()) || strings.ContainsAny(profile.Image, "\r\n\x00") {
		return e.New(e.CodeUnpinnedInput, "disk inspection profile requires a canonical digest-pinned OCI image")
	}
	for _, pin := range []string{profile.InspectorDigest, profile.CodecDigest, profile.GuestfishDigest, profile.ScannerDigest, profile.PackageReaderDigest} {
		if !canonical.IsDigest(pin) {
			return e.New(e.CodeUnpinnedInput, "disk inspection profile requires all exact executable pins")
		}
	}
	if profile.Resources.CPUMillis < 10 || profile.Resources.MemoryMiB < 2048 || profile.Resources.EphemeralStorageMiB == 0 ||
		profile.TimeoutSeconds == 0 || profile.TimeoutSeconds > 600 || profile.MaxFilesystemBytes == 0 || profile.MaxFilesystemBytes > 16<<30 {
		return e.New(e.CodeEngineCapability, "disk inspection profile requires bounded filesystem, CPU, memory, scratch and wall-time capacity")
	}
	if profile.ScratchStorage != "memory" && profile.ScratchStorage != "private-volume" {
		return e.New(e.CodeEngineCapability, "disk inspection profile requires explicit memory or private-volume scratch")
	}
	return nil
}
