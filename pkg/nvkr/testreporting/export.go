// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const MaxIndexBytes = 16 << 20

// ExportIndex commits a fresh canonical generated index to one new local file.
// Existing outputs are never replaced. Reading the file later establishes no
// signer, receipt lifetime or consumer-policy authority.
func (r *Report) ExportIndex(ctx context.Context, out string) error {
	raw, err := r.JSON(ctx)
	if err != nil {
		return err
	}
	return r.exportLocalFile(ctx, out, raw)
}

// ExportMarkdown commits an immutable local human-readable projection. It grants
// no machine verification or authority beyond the fresh receipts used to render.
func (r *Report) ExportMarkdown(ctx context.Context, out string) error {
	rendered, err := r.Markdown(ctx)
	if err != nil {
		return err
	}
	return r.exportLocalFile(ctx, out, []byte(rendered))
}
func (r *Report) exportLocalFile(ctx context.Context, out string, raw []byte) error {
	return r.exportLocalFileWithCommit(ctx, out, raw, nil)
}
func (r *Report) exportLocalFileWithCommit(ctx context.Context, out string, raw []byte, onCommit func()) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if out == "" {
		return e.New(e.CodeUsage, "explicit generated report output required")
	}

	if len(raw) > MaxIndexBytes {
		return failure("generated discovery index exceeds byte budget")
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "resolving index output")
	}
	parent, parentInfo, err := securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding generated index parent")
	}
	defer parent.Close()
	stage, stageName, err := securepath.CreateTemporarySiblingWithPrefix(parent, ".nvkr-report-")
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "creating generated index stage")
	}
	defer stage.Close()
	committed := false
	defer func() {
		if !committed {
			_ = unix.Unlinkat(int(parent.Fd()), stageName, 0)
		}
	}()
	if _, err := stage.Write(raw); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "writing generated index stage")
	}
	if err := stage.Sync(); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "persisting generated index stage")
	}
	if _, err := r.Index(ctx); err != nil {
		return err
	}
	requested, info, err := securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "rechecking generated index parent")
	}
	_ = requested.Close()
	if !os.SameFile(parentInfo, info) {
		return failure("generated index parent was replaced")
	}
	stagedInfo, err := stage.Stat()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "checking index stage")
	}
	named, namedInfo, err := securepath.OpenChild(parent, stageName, false)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "rechecking index stage name")
	}
	_ = named.Close()
	if !os.SameFile(stagedInfo, namedInfo) {
		return failure("generated index stage was replaced")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), stageName, int(parent.Fd()), filepath.Base(absolute)); err != nil {
		return renameFailure(err, "committing generated index without replacement")
	}
	committed = true
	if onCommit != nil {
		onCommit()
	}
	if err := parent.Sync(); err != nil {
		return committedFailure(ctx, err, "generated index committed but parent sync failed; no rollback performed")
	}
	requested, info, err = securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return committedFailure(ctx, err, "generated index committed but parent recheck failed; no rollback performed")
	}
	defer requested.Close()
	if !os.SameFile(parentInfo, info) {
		return committedFailure(ctx, nil, "generated index committed but parent was replaced; no rollback performed")
	}
	published, publishedInfo, err := securepath.OpenChild(requested, filepath.Base(absolute), false)
	if err != nil {
		return committedFailure(ctx, err, "generated index committed but file recheck failed; no rollback performed")
	}
	defer published.Close()
	if !os.SameFile(stagedInfo, publishedInfo) {
		return committedFailure(ctx, nil, "generated index committed but file identity was replaced; no rollback performed")
	}
	final, err := io.ReadAll(io.LimitReader(published, MaxIndexBytes+1))
	if err != nil || !bytes.Equal(final, raw) {
		return committedFailure(ctx, nil, "generated index committed but exact bytes changed; no rollback performed")
	}
	if _, err := r.Index(ctx); err != nil {
		return committedFailure(ctx, err, "generated index committed but receipt lifetime recheck failed; no rollback performed")
	}
	return nil
}

func renameFailure(err error, message string) error {
	code := e.CodeVerifyFailed
	if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
		code = e.CodeImmutableViolation
	} else if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		code = e.CodeEngineCapability
	}
	return e.Wrap(code, err, "%s", message)
}
func committedFailure(ctx context.Context, err error, message string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", message)
	}
	if err == nil {
		return e.New(e.CodeVerifyFailed, "%s", message)
	}
	return e.Wrap(e.CodeVerifyFailed, err, "%s", message)
}
