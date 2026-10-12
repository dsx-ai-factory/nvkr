// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationDiskScratchCapacityPolicy(t *testing.T) {
	observationFailure := errors.New("injected memory-backed filesystem")
	for _, scenario := range []struct {
		name      string
		available uint64
		required  uint64
		failure   error
		refuse    bool
	}{
		{name: "headroom", available: 11, required: 10},
		{name: "exact fit", available: 10, required: 10},
		{name: "empty payload", available: 0, required: 0},
		{name: "insufficient", available: 9, required: 10, refuse: true},
		{name: "observation failure", failure: observationFailure, refuse: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = directory.Close() }()
			calls := 0
			err = admitPublicationDiskScratchWith(t.Context(), directory, scenario.required,
				func(ctx context.Context, held *os.File) (uint64, error) {
					calls++
					if held != directory || ctx != t.Context() {
						t.Fatal("capacity observation substituted its held directory or context")
					}
					return scenario.available, scenario.failure
				})
			if calls != 1 {
				t.Fatalf("capacity calls=%d, want one", calls)
			}
			if !scenario.refuse {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertRegistryCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), directory.Name()) || !strings.Contains(err.Error(), "TMPDIR") {
				t.Fatalf("refusal omitted consumer scratch location: %v", err)
			}
			if scenario.failure != nil && !errors.Is(err, scenario.failure) {
				t.Fatalf("refusal lost observation failure: %v", err)
			}
		})
	}
}
