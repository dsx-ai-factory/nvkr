// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestFreezeSourceHeadAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name, ref string
		ctx       context.Context
		code      e.Code
	}{
		{"missing context", "refs/heads/main", nil, e.CodeUsage},
		{"canceled", "refs/heads/main", canceled, e.CodeTimeout},
		{"short ref", "main", t.Context(), e.CodeInvalidCatalog},
		{"foreign ref", "refs/heads/development", t.Context(), e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection, err := FreezeSourceHead(tc.ctx, "https://github.com/example/source", tc.ref)
			assertCatalogContractCode(t, err, tc.code)
			if selection != (SourceSelection{}) {
				t.Fatal("failed freeze returned a partial selection")
			}
		})
	}
}
