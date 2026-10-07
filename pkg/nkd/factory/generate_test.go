// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGenerationEngineAdmission(t *testing.T) {
	for _, test := range []struct {
		engine string
		code   e.Code
	}{
		{EngineGitHub, ""},
		{" GitHub ", ""},
		{EngineGitLab, e.CodeEngineCapability},
		{EngineTekton, e.CodeEngineCapability},
		{EngineArgo, e.CodeEngineCapability},
		{"local", e.CodeUsage},
		{"jenkins", e.CodeUsage},
	} {
		t.Run(test.engine, func(t *testing.T) {
			err := ValidateGenerationEngine(test.engine)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, test.code)
			bundle, err := Render(t.Context(), nil, test.engine)
			assertFactoryErrorCode(t, err, test.code)
			if bundle != nil {
				t.Fatal("unsupported engine returned partial output")
			}
		})
	}
}
