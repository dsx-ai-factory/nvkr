// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Register the whole selected operation, including runtime observation and
// post-cleanup revalidation, with the frozen original session lifetime.
func (executor *DebianProducerExecutor) begin(ctx context.Context) (context.Context, func(), error) {
	if err := requireRunContext(ctx, "original Debian executor"); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "using original Debian executor")
	}
	if executor == nil || executor.state == nil || executor.state.session == nil {
		return nil, nil, e.New(e.CodeProvenanceMissing, "original Debian executor is required")
	}
	session := executor.state.session
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.ctx.Err() != nil {
		return nil, nil, e.New(e.CodeEngineCapability, "original Debian session is closed or canceled")
	}
	owned, cancel := context.WithCancel(ctx)
	session.calls[owned] = cancel
	session.active.Add(1)
	stop := context.AfterFunc(session.ctx, cancel)
	return owned, func() {
		stop()
		cancel()
		session.mu.Lock()
		delete(session.calls, owned)
		session.mu.Unlock()
		session.active.Done()
	}, nil
}

// Private native runtime admission reuses the existing held executable, daemon
// identity and image-inspection owners. It admits no source or output work, no
// qualification and no public factory execution.
func (executor *DebianProducerExecutor) admitRuntime(ctx context.Context, runtimeName string) error {
	if err := requireRunContext(ctx, "Debian runtime admission"); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "native Debian runtime admission requires Linux")
	}
	if _, ok := ctx.Deadline(); !ok {
		return e.New(e.CodeEngineCapability, "Debian runtime admission requires a bounded context")
	}
	owned, finish, err := executor.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	executor.state.mu.Lock()
	subjects := make([]string, 0, len(executor.state.nodes))
	for subject := range executor.state.nodes {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	nodes := make([][]byte, 0, len(subjects))
	for _, subject := range subjects {
		nodes = append(nodes, executor.state.nodes[subject])
	}
	executor.state.mu.Unlock()
	for _, raw := range nodes {
		var node ir.Node
		if err := canonical.UnmarshalExact(raw, &node); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "reading selected Debian instruction")
		}
		if node.Platform != "linux/"+runtime.GOARCH {
			return e.New(e.CodeEngineCapability, "native Debian toolchain requires its selected host architecture")
		}
	}
	path, err := resolveExecutionRuntime(runtimeName)
	if err != nil {
		return err
	}
	if name := filepath.Base(path); name != "docker" && name != "podman" {
		return e.New(e.CodeEngineCapability, "Debian runtime requires the explicit Docker or Podman adapter")
	}
	held, err := executor.state.session.admitPackerRuntime(owned, path)
	if err != nil {
		return err
	}
	adapter := heldPackerContainerRuntime{runtime: held}
	if err := observePackerRuntimeFreshness(owned, adapter); err != nil {
		return err
	}
	for _, raw := range nodes {
		var node ir.Node
		if err := canonical.UnmarshalExact(raw, &node); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "reading selected Debian instruction")
		}
		if _, err := observeLocalContainerImage(owned, adapter, node.Toolchain, node.Platform, "selected Debian toolchain"); err != nil {
			return err
		}
	}
	if err := owned.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "completing Debian runtime admission")
	}
	executor.state.mu.Lock()
	executor.state.runtime = held
	executor.state.mu.Unlock()
	return nil
}

func revalidateDebianRuntime(ctx context.Context, held *heldRuntimeExecutable, node ir.Node) error {
	if held == nil {
		return nil
	} // Existing private fixture path has no runtime authority.
	if _, ok := ctx.Deadline(); !ok {
		return e.New(e.CodeEngineCapability, "held Debian production requires a bounded context")
	}
	adapter := heldPackerContainerRuntime{runtime: held}
	if err := observePackerRuntimeFreshness(ctx, adapter); err != nil {
		return err
	}
	_, err := observeLocalContainerImage(ctx, adapter, node.Toolchain, node.Platform, "selected Debian toolchain")
	return err
}
