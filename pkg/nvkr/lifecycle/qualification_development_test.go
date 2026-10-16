// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func TestDevelopmentQualificationReferenceScope(t *testing.T) {
	origin := qualificationorigin.Description{Repository: "example/nvkr", RunID: "123", Attempt: "2"}
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, ref string
		code      e.Code
	}{
		{"exact", "ghcr.io/example/nvkr/development/runs/123/attempts/2/releases" + digest, ""},
		{"production namespace", "ghcr.io/example/nvkr/releases" + digest, e.CodeEngineCapability},
		{"foreign repository", "ghcr.io/other/nvkr/development/runs/123/attempts/2/releases" + digest, e.CodeEngineCapability},
		{"other attempt", "ghcr.io/example/nvkr/development/runs/123/attempts/1/releases" + digest, e.CodeVerifyFailed},
		{"other run", "ghcr.io/example/nvkr/development/runs/124/attempts/2/releases" + digest, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admitDevelopmentQualificationReference(tc.ref, origin)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, tc.code)
			}
		})
	}
}
