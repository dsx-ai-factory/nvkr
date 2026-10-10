// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"context"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const highWaterSchemaVersion = "nvkr.dev/status-high-water/v1"

type highWaterFile struct {
	SchemaVersion    string `json:"schemaVersion"`
	Target           string `json:"target"`
	Authority        string `json:"authority"`
	Sequence         uint64 `json:"sequence"`
	CheckpointDigest string `json:"checkpointDigest"`
}

func highWaterFromReceipt(receipt CheckpointReceipt) (highWaterFile, error) {
	target, err := receipt.Target()
	if err != nil {
		return highWaterFile{}, err
	}
	authority, err := receipt.Authority()
	if err != nil {
		return highWaterFile{}, err
	}
	sequence, err := receipt.Sequence()
	if err != nil {
		return highWaterFile{}, err
	}
	digest, err := receipt.CheckpointDigest()
	if err != nil {
		return highWaterFile{}, err
	}
	return highWaterFile{SchemaVersion: highWaterSchemaVersion, Target: target,
		Authority: authority, Sequence: sequence, CheckpointDigest: digest}, nil
}

func statusContextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

type statusContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader statusContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}
