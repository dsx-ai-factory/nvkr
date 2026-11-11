// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bundlearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Extract admits the complete bounded archive before creating a private new
// directory. Only inspected regular files/directories are materialized, beneath
// an os.Root; failure removes the partial extraction. The caller owns raw bytes.
func Extract(ctx context.Context, raw []byte, destination string) (err error) {
	if len(raw) > MaxArchiveBytes {
		return e.New(e.CodeVerifyFailed, "bundle exceeds its compressed byte bound")
	}
	raw = bytes.Clone(raw)
	if _, err := Inspect(ctx, bytes.NewReader(raw)); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "creating bundle extraction")
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening bundle extraction")
	}
	defer func() { _ = root.Close() }()
	zipped, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return archiveError(ctx, err, "opening admitted bundle")
	}
	defer func() { _ = zipped.Close() }()
	reader := tar.NewReader(contextReader{ctx: ctx, source: zipped})
	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return archiveError(ctx, readErr, "extracting admitted bundle")
		}
		if header.Typeflag == tar.TypeDir {
			if err := root.MkdirAll(header.Name, 0o700); err != nil {
				return archiveError(ctx, err, "creating bundle directory")
			}
			continue
		}
		if err := root.MkdirAll(path.Dir(header.Name), 0o700); err != nil {
			return archiveError(ctx, err, "creating bundle parent")
		}
		file, err := root.OpenFile(header.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			return archiveError(ctx, err, "creating bundle file")
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return archiveError(ctx, copyErr, "writing bundle file")
		}
		if closeErr != nil {
			return archiveError(ctx, closeErr, "closing bundle file")
		}
	}
}
