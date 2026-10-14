// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianRunProducerBridgeRejectsForeignRunBeforeWork(t *testing.T) {
	plan, result, _ := debianRunOwnerFixture(t)
	foreign := &LocalRunResult{SchemaVersion: result.SchemaVersion, PlanDigest: result.PlanDigest, Qualification: result.Qualification}
	if err := result.debian.executeBoundProducer(t.Context(), foreign, plan, plan.LifecycleOutputs[0], "/missing", nil, nil); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatal("foreign description reached producer admission", err)
	}
	if len(result.debian.executors) != 0 || len(result.debian.completions) != 0 {
		t.Fatal("foreign Run retained partial output")
	}
}
