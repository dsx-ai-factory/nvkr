// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

// NativeDemoConfig, NativeDemoRunInputs and NativeDemoResources are provisional
// downstream diagnostic descriptions. Paths and resource choices confer no
// artifact, readiness, Release or qualification authority. They deliberately
// alias the existing owner rather than introducing another configuration schema.
type NativeDemoConfig = nvkedemo.Config
type NativeDemoRunInputs = nvkedemo.RunInputs
type NativeDemoResources = nvkedemo.NativeResources

// NativeSite and NativeTenant are callback-scoped handles issued by the native
// diagnostic owner. Zero values and escaped handles cannot operate resources.
// Their kubeconfig paths are private access locators, not readiness receipts.
type NativeSite = nvkedemo.NativeSite
type NativeTenant = nvkedemo.NativeTenant

// WithNativeSite admits completed Runs and owns one native diagnostic site for
// use. Its methods create/delete a tenant and explicitly run conformance against
// that same live tenant. Callback return, failure, cancellation or test fatal
// exit invokes joined owned cleanup; unjoined work preserves dependent resources.
//
// This provisional downstream API is separate from the public NVKR lifecycle.
// It requires the existing matching nvkr and nvke-demo helper executables and
// native host prerequisites; it does not build, acquire sources, install host
// dependencies, detach ownership or grant qualification authority.
func (c *Client) WithNativeSite(ctx context.Context, cfg NativeDemoConfig, use func(context.Context, *NativeSite) error) error {
	if err := c.ready(ctx, "native diagnostic site"); err != nil {
		return err
	}
	return nvkedemo.WithNativeSite(ctx, cfg, use)
}
