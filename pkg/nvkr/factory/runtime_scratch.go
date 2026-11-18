// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// runtimeScratch belongs to the original privileged disk coordinator. A path
// supplied in a Plan or a restored result cannot construct this owner. The disk
// coordinator closes it only after all production and consumer owners join.
type runtimeScratch struct {
	mu        sync.Mutex
	parent    *os.File
	directory *os.File
	location  string
}

func (scratch *runtimeScratch) path(ctx context.Context) (string, error) {
	if ctx == nil || scratch == nil {
		return "", e.New(e.CodeEngineCapability, "runtime scratch requires its original privileged owner")
	}
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "runtime scratch admission canceled")
	}
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	if scratch.directory == nil || scratch.parent == nil {
		return "", e.New(e.CodeGateUnmet, "runtime scratch owner is closed")
	}
	held, err := scratch.directory.Stat()
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "observing held runtime scratch")
	}
	current, err := os.Lstat(scratch.location)
	if err != nil || !os.SameFile(held, current) || !current.IsDir() {
		return "", e.New(e.CodeVerifyFailed, "runtime scratch location changed")
	}
	return scratch.location, nil
}

func (scratch *runtimeScratch) Close() error {
	if scratch == nil {
		return nil
	}
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	if scratch.directory == nil {
		return nil
	}
	// The parent is fixed, root-owned and non-writable by other principals.
	// Remove through its held descriptor; never follow a replaced outer path.
	cleanup := removeRuntimeScratch(scratch.parent, filepath.Base(scratch.location))
	err := errors.Join(cleanup, scratch.directory.Close(), scratch.parent.Close())
	scratch.directory, scratch.parent = nil, nil
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "closing original runtime scratch")
	}
	return nil
}
