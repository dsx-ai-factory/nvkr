// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianDefinitionRequiresExplicitPlatformAndOneProducer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  DefinitionOutput
		invalid bool
	}{
		{"amd64", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platform: "linux/amd64", Components: []string{"agent"}}, false},
		{"arm64", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platform: "linux/arm64", Components: []string{"agent"}}, false},
		{"missing platform", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Components: []string{"agent"}}, true},
		{"multiple packages", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platform: "linux/amd64", Components: []string{"agent", "other"}}, true},
		{"no packages", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platform: "linux/amd64"}, true},
		{"platform set", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platforms: []string{"linux/amd64"}, Components: []string{"agent"}}, true},
		{"foreign platform", DefinitionOutput{Name: "agent", Format: OutputDebianPackage, Platform: "darwin/arm64", Components: []string{"agent"}}, true},
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
