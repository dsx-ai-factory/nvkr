// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDiskEvidenceOverrides(t *testing.T) {
	definition := Definition{Outputs: []DefinitionOutput{{Name: "plain", Format: OutputDiskImage}, {Name: "fde", Format: OutputDiskImage}, {Name: "cli", Format: OutputExecutable}}}
	for _, test := range []struct {
		name, mode string
		want       e.Code
	}{
		{"fde", "build-provenance-v1", ""}, {"fde", "", ""},
		{"missing", "build-provenance-v1", e.CodeInvalidCatalog}, {"cli", "build-provenance-v1", e.CodeInvalidCatalog},
		{"fde", "invented", e.CodeEngineCapability},
	} {
		t.Run(test.name+test.mode, func(t *testing.T) {
			environment := Environment{GitHubARC: "native-kvm-v1", Publication: &Publication{}, DiskEvidence: "installed-packages-v1", DiskEvidenceOverrides: map[string]string{test.name: test.mode}}
			err := environment.validateDiskEvidence()
			if err == nil {
				err = environment.ValidateDiskEvidenceOutputs(definition)
			}
			if test.want != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.want {
					t.Fatalf("err=%v, want %s", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if environment.DiskEvidenceFor("plain") != "installed-packages-v1" || environment.DiskEvidenceFor("fde") != test.mode {
				t.Fatal("output override did not retain the explicit mode")
			}
		})
	}
}
