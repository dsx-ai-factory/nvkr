// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// RuntimeInvocation is the fixed filesystem and invocation contract of the
// produced-runtime Packer consumer. Changing that contract changes Build identity.
const RuntimeInvocation = "nvkr-packer-rootfs/v1"

// RuntimeSelection describes an exact named Run-only producer. It is a logical
// Build input, not authority to execute a downloaded root filesystem. Execution
// additionally requires the original Run's live production and evidence receipt.
type RuntimeSelection struct {
	Tool        string `json:"tool"`
	BuildDigest string `json:"buildDigest"`
	Component   string `json:"component"`
	Platform    string `json:"platform"`
	Invocation  string `json:"invocation"`
}

func (selection RuntimeSelection) Validate() error {
	if !canonicalNodeID.MatchString(selection.Tool) || !canonicalNodeID.MatchString(selection.Component) ||
		!canonical.IsDigest(selection.BuildDigest) {
		return e.New(e.CodeUnpinnedSource, "produced runtime requires an exact named producer and Build digest")
	}
	if selection.Platform != "linux/amd64" || selection.Invocation != RuntimeInvocation {
		return e.New(e.CodeEngineCapability, "produced runtime requires the admitted amd64 Packer rootfs invocation")
	}
	return nil
}

func cloneRuntimeSelection(selection *RuntimeSelection) *RuntimeSelection {
	if selection == nil {
		return nil
	}
	copy := *selection
	return &copy
}
