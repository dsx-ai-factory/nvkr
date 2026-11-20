// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestMacPackerPrerequisitesFollowCatalogSelection(t *testing.T) {
	for _, tc := range []struct {
		name, tag, member, platform, version, executable string
		reject                                           bool
	}{
		{name: "current", tag: "v1.15.4", member: "packer-plugin-qemu_v1.1.6_x5.0_darwin_arm64", platform: "darwin/arm64", version: "1.15.4", executable: "packer-plugin-qemu_v1.1.6_x5.0_darwin_arm64"},
		{name: "independent selection", tag: "v2.3.4", member: "bin/plugin-selected-by-catalog", platform: "darwin/arm64", version: "2.3.4", executable: "plugin-selected-by-catalog"},
		{name: "missing Packer pin", member: "plugin", platform: "darwin/arm64", reject: true},
		{name: "missing plugin member", tag: "v2.3.4", platform: "darwin/arm64", reject: true},
		{name: "wrong plugin platform", tag: "v2.3.4", member: "plugin", platform: "linux/arm64", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version, member, err := macPackerPrerequisiteSelection(catalog.Component{Tag: tc.tag}, catalog.ToolPin{ExecutablePath: tc.member, Platform: tc.platform})
			if (err != nil) != tc.reject {
				t.Fatalf("selection=%q/%q error=%v", version, member, err)
			}
			if tc.reject {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || version != "" || member != "" {
					t.Fatalf("uncoded or partial refusal: %q/%q %v", version, member, err)
				}
			}
			if !tc.reject && (version != tc.version || member != tc.executable) {
				t.Fatalf("catalog selection changed: %q/%q", version, member)
			}
		})
	}
}
