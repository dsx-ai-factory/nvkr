// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// The destination parent must already exist. Once opened, all writes, cleanup,
// no-replace commit and fsync use held directory descriptors, not reopened paths.
func writeImageIndexLayout(ctx context.Context, out string, closure *imageIndexClosure) error {
	blobs := make(map[string]func(io.Writer) error, len(closure.blobs))
	for digest, file := range closure.blobs {
		blobs[digest] = func(writer io.Writer) error { _, err := file.WriteToContext(ctx, writer); return err }
	}
	return writeHeldLayout(ctx, out, func(writer io.Writer) error {
		_, err := closure.index.WriteToContext(ctx, writer)
		return err
	}, blobs)
}

// Both owners use the existing descriptor-relative, no-replace layout transaction.
func writeHeldLayout(ctx context.Context, out string, writeIndex func(io.Writer) error, blobWriters map[string]func(io.Writer) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	out = filepath.Clean(out)
	name := filepath.Base(out)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return e.New(e.CodeUsage, "layout destination must name a new directory")
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(out))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening layout destination parent")
	}
	defer parent.Close()
	stage, temporary, err := createImageIndexStage(parent)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating private layout stage")
	}
	defer stage.Close()
	var blobs, shaDirectory *os.File
	committed := false
	defer func() {
		if !committed {
			if shaDirectory != nil {
				for digest := range blobWriters {
					_ = unix.Unlinkat(int(shaDirectory.Fd()), strings.TrimPrefix(digest, "sha256:"), 0)
				}
			}
			if blobs != nil {
				_ = unix.Unlinkat(int(blobs.Fd()), "sha256", unix.AT_REMOVEDIR)
			}
			_ = unix.Unlinkat(int(stage.Fd()), "blobs", unix.AT_REMOVEDIR)
			_ = unix.Unlinkat(int(stage.Fd()), "index.json", 0)
			_ = unix.Unlinkat(int(stage.Fd()), "oci-layout", 0)
			_ = unix.Unlinkat(int(parent.Fd()), temporary, unix.AT_REMOVEDIR)
		}
		if shaDirectory != nil {
			_ = shaDirectory.Close()
		}
		if blobs != nil {
			_ = blobs.Close()
		}
	}()
	blobs, err = createImageIndexDirectory(stage, "blobs")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating layout blob directory")
	}
	shaDirectory, err = createImageIndexDirectory(blobs, "sha256")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating layout SHA-256 directory")
	}
	digests := make([]string, 0, len(blobWriters))
	for digest := range blobWriters {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		if err := writeImageIndexFile(ctx, shaDirectory, strings.TrimPrefix(digest, "sha256:"),
			blobWriters[digest]); err != nil {
			return err
		}
	}
	if err := writeImageIndexFile(ctx, stage, "index.json", writeIndex); err != nil {
		return err
	}
	if err := writeImageIndexFile(ctx, stage, "oci-layout", func(writer io.Writer) error {
		_, err := io.WriteString(writer, `{"imageLayoutVersion":"1.0.0"}`)
		return err
	}); err != nil {
		return err
	}
	for _, directory := range []*os.File{shaDirectory, blobs, stage} {
		if err := directory.Sync(); err != nil {
			return e.Wrap(e.CodeInternal, err, "persisting layout directory")
		}
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), name); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing layout without replacement")
	}
	committed = true
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "persisting layout commit")
	}
	return nil
}

func createImageIndexStage(parent *os.File) (*os.File, string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var token [12]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, "", err
		}
		name := ".nvkr-layout-" + hex.EncodeToString(token[:])
		directory, err := createImageIndexDirectory(parent, name)
		if err == unix.EEXIST {
			continue
		}
		return directory, name, err
	}
	return nil, "", e.New(e.CodeInternal, "could not allocate private layout stage")
}

func createImageIndexDirectory(parent *os.File, name string) (*os.File, error) {
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return nil, err
	}
	directory, _, err := securepath.OpenChild(parent, name, true)
	if err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
		return nil, err
	}
	return directory, nil
}

func writeImageIndexFile(ctx context.Context, directory *os.File, name string, write func(io.Writer) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating held layout member")
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if err := write(file); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeInternal, err, "writing held layout member"))
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "persisting held layout member")
	}
	return nil
}
