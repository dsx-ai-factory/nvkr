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
		name   string
		output string
		tool   string
		code   e.Code
	}{
		{"disk output", "node-image", "vm-runtime-amd64", ""},
		{"platform instead of output", "linux/amd64", "runtime", e.CodeInvalidCatalog},
		{"empty output", "", "runtime", e.CodeInvalidCatalog},
		{"path output", "../image", "runtime", e.CodeInvalidCatalog},
		{"empty tool", "node-image", "", e.CodeInvalidCatalog},
		{"path tool", "node-image", "../runtime", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := Environment{Runtime: Runtime{ProducedRuntimes: map[string]string{tc.output: tc.tool}}}
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
			copy.Runtime.ProducedRuntimes[tc.output] = "other"
			if input.Runtime.ProducedRuntimes[tc.output] != tc.tool {
				t.Fatal("Environment clone aliases runtime selection")
			}
		})
	}
}

func TestProducedRuntimeEnvironmentRejectsSharedRequirement(t *testing.T) {
	input := Runtime{ProducedRuntimes: map[string]string{"first": "runtime", "second": "runtime"}}
	var coded *e.Error
	if err := input.validate(); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("shared runtime requirement = %v; want %s", err, e.CodeEngineCapability)
	}
}
