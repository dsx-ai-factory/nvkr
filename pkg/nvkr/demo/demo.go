// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package demo exposes the downstream diagnostic site/tenant owner to Go tests.
// It is separate from the release client so normal NVKR consumers do not import
// VM deployment or SPIFFE runtime dependencies.
package demo

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

// Config, RunInputs and Resources are provisional
// downstream diagnostic descriptions. Paths and resource choices confer no
// artifact, readiness, Release or qualification authority. They deliberately
// alias the existing owner rather than introducing another configuration schema.
type Config = nvkedemo.Config
type RunInputs = nvkedemo.RunInputs
type Resources = nvkedemo.NativeResources

// Site and Tenant are callback-scoped handles issued by the native
// diagnostic owner. Zero values and escaped handles cannot operate resources.
// Their kubeconfig paths are private access locators, not readiness receipts.
type Site = nvkedemo.NativeSite
type Tenant = nvkedemo.NativeTenant

// WithSite admits completed Runs and owns one native diagnostic site for
// use. Its methods create/delete a tenant and explicitly run conformance against
// that same live tenant. Callback return, failure, cancellation or test fatal
// exit invokes joined owned cleanup; unjoined work preserves dependent resources.
//
// This provisional downstream API is separate from the public NVKR lifecycle.
// It requires the existing matching nvkr and nvke-demo helper executables and
// native host prerequisites; it does not build, acquire sources, install host
// dependencies, detach ownership or grant qualification authority.
func WithSite(ctx context.Context, cfg Config, use func(context.Context, *Site) error) error {
	return nvkedemo.WithNativeSite(ctx, cfg, use)
}
