// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func TestSDKReportingAdmissionAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx    context.Context
		format string
		want   e.Code
	}{{ctx, "json", e.CodeTimeout}, {t.Context(), "invalid", e.CodeUsage}, {nil, "json", e.CodeTimeout}} {
		out := filepath.Join(t.TempDir(), "report.json")
		result, err := client.New().ReportEvidence(tc.ctx, "/absent", tc.format, out, "", "", testobservation.RegistryOptions{})
		if result != nil {
			t.Fatal("SDK reporting returned result before admission")
		}
		requireLifecycleCode(t, err, tc.want)
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatal("SDK reporting admission wrote output")
		}
	}
}
