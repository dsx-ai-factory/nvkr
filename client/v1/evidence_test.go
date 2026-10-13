// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestEvidenceSDKAdmissionPrecedesFilesystemWork(t *testing.T) {
	for _, operation := range []struct {
		name string
		call func(context.Context, *client.Client) error
	}{
		{"emit", func(ctx context.Context, c *client.Client) error {
			_, err := c.EmitEvidence(ctx, client.EmitEvidenceRequest{Root: "missing", DescriptionPath: "missing", SelectionPath: "missing", OutputPath: "missing"})
			return err
		}},
		{"verify-local", func(ctx context.Context, c *client.Client) error {
			_, err := c.VerifyLocalEvidence(ctx, "missing", "missing")
			return err
		}},
		{"verify-locator", func(ctx context.Context, c *client.Client) error {
			_, err := c.VerifyEvidenceFiles(ctx, client.VerifyEvidenceRequest{LocatorPath: "missing", ExpectedPath: "missing"}, client.EvidenceRegistryOptions{})
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(t.Context(), nil); errorCode(err) != e.CodeUsage {
				t.Fatalf("nil client = %v, want usage before filesystem work", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := operation.call(ctx, client.New()); errorCode(err) != e.CodeTimeout {
				t.Fatalf("canceled client = %v, want timeout before filesystem work", err)
			}
		})
	}
}
