// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package rootfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"golang.org/x/sys/unix"
)

// Layer holds the authenticated OCI tar inode through a read-only descriptor.
// It creates no second retained copy and exposes neither the descriptor nor a
// writable handle. This is digest-bound retention, not a kernel memfd seal:
// mutations revoke the bytes, and Stage hashes the complete extracted stream
// before releasing its private root. The production owner also rechecks the
// original OCI closure. A saved path or digest cannot restore that owner.
type Layer struct {
	mu     sync.Mutex
	file   *os.File
	size   int64
	digest string
	mode   os.FileMode
}

func captureDiskLayer(ctx context.Context, source *os.File, digest string, size int64) (*Layer, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "runtime retention requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, stageError(ctx, err)
	}
	if source == nil || size <= 0 || size > MaxLayerBytes {
		return nil, e.New(e.CodeVerifyFailed, "runtime retention requires a bounded layer descriptor")
	}
	flags, err := unix.FcntlInt(source.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, e.New(e.CodeVerifyFailed, "runtime retention requires a read-only descriptor")
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(source.Fd()), &filesystem); err != nil {
		return nil, stageError(ctx, err)
	}
	if securepath.FilesystemSpaceFromStat(filesystem).MemoryBacked() {
		return nil, e.New(e.CodeEngineCapability, "runtime retention requires a disk-backed layer")
	}
	info, err := source.Stat()
	if err != nil {
		return nil, stageError(ctx, err)
	}
	fd, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	layer := &Layer{file: os.NewFile(uintptr(fd), "retained-runtime-layer"), size: size, digest: digest, mode: info.Mode().Perm()}
	if err := layer.AssertUnchangedContext(ctx); err != nil {
		_ = layer.Close()
		return nil, err
	}
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
	if !info.Mode().IsRegular() || info.Size() != layer.size || info.Mode().Perm() != layer.mode {
		return e.New(e.CodeVerifyFailed, "retained runtime layer metadata changed")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(layer.file.Fd()), &stat); err != nil {
		return stageError(ctx, err)
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 {
		return e.New(e.CodeVerifyFailed, "retained runtime layer ownership or write permissions changed")
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
