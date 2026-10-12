// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestReferenceInvocationRetryScope(t *testing.T) {
	const base = "https://github.com/example/nvkr/actions/runs/12345/attempts/"
	for _, tc := range []struct {
		name, candidate, current string
		valid                    bool
	}{
		{"same", base + "2", base + "2", true},
		{"earlier", base + "1", base + "3", true},
		{"future", base + "3", base + "2", false},
		{"other run", "https://github.com/example/nvkr/actions/runs/12346/attempts/1", base + "2", false},
		{"other repository", "https://github.com/example/other/actions/runs/12345/attempts/1", base + "2", false},
		{"other server", "https://other.test/example/nvkr/actions/runs/12345/attempts/1", base + "2", false},
		{"query", base + "1?x=1", base + "2", false},
		{"fragment", base + "1#x", base + "2", false},
		{"escape", base + "%31", base + "2", false},
		{"leading zero", base + "01", base + "2", false},
		{"zero", base + "0", base + "2", false},
		{"negative", base + "-1", base + "2", false},
		{"overflow", base + "18446744073709551616", base + "2", false},
		{"suffix", base + "1/extra", base + "2", false},
		{"local", "local:run", "local:run", false},
		{"invalid current", base + "1", base + "02", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admitReferenceInvocation(tc.candidate, tc.current)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}
