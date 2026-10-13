// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"errors"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestSuiteQualificationFacadeAdmissionBeforeConfigurationIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		sdk  *client.Client
		ctx  context.Context
		code e.Code
	}{
		{"nil client", nil, t.Context(), e.CodeUsage},
		{"nil context", client.New(), nil, e.CodeTimeout},
		{"canceled context", client.New(), ctx, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.sdk.QualifyTriggeredWithSuites(tc.ctx, "/not-opened/native-policy.yaml", "/not-opened/private-policy.json", lifecycle.QualificationRuntimeOptions{})
			var coded *e.Error
			if result != nil || !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("configuration accessed before admission: result=%v error=%v", result, err)
			}
		})
	}
}
