// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
)

// localStore is publish --local-dir: the bucket's create-only protocol on a local
// directory, for a dry run. It creates an object by linking a complete temporary
// file into place, so a reader never sees a partial object.
type localStore struct{ root string }

func (s localStore) Create(ctx context.Context, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "writing %s", name)
	}
	target, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "creating the directory of %s", name)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".nvkr-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeUnavailable, err, "writing %s", name)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	_, writeErr := temp.Write(data)
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "writing %s", name)
	}
	if err := os.Link(temp.Name(), target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return resultspublish.ErrExists
		}
		return e.Wrap(e.CodeUnavailable, err, "writing %s", name)
	}
	return nil
}

func (s localStore) Read(ctx context.Context, name string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "reading %s", name)
	}
	target, err := s.path(name)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, e.Wrap(e.CodeUnavailable, err, "reading %s", name)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, e.Wrap(e.CodeUnavailable, err, "reading %s", name)
	}
	if int64(len(data)) > maxBytes {
		return nil, e.New(e.CodeImmutableViolation, "%s exceeds %d bytes", name, maxBytes)
	}
	return data, nil
}

func (s localStore) path(name string) (string, error) {
	if !fs.ValidPath(name) || name == "." {
		return "", e.New(e.CodeUsage, "object path %q is not a relative path", name)
	}
	return filepath.Join(s.root, filepath.FromSlash(name)), nil
}
