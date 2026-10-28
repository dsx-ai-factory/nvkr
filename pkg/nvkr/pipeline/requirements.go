// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// rejectUnsupportedExecutionRequirements is the admission boundary until an
// adapter can authenticate and enforce the compiler-selected Environment class on
// the realized runtime. Structural validation and identity inspection remain
// available, but no adapter may silently ignore a request.
func rejectUnsupportedExecutionRequirements(nodes []ir.Node, adapter string) error {
	if err := rejectUnjoinedDebian(nodes, adapter); err != nil {
		return err
	}
	for _, node := range nodes {
		if node.Requirements != nil {
			return e.New(e.CodeEngineCapability,
				"%s cannot enforce execution requirements for node %q", adapter, node.ID)
		}
	}
	return nil
}

func rejectUnjoinedDebian(nodes []ir.Node, adapter string) error {
	for _, node := range nodes {
		if node.OutputType == catalog.OutputDebianPackage {
			return e.New(e.CodeEngineCapability, "%s cannot yet join standalone Debian build, inspection and final-byte evidence for node %q", adapter, node.ID)
		}
	}
	return nil
}
