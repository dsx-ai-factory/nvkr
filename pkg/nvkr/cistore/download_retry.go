// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

const maxChunkReadAttempts = 3

// Retry only extraction into the caller's private staging tree. Each attempt
// opens fresh response bodies from the already authenticated layers, starts at
// byte zero and repeats every chunk and whole-archive check. No final destination
// projection or rollback runs inside this loop. The original operation context
// bounds all attempts and waits; a retry never obtains a fresh deadline.
//
// The private wait/cleanup seams permit focused lifetime contracts without
// replacing the extraction or authentication owners.
func unpackChunkArchive(ctx context.Context, layers []v1.Layer, manifest *v1.Manifest, locator Locator, restored string,
	wait func(context.Context, time.Duration) error, cleanup func(string) error,
) error {
	return unpackChunkAttempts(ctx, layers, manifest, restored, func(source *chunkReader) error {
		return transportarchive.UnpackStream(ctx, source, locator.ArchiveSize, locator.ArchiveDigest, restored, locator.Paths)
	}, wait, cleanup)
}

func unpackChunkAttempts(ctx context.Context, layers []v1.Layer, manifest *v1.Manifest, restored string,
	unpack func(*chunkReader) error, wait func(context.Context, time.Duration) error, cleanup func(string) error,
) error {
	for attempt := 1; attempt <= maxChunkReadAttempts; attempt++ {
		if ctx.Err() != nil {
			return safeIOError(ctx, e.CodeTimeout, "private CI archive read canceled")
		}
		stream := newChunkReader(ctx, layers, manifest.Layers)
		err := unpack(stream)
		closeErr := stream.Close()
		if err == nil && closeErr == nil {
			return nil
		}
		// A reader may yield bytes and an error together. Those bytes can also
		// cause an independent TAR, path or filesystem rejection; only the
		// extraction error's exact stream cause may authorize another attempt.
		if exactChunkStreamCause(err, stream.err) {
			err = stream.err
		}
		if closeErr != nil {
			err = closeErr
		}
		// UnpackStream may have created directories or a partial inner carrier.
		// Its own best-effort cleanup is insufficient for a retry: remove the
		// complete owned attempt before opening another network response.
		if cleanupErr := cleanup(restored); cleanupErr != nil {
			return safeIOError(ctx, e.CodeImmutableViolation, "removing failed private CI archive staging")
		}
		if ctx.Err() != nil {
			return safeIOError(ctx, e.CodeTimeout, "private CI archive read canceled")
		}
		if !isRetryableChunkRead(err) {
			return err
		}
		if attempt == maxChunkReadAttempts {
			var coded *e.Error
			if !errors.As(err, &coded) {
				return e.New(e.CodeInternal, "private CI read retry lacks a coded failure")
			}
			return e.Wrap(coded.Code, err, "private CI archive read failed after %d attempts", attempt)
		}
		if err := wait(ctx, time.Duration(attempt)*time.Second); err != nil {
			return safeIOError(ctx, e.CodeTimeout, "waiting to retry private CI archive read")
		}
	}
	return e.New(e.CodeInternal, "private CI archive read exceeded its attempt bound")
}

// Extraction adds verification context around read failures. Only that bounded
// single-cause chain may expose the exact source-issued failure: errors.Is would
// also match a joined independent failure or caller-defined equality. Other coded
// boundaries and filesystem/TLS wrappers retain their own nonretryable failure.
func exactChunkStreamCause(err, sourceErr error) bool {
	if sourceErr == nil {
		return false
	}
	for depth := 0; depth < maxChunkDiagnosticNodes && err != nil; depth++ {
		if err == sourceErr {
			return true
		}
		switch typed := err.(type) {
		case *e.Error:
			if typed.Code != e.CodeVerifyFailed {
				return false
			}
		case *tls.CertificateVerificationError, *os.PathError, *os.LinkError:
			return false
		}
		if _, joined := err.(interface{ Unwrap() []error }); joined {
			return false
		}
		wrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}

func waitChunkRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
