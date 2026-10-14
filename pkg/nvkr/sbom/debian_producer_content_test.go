// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestDebianProducerContentRequiresAdmittedRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		runtime *DiskInspectionRuntime
		want    e.Code
	}{
		{"nil context", nil, nil, e.CodeUsage},
		{"nil runtime", t.Context(), nil, e.CodeEngineCapability},
		{"zero runtime", t.Context(), &DiskInspectionRuntime{}, e.CodeEngineCapability},
		{"canceled", canceledDebianContentContext(t), nil, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.runtime.InspectDebianProducerContent(tc.ctx, &pipeline.DebianProducerArchive{})
			var coded *e.Error
			if result != nil || !errors.As(err, &coded) || coded.Code != tc.want {
				t.Fatal("unadmitted runtime issued content", err)
			}
		})
	}
}
