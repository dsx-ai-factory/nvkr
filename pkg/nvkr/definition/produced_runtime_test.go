// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestProducedRuntimeEnvironmentSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform string
		tool     string
		code     e.Code
	}{
		{"amd64", "linux/amd64", "vm-runtime-amd64", ""},
		{"arm64 deferred", "linux/arm64", "vm-runtime-arm64", e.CodeEngineCapability},
		{"host alias", "amd64", "runtime", e.CodeEngineCapability},
		{"empty tool", "linux/amd64", "", e.CodeInvalidCatalog},
		{"path tool", "linux/amd64", "../runtime", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := Environment{Runtime: Runtime{ProducedRuntimes: map[string]string{tc.platform: tc.tool}}}
			err := input.Runtime.validate()
			var coded *e.Error
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("validation = %v; want %s", err, tc.code)
			}
			copy := input.Clone()
			copy.Runtime.ProducedRuntimes[tc.platform] = "other"
			if input.Runtime.ProducedRuntimes[tc.platform] != tc.tool {
				t.Fatal("Environment clone aliases runtime selection")
			}
		})
	}
}
