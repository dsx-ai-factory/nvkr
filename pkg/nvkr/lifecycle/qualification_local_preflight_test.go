// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"runtime"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestLocalQualificationPreflightAdmission(t *testing.T) {
	for _, ctx := range []context.Context{nil, t.Context()} {
		assertCode(t, PreflightLocalQualification(ctx, nil, LocalQualificationOptions{}), e.CodeUsage)
	}
	if runtime.GOOS != "linux" {
		options := LocalQualificationOptions{PolicyPath: "missing", BuilderKeyPath: "missing", SmokeKeyPath: "missing", QualifierKeyPath: "missing", CosignExecutable: "missing", Repository: "missing", Scratch: "missing", EvidenceOut: "missing"}
		assertCode(t, PreflightLocalQualification(t.Context(), &factory.Plan{}, options), e.CodeEngineCapability)
	}
}
