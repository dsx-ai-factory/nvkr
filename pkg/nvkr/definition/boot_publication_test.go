// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func bootSelectionFixture() BootSelection {
	return BootSelection{SchemaVersion: BootSelectionSchemaVersion,
		Disks: []BootDisk{{Output: "node-amd64", Platform: "linux/amd64"}, {Output: "node-arm64", Platform: "linux/arm64"}},
		Imagers: []BootImager{
			{Output: "imager-amd64", Member: "qcow-imager.efi", Platform: "linux/amd64", Profile: "test-profile-v1"},
			{Output: "imager-arm64", Member: "qcow-imager.efi", Platform: "linux/arm64", Profile: "test-profile-v1"},
		},
	}
}

func TestBootSelectionAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*BootSelection)
		code   e.Code
	}{
		{name: "both architectures"},
		{name: "one architecture", change: func(s *BootSelection) { s.Disks, s.Imagers = s.Disks[:1], s.Imagers[:1] }},
		{name: "shared imager", change: func(s *BootSelection) {
			s.Disks = append([]BootDisk{{Output: "another-node", Platform: "linux/amd64"}}, s.Disks...)
		}},
		{name: "schema", change: func(s *BootSelection) { s.SchemaVersion = "nvkr.dev/boot-selection/v2" }, code: e.CodeEngineCapability},
		{name: "empty disks", change: func(s *BootSelection) { s.Disks = nil }, code: e.CodeInvalidCatalog},
		{name: "empty imagers", change: func(s *BootSelection) { s.Imagers = nil }, code: e.CodeInvalidCatalog},
		{name: "duplicate disk", change: func(s *BootSelection) { s.Disks[1] = s.Disks[0] }, code: e.CodeInvalidCatalog},
		{name: "unsorted disks", change: func(s *BootSelection) { s.Disks[0], s.Disks[1] = s.Disks[1], s.Disks[0] }, code: e.CodeInvalidCatalog},
		{name: "wildcard", change: func(s *BootSelection) { s.Disks[0].Output = "node-*" }, code: e.CodeInvalidCatalog},
		{name: "host platform", change: func(s *BootSelection) { s.Disks[0].Platform = "" }, code: e.CodeInvalidCatalog},
		{name: "unsupported platform", change: func(s *BootSelection) { s.Disks[0].Platform = "darwin/arm64" }, code: e.CodeInvalidCatalog},
		{name: "missing architecture", change: func(s *BootSelection) { s.Imagers = s.Imagers[:1] }, code: e.CodeInvalidCatalog},
		{name: "unused imager", change: func(s *BootSelection) { s.Disks = s.Disks[:1] }, code: e.CodeInvalidCatalog},
		{name: "duplicate architecture", change: func(s *BootSelection) { s.Imagers[1].Platform = s.Imagers[0].Platform }, code: e.CodeInvalidCatalog},
		{name: "same named imager", change: func(s *BootSelection) { s.Imagers[1].Output = s.Imagers[0].Output }, code: e.CodeInvalidCatalog},
		{name: "disk as imager", change: func(s *BootSelection) { s.Imagers[0].Output = s.Disks[0].Output }, code: e.CodeInvalidCatalog},
		{name: "member traversal", change: func(s *BootSelection) { s.Imagers[0].Member = "../qcow-imager.efi" }, code: e.CodeInvalidCatalog},
		{name: "missing profile", change: func(s *BootSelection) { s.Imagers[0].Profile = "" }, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := bootSelectionFixture()
			if tc.change != nil {
				tc.change(&selection)
			}
			err := selection.Validate()
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBootSelectionJoinsNamedDefinitionOutputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]DefinitionOutput) []DefinitionOutput
	}{
		{name: "complete"},
		{name: "missing selected", change: func(o []DefinitionOutput) []DefinitionOutput { return o[1:] }},
		{name: "wrong disk format", change: func(o []DefinitionOutput) []DefinitionOutput { o[0].Format = "oci"; return o }},
		{name: "wrong imager format", change: func(o []DefinitionOutput) []DefinitionOutput { o[2].Format = "bundle-oci"; return o }},
		{name: "wrong architecture", change: func(o []DefinitionOutput) []DefinitionOutput { o[2].Platform = "linux/arm64"; return o }},
		{name: "aggregate imager", change: func(o []DefinitionOutput) []DefinitionOutput { o[2].Platforms = []string{"linux/amd64"}; return o }},
		{name: "unknown imager member", change: func(o []DefinitionOutput) []DefinitionOutput { o[2].Components = []string{"other"}; return o }},
		{name: "ambiguous output", change: func(o []DefinitionOutput) []DefinitionOutput { return append(o, o[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputs := []DefinitionOutput{
				{Name: "node-amd64", Format: "disk-image", Platform: "linux/amd64"},
				{Name: "node-arm64", Format: "disk-image", Platform: "linux/arm64"},
				{Name: "imager-amd64", Format: "executable", Platform: "linux/amd64", Components: []string{"qcow-imager.efi"}},
				{Name: "imager-arm64", Format: "executable", Platform: "linux/arm64", Components: []string{"qcow-imager.efi"}},
				{Name: "unselected-cli", Format: "executable", Platform: "linux/amd64"},
			}
			if tc.change != nil {
				outputs = tc.change(outputs)
			}
			err := bootSelectionFixture().ValidateOutputs(outputs)
			if tc.change == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
}

func TestBootSelectionCloneOwnsSelection(t *testing.T) {
	original := bootSelectionFixture()
	copy := original.Clone()
	original.Disks[0].Output = "substituted"
	original.Imagers[0].Member = "other.efi"
	if copy.Disks[0].Output != "node-amd64" || copy.Imagers[0].Member != "qcow-imager.efi" {
		t.Fatal("selection clone retained caller-owned membership")
	}
}
