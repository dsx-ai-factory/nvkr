// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianProducerBridgeRequiresOriginalOwnersBeforeWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for _, tc := range []struct {
		name     string
		executor *DebianProducerExecutor
		binding  *DebianProducerBinding
		want     e.Code
	}{
		{"missing", nil, nil, e.CodeProvenanceMissing},
		{"decoded executor", &DebianProducerExecutor{}, &DebianProducerBinding{}, e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if a, r, err := tc.executor.RunBoundContext(ctx, tc.binding, DebianProducerRun{SourceRoot: "/missing"}); a != nil || r != nil || code(err) != tc.want {
				t.Fatal("descriptions restored bound execution", err)
			}
		})
	}
	_, executor, instruction, _ := debianBindingFixture(t)
	var binding *DebianProducerBinding
	if err := executor.BindSelectionContext(ctx, "artifact/a", instruction, func(_ context.Context, b *DebianProducerBinding) error { binding = b; return nil }); err != nil {
		t.Fatal(err)
	}
	if a, r, err := executor.RunBoundContext(ctx, binding, DebianProducerRun{SourceRoot: "/missing"}); a != nil || r != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("private fixture runtime entered public bridge", err)
	}
	if a, r, err := executor.RunBoundContext(t.Context(), binding, DebianProducerRun{SourceRoot: "/missing"}); a != nil || r != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("unbounded request admitted", err)
	}
}
