// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestUtilityContainerPlatformAdmission(t *testing.T) {
	image := "busybox@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		pins     map[string]string
		platform string
		code     e.Code
	}{
		{"legacy", nil, "linux/arm64", ""},
		{"native", map[string]string{"linux/amd64": image, "linux/arm64": image}, "linux/arm64", ""},
		{"missing", map[string]string{"linux/amd64": image}, "linux/arm64", e.CodeEngineCapability},
		{"empty", map[string]string{}, "linux/arm64", e.CodeInvalidCatalog},
		{"default differs", map[string]string{"linux/amd64": "busybox@sha256:" + strings.Repeat("b", 64)}, "linux/amd64", e.CodeInvalidCatalog},
		{"tag", map[string]string{"linux/amd64": image, "linux/arm64": "busybox:latest"}, "linux/arm64", e.CodeUnpinnedSource},
		{"unsupported", map[string]string{"linux/amd64": image, "darwin/arm64": image}, "darwin/arm64", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := Environment{UtilityContainer: image, UtilityPlatform: "linux/amd64", UtilityContainers: tc.pins}
			err := env.validateUtilityContainers()
			var platform, container string
			if err == nil {
				container, platform, err = env.OutputUtility(tc.platform)
			}
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := tc.platform
			if tc.pins == nil {
				want = "linux/amd64"
			}
			if container != image || platform != want {
				t.Fatalf("utility = %s/%s", container, platform)
			}
			clone := env.Clone()
			if clone.UtilityContainers != nil {
				clone.UtilityContainers["linux/amd64"] = "changed"
				if env.UtilityContainers["linux/amd64"] != image {
					t.Fatal("clone aliases utility map")
				}
			}
		})
	}
}
