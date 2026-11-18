// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const BinaryBootSchemaVersion = "nvkr.dev/binary-bundle/v2"

// BinaryBootBinding is an original producer declaration about one regular
// member. Its hash/size and platform come from the same signed bundle config.
// The copier cannot add this claim to an existing artifact. The producer owns
// PE/UKI inspection and profile acceptance; this record is not boot evidence.
type BinaryBootBinding struct {
	Member  string `json:"member"`
	Profile string `json:"profile"`
}

func (binding *BinaryBootBinding) clone() *BinaryBootBinding {
	if binding == nil {
		return nil
	}
	copy := *binding
	return &copy
}

func validateBinaryBootBinding(binding *BinaryBootBinding, purpose, osName, arch string) error {
	if binding == nil {
		return e.New(e.CodeVerifyFailed, "boot binary bundle requires an original member/profile binding")
	}
	return (definition.BootImager{Output: purpose, Member: binding.Member, Profile: binding.Profile, Platform: osName + "/" + arch}).Validate()
}

func admitBinaryBootRequest(request BinaryBundleRequest) error {
	if request.Boot == nil {
		return nil
	}
	if err := validateBinaryBootBinding(request.Boot, request.Purpose, request.OS, request.Arch); err != nil {
		return err
	}
	found := 0
	for _, file := range request.Files {
		if file.Name == request.Boot.Member {
			found++
		}
	}
	if found != 1 {
		return e.New(e.CodeInvalidCatalog, "boot binary bundle must declare exactly one matching regular member")
	}
	return nil
}

// BootMember checks the original metadata against an independently selected
// named output, member, profile and platform. Success is descriptive integrity;
// lifecycle must authenticate the original subject and exact output membership.
func (bundle BinaryBundle) BootMember(expected definition.BootImager) (BinaryEntry, error) {
	if err := expected.Validate(); err != nil {
		return BinaryEntry{}, err
	}
	if err := bundle.Validate(); err != nil {
		return BinaryEntry{}, err
	}
	if bundle.SchemaVersion != BinaryBootSchemaVersion || bundle.Boot == nil {
		return BinaryEntry{}, e.New(e.CodeEngineCapability, "original binary bundle lacks a versioned imager binding")
	}
	if bundle.Purpose != expected.Output || bundle.OS+"/"+bundle.Arch != expected.Platform ||
		bundle.Boot.Member != expected.Member || bundle.Boot.Profile != expected.Profile {
		return BinaryEntry{}, e.New(e.CodeVerifyFailed, "original imager metadata differs from the exact expected output/member/platform/profile")
	}
	for _, entry := range bundle.Entries {
		if entry.Name == expected.Member {
			return entry, nil
		}
	}
	return BinaryEntry{}, e.New(e.CodeVerifyFailed, "original imager binding has no regular member")
}
