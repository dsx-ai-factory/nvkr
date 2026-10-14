// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestDebianRunContentRequiresOriginalCompletion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inspector *sbom.DiskInspectionRuntime
		want      e.Code
	}{
		{"missing inspector", nil, e.CodeEngineCapability},
		{"missing completion", &sbom.DiskInspectionRuntime{}, e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, result, _ := debianRunOwnerFixture(t)
			document, err := result.debian.inspectBoundProducer(t.Context(), result, plan, plan.LifecycleOutputs[0], tc.inspector)
			if document != nil || factoryErrorCode(err) != tc.want || len(result.debian.content) != 0 {
				t.Fatal("description restored inspected original content", err)
			}
		})
	}
}
