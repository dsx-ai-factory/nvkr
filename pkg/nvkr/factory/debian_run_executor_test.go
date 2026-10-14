// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Public issuance remains closed. These rejection tests do not construct an
// executor or claim a positive factory producer/evidence run.
func TestDebianRunExecutorBindingRejectsDescriptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		executor *pipeline.DebianProducerExecutor
		want     e.Code
	}{
		{"missing executor", nil, e.CodeProvenanceMissing},
		{"decoded executor", &pipeline.DebianProducerExecutor{}, e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, result, _ := debianRunOwnerFixture(t)
			if err := result.debian.bindExecutor(t.Context(), result, plan, plan.LifecycleOutputs[0], tc.executor); factoryErrorCode(err) != tc.want {
				t.Fatal("executor description bound original Run", err)
			}
			if len(result.debian.executors) != 0 {
				t.Fatal("failed binding installed an executor")
			}
		})
	}
}
