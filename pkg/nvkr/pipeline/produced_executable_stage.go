// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// writeProducedExecutable streams only the sealed snapshot held by the producer
// capability. No host pathname is passed to the daemon or reopened for execution.
func writeProducedExecutable(ctx context.Context, tool *ProducedExecutable, binding ProducedExecutableBinding, destination io.Writer) error {
	current, err := tool.BindingContext(ctx)
	if err != nil {
		return err
	}
	if current != binding {
		return e.New(e.CodeVerifyFailed, "inspection helper changed before streaming")
	}
	state := tool.state
	if err := lockDiskInspection(ctx, &state.mu); err != nil {
		return err
	}
	defer state.mu.Unlock()
	if state.file == nil {
		return e.New(e.CodeVerifyFailed, "inspection helper closed before streaming")
	}
	written, err := state.file.WriteToContext(ctx, destination)
	if err != nil {
		return mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "streaming held inspection helper"), "inspection helper stream")
	}
	if written != binding.Size {
		return e.New(e.CodeVerifyFailed, "inspection helper stream differs from its bound size")
	}
	return nil
}
