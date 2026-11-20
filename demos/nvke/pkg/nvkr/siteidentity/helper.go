// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package siteidentity

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// RunHelper is the private executable's bounded stdio adapter. Its caller owns
// pollable input/output descriptors (the selected kubectl exec stream); unsupported
// descriptors fail before identity or remote work. Direct Go callers should use Run.
// The native caller must treat missing/malformed helper output as unknown remote
// completion, regardless of a local process exit status.
func RunHelper(ctx context.Context, input, output *os.File) (ret Result, resultErr error) {
	ret = Result{SchemaVersion: SchemaVersion, Completion: NotStarted}
	err := admitRuntime(ctx)
	if err == nil {
		if output == nil {
			err = e.New(e.CodeEngineCapability, "identity helper needs an owned output descriptor")
		} else if outputErr := output.SetWriteDeadline(time.Now().Add(2 * time.Second)); outputErr != nil {
			err = e.Wrap(e.CodeEngineCapability, outputErr, "identity helper output must be pollable")
		} else if clearErr := output.SetWriteDeadline(time.Time{}); clearErr != nil {
			err = e.Wrap(e.CodeEngineCapability, clearErr, "identity helper output deadline is unavailable")
		}
	}
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			ret.FailureCode = coded.Code
		}
		return ret, err
	}
	var inputBytes []byte
	inputBytes, err = readInput(ctx, input)
	if err == nil {
		var request Request
		request, err = DecodeRequest(ctx, inputBytes)
		if err == nil {
			ret, err = Run(ctx, request)
		}
	}
	var coded *e.Error
	if err != nil && errors.As(err, &coded) {
		ret.FailureCode = coded.Code
	}
	raw, encodeErr := canonical.Marshal(ret)
	if encodeErr != nil {
		return ret, e.Wrap(e.CodeInternal, errors.Join(err, encodeErr), "encode private identity result")
	}
	if writeErr := output.SetWriteDeadline(time.Now().Add(2 * time.Second)); writeErr != nil {
		return ret, e.Wrap(e.CodeEngineCapability, errors.Join(err, writeErr), "identity helper output must be pollable")
	}
	if _, writeErr := output.Write(append(raw, '\n')); writeErr != nil {
		return ret, e.Wrap(e.CodeUnavailable, errors.Join(err, writeErr), "identity helper result delivery failed")
	}
	return ret, err
}

func readInput(ctx context.Context, input *os.File) ([]byte, error) {
	if input == nil {
		return nil, e.New(e.CodeEngineCapability, "identity helper needs an owned input descriptor")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "read identity request")
	}
	if err := input.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "identity helper input must be pollable")
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = input.SetReadDeadline(time.Now()); close(joined) })
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(input, MaxRequestBytes+1))
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "identity request canceled")
	}
	if err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "identity request read did not complete")
	}
	if len(raw) > MaxRequestBytes {
		return nil, e.New(e.CodeUsage, "identity request exceeds its byte bound")
	}
	return raw, nil
}
