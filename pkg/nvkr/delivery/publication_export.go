// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// WriteLayout exports the complete captured closure without reopening its source.
// It preserves exact transport bytes and grants no signature or policy authority.
// The existing destination parent must be a real directory; the destination is
// committed without replacement only after every held file has been written.
func (publication *Publication) WriteLayout(ctx context.Context, out string) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if publication == nil {
		return e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed || len(publication.files) == 0 {
		return e.New(e.CodeGateUnmet, "publication is not open")
	}
	writers := make(map[string]func(io.Writer) error, len(publication.files)+len(publication.diskPayloads))
	for _, file := range publication.files[1:] {
		writers[file.Digest()] = func(writer io.Writer) error {
			_, err := file.WriteToContext(ctx, writer)
			return err
		}
	}
	for digest, file := range publication.diskPayloads {
		writers[digest] = func(writer io.Writer) error {
			info, err := file.Stat()
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "reading held disk snapshot")
			}
			_, err = streamDiskPayload(ctx, file, writer, digest, info.Size())
			return err
		}
	}
	return writeHeldLayout(ctx, out, func(writer io.Writer) error {
		_, err := publication.files[0].WriteToContext(ctx, writer)
		return err
	}, writers)
}
