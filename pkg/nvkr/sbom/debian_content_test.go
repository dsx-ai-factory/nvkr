// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianPackageContentRequiresLiveRuntimeBeforeInput(t *testing.T) {
	for _, inspection := range []*DiskInspectionRuntime{nil, {}} {
		result, err := inspection.InspectDebianPackageContent(t.Context(), "absent-package", "sha256:"+strings.Repeat("a", 64), 1, "linux/amd64")
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || result != nil {
			t.Fatalf("unadmitted runtime reached package I/O: %v, %v", result, err)
		}
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{name: "nil context", code: e.CodeUsage},
		{name: "canceled", code: e.CodeTimeout, ctx: canceledDebianContentContext(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inspection *DiskInspectionRuntime
			result, err := inspection.InspectDebianPackageContent(tc.ctx, "absent-package", "sha256:"+strings.Repeat("a", 64), 1, "linux/amd64")
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code || result != nil {
				t.Fatalf("context admission = %v, %v; want %s", result, err, tc.code)
			}
		})
	}
}

func canceledDebianContentContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}
