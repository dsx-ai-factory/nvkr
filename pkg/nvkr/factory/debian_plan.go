// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func debianOutputRequest(output authored.DefinitionOutput) *target.Debian {
	if output.Format != authored.OutputDebianPackage {
		return nil
	}
	request := output.DebianTargetRequest()
	return &request
}

func cloneDebianTarget(value *target.Debian) *target.Debian {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Compilation/Plan inspection remains available while execution cannot yet join
// its exact source archive to content inspection and final-output evidence.
func rejectUnjoinedDebianPlan(plan *Plan, adapter string) error {
	for _, output := range plan.Definition.Outputs {
		if output.Format == authored.OutputDebianPackage {
			return e.New(e.CodeEngineCapability, "%s cannot yet join Debian archive inspection and final-byte producer/output evidence", adapter)
		}
	}
	return nil
}
