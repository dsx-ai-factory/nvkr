// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// WithDiagnosticOperation borrows this live receipt for bounded diagnostic work.
// It supplies a defensive description and a context canceled by request
// cancellation or receipt Close. Close waits until the callback and its cleanup
// have joined. The callback must own its blocking work and honor that context;
// independently bounded deferred cleanup may finish before it returns.
//
// The callback MUST NOT call Close on this receipt or any shallow copy: Close
// waits for this operation, so doing so would deadlock. The caller owns receipt
// closure outside this callback. Descriptions remain diagnostic data; this
// operation grants no payload access, execution, qualification or release
// authority. Coded callback errors are returned unchanged; uncoded failures are
// wrapped with CodeVerifyFailed while preserving their cause. An externally borrowed
// source is checked at registration and completion; its separate revocation does
// not asynchronously cancel the callback context.
func (receipt *ArtifactCheckReceipt) WithDiagnosticOperation(ctx context.Context, observe func(context.Context, ArtifactCheckDescription) error) error {
	if observe == nil {
		return e.New(e.CodeUsage, "artifact diagnostic callback is required")
	}
	operation, done, err := receipt.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer done()
	description, err := receipt.Description()
	if err != nil {
		return err
	}
	if err := operation.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "artifact diagnostic operation canceled before observation")
	}
	if err := observe(operation, description); err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return err
		}
		return e.Wrap(e.CodeVerifyFailed, err, "artifact diagnostic operation failed")
	}
	if err := operation.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "artifact diagnostic operation canceled")
	}
	// A callback ignoring cancellation cannot turn a revoked receipt/source into
	// successful diagnostic completion. The operation remains registered here.
	_, err = receipt.Description()
	return err
}
