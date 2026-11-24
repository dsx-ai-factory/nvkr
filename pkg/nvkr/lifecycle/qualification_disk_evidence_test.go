// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestQualificationDiskEvidenceReflectsSignedMode(t *testing.T) {
	for _, test := range []struct{ mode, want string }{
		{sbom.DiskBuildProvenance, DiskEvidenceDescriptionBuildProvenance},
		{sbom.DiskInstalledPackages, DiskEvidenceDescriptionPackages},
		{"", DiskEvidenceDescriptionStrict},
		{"invented", ""},
	} {
		t.Run(test.mode, func(t *testing.T) {
			got, err := qualificationDiskEvidence(test.mode)
			if test.mode == "invented" {
				assertCode(t, err, e.CodeVerifyFailed)
			} else if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("scope %q, want %q", got, test.want)
			}
		})
	}
}
