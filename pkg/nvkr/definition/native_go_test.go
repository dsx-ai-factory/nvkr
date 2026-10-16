// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"strings"
	"testing"
)

func TestNativeGoLocationIsCompleteAndCannotMixBundleForm(t *testing.T) {
	for _, scenario := range []string{"exact", "missing SDK", "missing compiler", "relative manifest", "mixed bundle", "unbound identity"} {
		t.Run(scenario, func(t *testing.T) {
			identity := "sha256:" + strings.Repeat("a", 64)
			location := NativeToolchain{GoManifest: "/private/manifest.json", GoRoot: "/installed/go", Compiler: "/usr/bin/clang"}
			switch scenario {
			case "missing SDK":
				location.GoRoot = ""
			case "missing compiler":
				location.Compiler = ""
			case "relative manifest":
				location.GoManifest = "manifest.json"
			case "mixed bundle":
				location.Layout = "bundle"
				location.Bundle = "tools"
			case "unbound identity":
				identity = "latest"
			}
			err := (Runtime{NativeToolchains: map[string]NativeToolchain{identity: location}}).validate()
			if (err == nil) != (scenario == "exact") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}
