// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPackerAcquisitionArchiveIsExactOutputRelativePath(t *testing.T) {
	for _, tt := range []struct {
		path  string
		valid bool
	}{
		{"", true}, {"captures/packages.tar", true}, {"../capture.tar", false}, {"/capture.tar", false},
		{".", false}, {"captures/../capture.tar", false}, {"capture-{{.Arch}}.tar", false}, {"capture\n.tar", false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			config := &PackerConfig{ProjectPath: "packer", PackerVersion: "1.15.4", BuildName: "node.qemu.node",
				ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "materials", OutputDirectoryVariable: "output", AcquisitionArchive: tt.path}
			err := validatePackerConfig("node", config)
			if tt.valid && err != nil {
				t.Fatal(err)
			}
			if !tt.valid {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
}
