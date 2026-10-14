// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ValidateVMImagePackerDiskExecution admits the disk encryption contract before
// any runtime, source, material or output work. LUKS2 execution is limited to the
// explicitly public bootstrap value luks_passphrase=changeit. That literal is a
// normal identity-bound build input, provides no confidentiality, and must not
// be replaced with a credential. This check grants neither publication nor
// qualification authority; complete Plan and runtime admission remain required.
func ValidateVMImagePackerDiskExecution(node ir.Node) error {
	if node.Disk != nil && node.Disk.Format == "qcow2" {
		if node.Disk.Encryption == "none" {
			return nil
		}
		if node.Disk.Encryption == "luks2" && node.BuilderType == ir.BuilderVMImage &&
			node.Packer != nil && len(node.Command) == 0 {
			bindings := 0
			publicBootstrap := false
			for _, variable := range node.Packer.Variables {
				if variable.Name == "luks_passphrase" {
					bindings++
					publicBootstrap = variable.Value == "changeit"
				}
			}
			if bindings == 1 && publicBootstrap {
				return nil
			}
		}
	}
	// Never interpolate a rejected value: this boundary has no secret channel.
	return e.New(e.CodeEngineCapability,
		"VM-image disk execution requires unencrypted QCOW2 or direct Packer with explicit public luks_passphrase=changeit; a private encryption secret channel is unavailable")
}
