// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSDKValidationReadinessBeforeAdapterWork(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		sdk  *client.Client
		ctx  context.Context
		code e.Code
	}{
		{"nil client", nil, t.Context(), e.CodeUsage},
		{"invalid configuration", client.New(nil), t.Context(), e.CodeUsage},
		{"nil context", client.New(), nil, e.CodeTimeout},
		{"canceled", client.New(), canceled, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.sdk.Validate(tc.ctx, "", "/not-opened/config", client.ValidationOptions{WorkspaceRoot: "/not-created/workspace"})
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code || len(result) != 0 {
				t.Fatalf("admission changed: %+v %v", result, err)
			}
			if tc.name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
		})
	}
}

func TestSDKValidatorMethodsRefuseMissingConfiguration(t *testing.T) {
	sdk := client.New()
	for _, tc := range []struct {
		name   string
		invoke func(*testing.T, string) error
	}{
		{"list", func(t *testing.T, path string) error {
			result, err := sdk.ListValidators(t.Context(), path)
			if len(result) != 0 {
				t.Fatal(result)
			}
			return err
		}},
		{"describe", func(t *testing.T, path string) error {
			result, err := sdk.DescribeValidator(t.Context(), path, "nvke/nvkectl/contract")
			if result.Available {
				t.Fatal(result)
			}
			return err
		}},
		{"prepare", func(t *testing.T, path string) error {
			result, err := sdk.PrepareValidatorProfile(t.Context(), path, client.ValidatorRuntime{})
			if result != nil {
				t.Fatal("returned an invocation on refusal")
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.invoke(t, filepath.Join(t.TempDir(), "missing.yaml"))
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("uncoded or unexpected refusal: %v", err)
			}
		})
	}
}
