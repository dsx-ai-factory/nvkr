// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/rootfs"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeuse"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// withConsumerRoot is private to the pipeline's concrete consumer adapters.
// Neither a layout locator nor a caller-supplied command can enter this path.
// The original Run receipt owns cancellation; the production lock keeps the
// layer alive until staging, consumer work and cleanup have all joined.
func (tool *ProducedRuntime) withConsumerRoot(ctx context.Context, use *runtimeuse.Receipt,
	subject string, node ir.Node, scratch string, consume func(context.Context, *os.File) error,
) error {
	if err := requireRunContext(ctx, "runtime consumption"); err != nil {
		return err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return e.New(e.CodeEngineCapability, "runtime consumption requires its admitted deadline")
	}
	if consume == nil {
		return e.New(e.CodeUsage, "runtime consumption requires its concrete adapter")
	}
	if use == nil {
		return e.New(e.CodeGateUnmet, "runtime consumption requires its original Run use receipt")
	}
	binding, err := tool.BindingContext(ctx)
	if err != nil {
		return err
	}
	return use.Execute(ctx, subject, node, binding, func(callCtx context.Context) (returnErr error) {
		state, err := tool.lock(callCtx)
		if err != nil {
			return err
		}
		defer state.mu.Unlock()
		if state.binding != binding {
			return e.New(e.CodeVerifyFailed, "runtime consumption changed its original production")
		}
		staged, err := rootfs.Stage(callCtx, state.layer, scratch)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, staged.Close()) }()
		root, err := staged.Directory()
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
		if err := consume(callCtx, root); err != nil {
			return err
		}
		return state.require(callCtx)
	})
}
