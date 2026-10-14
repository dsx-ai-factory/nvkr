// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianDefinitionTargetDefaultsAreFormatSpecific(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  DefinitionOutput
		invalid bool
	}{
		{"omitted defaults", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Components: []string{"agent"}}, false},
		{"explicit target", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platform: "linux/arm64", Components: []string{"agent"}, Debian: &DebianOutput{Distribution: "debian-13"}}, false},
		{"ordinary omission", DefinitionOutput{Name: "agent", Format: OutputExecutable, Components: []string{"agent"}}, true},
		{"ordinary Debian field", DefinitionOutput{Name: "agent", Format: OutputExecutable, Platform: "linux/amd64", Components: []string{"agent"}, Debian: &DebianOutput{}}, true},
		{"multiple packages", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Components: []string{"agent", "other"}}, true},
		{"platform set", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platforms: []string{"linux/amd64"}, Components: []string{"agent"}}, true},
		{"invalid profile", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Components: []string{"agent"}, Debian: &DebianOutput{Distribution: "ubuntu-*"}}, true},
		{"invalid platform", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platform: "linux/riscv64", Components: []string{"agent"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: "deb"}, Catalog: Catalog{Base: "catalog"}, Outputs: []DefinitionOutput{tc.output}}
			err := input.Validate()
			if tc.invalid {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
					t.Fatalf("admission = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDebianDefinitionCloneOwnsTargetDescription(t *testing.T) {
	input := Definition{Outputs: []DefinitionOutput{{Debian: &DebianOutput{Distribution: "ubuntu-24.04"}}}}
	copy := input.Clone()
	input.Outputs[0].Debian.Distribution = "changed"
	if copy.Outputs[0].Debian.Distribution != "ubuntu-24.04" {
		t.Fatal("clone aliases distribution")
	}
	output := DefinitionOutput{Format: OutputDebianPackage}
	if got := output.TargetPlatforms(); len(got) != 1 || got[0] != "linux/amd64" {
		t.Fatalf("standalone target description = %v", got)
	}
}
