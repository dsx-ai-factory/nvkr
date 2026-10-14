// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pipeline

import (
	"context"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianExecutorRuntimeRequiresNativeLinuxBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want e.Code
	}{
		{"nil context", nil, e.CodeUsage},
		{"unsupported host", t.Context(), e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := (&DebianProducerExecutor{}).admitRuntime(tc.ctx, "/absent/runtime"); code(err) != tc.want {
				t.Fatal("unsupported runtime reached path work", err)
			}
		})
	}
}
