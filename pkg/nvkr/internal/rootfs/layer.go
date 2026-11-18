// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package rootfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
)

// Layer owns a private disk snapshot, bound to the authenticated OCI layer hash.
// Its only retained descriptor is read-only and its pathname is unlinked before
// return. It exposes no descriptor or writable handle. This is byte retention,
// not execution authority or a kernel memfd seal; Stage also hashes the complete
// stream it extracts before releasing a root to any consumer.
type Layer struct {
	mu     sync.Mutex
	file   *os.File
	size   int64
	digest string
}

func captureDiskLayer(ctx context.Context, source io.Reader, digest string, size int64, scratch string) (result *Layer, returnErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "runtime retention requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, stageError(ctx, err)
	}
	if scratch == "" || !filepath.IsAbs(scratch) {
		return nil, e.New(e.CodeEngineCapability, "runtime retention requires explicit disk-backed scratch")
	}
	directory, _, err := securepath.OpenDirectory(scratch)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	available, capacityErr := securepath.AvailableDiskBackedBytes(ctx, directory)
	if err := errors.Join(capacityErr, directory.Close()); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "runtime retention requires writable disk-backed scratch")
	}
	if size <= 0 || size > MaxLayerBytes || available < uint64(size)+(8<<20) {
		return nil, e.New(e.CodeEngineCapability, "insufficient disk-backed capacity for runtime layer")
	}
	private, err := os.MkdirTemp(scratch, "nvkr-runtime-layer-")
	if err != nil {
		return nil, stageError(ctx, err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, os.RemoveAll(private))
		if returnErr != nil && result != nil {
			returnErr = errors.Join(returnErr, result.Close())
			result = nil
		}
	}()
	path := filepath.Join(private, "layer")
	writable, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	defer writable.Close()
	hash := sha256.New()
	written, err := ociclosure.CopyContext(ctx, io.MultiWriter(writable, hash), io.LimitReader(source, size+1))
	if err != nil {
		return nil, stageError(ctx, err)
	}
	if written != size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, e.New(e.CodeVerifyFailed, "runtime layer differs from its authenticated descriptor")
	}
	if err := writable.Chmod(0o400); err != nil {
		return nil, stageError(ctx, err)
	}
	if err := writable.Sync(); err != nil {
		return nil, stageError(ctx, err)
	}
	if err := writable.Close(); err != nil {
		return nil, stageError(ctx, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	accepted := false
	defer func() {
		if !accepted || returnErr != nil {
			returnErr = errors.Join(returnErr, file.Close())
		}
	}()
	if err := os.Remove(path); err != nil {
		return nil, stageError(ctx, err)
	}
	layer := &Layer{file: file, size: size, digest: digest}
	if err := layer.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	accepted = true
	return layer, nil
}

func (layer *Layer) Size() int64 {
	if layer == nil {
		return 0
	}
	return layer.size
}
func (layer *Layer) Digest() string {
	if layer == nil {
		return ""
	}
	return layer.digest
}

func (layer *Layer) ReadAt(buffer []byte, offset int64) (int, error) {
	layer.mu.Lock()
	defer layer.mu.Unlock()
	if layer.file == nil {
		return 0, e.New(e.CodeGateUnmet, "runtime layer is closed")
	}
	return layer.file.ReadAt(buffer, offset)
}

func (layer *Layer) AssertUnchangedContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "runtime layer verification requires context")
	}
	if layer == nil {
		return e.New(e.CodeGateUnmet, "runtime layer is absent")
	}
	layer.mu.Lock()
	defer layer.mu.Unlock()
	if layer.file == nil {
		return e.New(e.CodeGateUnmet, "runtime layer is closed")
	}
	info, err := layer.file.Stat()
	if err != nil {
		return stageError(ctx, err)
	}
	if !info.Mode().IsRegular() || info.Size() != layer.size || info.Mode().Perm() != 0o400 {
		return e.New(e.CodeVerifyFailed, "retained runtime layer metadata changed")
	}
	hash := sha256.New()
	if _, err := ociclosure.CopyContext(ctx, hash, io.NewSectionReader(layer.file, 0, layer.size)); err != nil {
		return stageError(ctx, err)
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != layer.digest {
		return e.New(e.CodeVerifyFailed, "retained runtime layer bytes changed")
	}
	return nil
}

func (layer *Layer) Close() error {
	if layer == nil {
		return nil
	}
	layer.mu.Lock()
	defer layer.mu.Unlock()
	if layer.file == nil {
		return nil
	}
	err := layer.file.Close()
	layer.file = nil
	return err
}
