// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package packertransport

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateVariablesSharesOneBoundedArgvNamespace(t *testing.T) {
	validReserved := []string{"architecture", "material_manifest", "output_directory"}
	for _, test := range []struct {
		name     string
		reserved []string
		public   []Variable
		wantText string
	}{
		{
			name: "valid including empty value", reserved: validReserved,
			public: []Variable{{Name: "image_variant", Value: ""}},
		},
		{
			name: "delimiter in reserved name", reserved: []string{"architecture=other"},
			wantText: "reserved variable name",
		},
		{
			name: "duplicate reserved name", reserved: []string{"architecture", "architecture"},
			wantText: "is duplicated",
		},
		{
			name: "delimiter in public name", reserved: validReserved,
			public:   []Variable{{Name: "image_variant=other", Value: "value"}},
			wantText: "public variable name",
		},
		{
			name: "display control in public name", reserved: validReserved,
			public:   []Variable{{Name: "image\u202evariant", Value: "value"}},
			wantText: "public variable name",
		},
		{
			name: "display control in public value", reserved: validReserved,
			public:   []Variable{{Name: "image_variant", Value: "safe\u200bunsafe"}},
			wantText: "value that is not transport-safe",
		},
		{
			name: "reserved collision", reserved: validReserved,
			public:   []Variable{{Name: "architecture", Value: "arm64"}},
			wantText: "duplicates another binding",
		},
		{
			name: "duplicate public name", reserved: validReserved,
			public: []Variable{
				{Name: "image_variant", Value: "standard"},
				{Name: "image_variant", Value: "fde"},
			},
			wantText: "duplicates another binding",
		},
		{
			name: "too many public variables", reserved: validReserved,
			public:   variableSet(MaxPublicVariables+1, 0),
			wantText: "variable bound",
		},
		{
			name: "aggregate public argv too large", reserved: validReserved,
			public:   variableSet(17, MaxVariableValueBytes),
			wantText: "argv bound",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateVariables(test.reserved, test.public)
			if test.wantText == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func variableSet(count, valueBytes int) []Variable {
	variables := make([]Variable, count)
	for index := range variables {
		variables[index] = Variable{
			Name:  fmt.Sprintf("variable_%d", index),
			Value: strings.Repeat("x", valueBytes),
		}
	}
	return variables
}
