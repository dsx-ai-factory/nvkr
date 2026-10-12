// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPackerAcquisitionArchiveRejectsUnsafeOrDiskPaths(t *testing.T) {
	for _, path := range []string{".", "../capture.tar", "/capture.tar", "capture-{{.Arch}}.tar", "node.qcow2"} {
		t.Run(path, func(t *testing.T) {
			graph := vmGraph(t)
			graph.Nodes[0].Packer.AcquisitionArchive = path
			assertIRCode(t, graph.Validate(), e.CodeInvalidCatalog)
		})
	}
}
