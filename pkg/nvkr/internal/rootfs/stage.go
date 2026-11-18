// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package rootfs stages one authenticated, uncompressed root filesystem layer. It
// grants no execution authority: the Run owner must authenticate production and
// evidence and admit the native launcher before calling this package.
package rootfs

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	MaxLayerBytes int64 = 8 << 30
	maxMembers          = 100_000
)

// Tree owns a private extraction directory. The owner must stop and join every
// consumer before Close. The archive remains caller-owned. Extracted files have
// no set-ID bits or archive-supplied ownership; the launcher additionally mounts
// this projection read-only, nodev and nosuid before dropping privileges.
type Tree struct {
	root        *os.Root
	parent      string
	digest      string
	directories []directoryMode
}

// Directory duplicates the staging root for descriptor-based launcher entry.
// Possession of this descriptor is not a runtime-use receipt.
func (tree *Tree) Directory() (*os.File, error) {
	if tree == nil || tree.root == nil {
		return nil, e.New(e.CodeGateUnmet, "runtime root filesystem is closed or absent")
	}
	file, err := tree.root.Open(".")
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening staged runtime root")
	}
	return file, nil
}

func (tree *Tree) Digest() string {
	if tree == nil {
		return ""
	}
	return tree.digest
}

func (tree *Tree) Close() error {
	if tree == nil || tree.root == nil {
		return nil
	}
	err := restoreDirectoryAccess(tree.root, tree.directories)
	err = errors.Join(err, tree.root.Close())
	tree.root = nil
	if cleanup := os.RemoveAll(tree.parent); cleanup != nil {
		err = errors.Join(err, cleanup)
	}
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "cleaning private runtime root filesystem")
	}
	return nil
}

type layerInput interface {
	io.ReaderAt
	Size() int64
	Digest() string
	AssertUnchangedContext(context.Context) error
}

// Stage accepts only digest-bound bytes, creates a fresh private disk tree, and
// rejects duplicate paths, device nodes, sparse encodings, unknown archive
// extensions and writes through archive-created symlinks. It hashes the complete
// extracted stream before release. Absolute symlinks remain inert until entry
// into the isolated root.
func Stage(ctx context.Context, layer layerInput, scratch string) (_ *Tree, resultErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "runtime staging requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "runtime staging canceled")
	}
	if layer == nil || layer.Size() <= 0 || layer.Size() > MaxLayerBytes || layer.Size()%512 != 0 {
		return nil, e.New(e.CodeVerifyFailed, "runtime layer is absent or oversized")
	}
	if err := layer.AssertUnchangedContext(ctx); err != nil {
		return nil, stageError(ctx, err)
	}
	directory, _, err := securepath.OpenDirectory(scratch)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	_, capacityErr := securepath.AvailableDiskBackedBytes(ctx, directory)
	if err := errors.Join(capacityErr, directory.Close()); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "runtime extraction requires disk-backed scratch")
	}
	private, err := os.MkdirTemp(scratch, "nvkr-runtime-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private runtime staging directory")
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.RemoveAll(private))
		}
	}()
	rootPath := filepath.Join(private, "root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		return nil, stageError(ctx, err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	var directories []directoryMode
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, restoreDirectoryAccess(root, directories), root.Close())
		}
	}()
	hash := sha256.New()
	stream := &contextReader{ctx: ctx, reader: io.TeeReader(io.NewSectionReader(layer, 0, layer.Size()), hash)}
	reader := tar.NewReader(stream)
	seen := make(map[string]byte)
	var links []hardlink
	var total int64
	var nextHeader int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			if stream.offset != nextHeader+1024 {
				return nil, e.New(e.CodeVerifyFailed, "runtime layer has an incomplete terminator or hidden trailing headers")
			}
			var terminator [1024]byte
			if _, err := layer.ReadAt(terminator[:], nextHeader); err != nil {
				return nil, stageError(ctx, err)
			}
			for _, value := range terminator {
				if value != 0 {
					return nil, e.New(e.CodeVerifyFailed, "runtime layer has a nonzero terminator")
				}
			}
			break
		}
		if err != nil {
			return nil, stageError(ctx, err)
		}
		name, err := memberName(header.Name, header.Typeflag == tar.TypeDir)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "runtime layer member %.256q (%d name bytes, typeflag 0x%02x)", header.Name, len(header.Name), header.Typeflag)
		}
		if _, duplicate := seen[name]; duplicate || len(seen) >= maxMembers {
			return nil, e.New(e.CodeVerifyFailed, "runtime layer repeats a path or exceeds its member bound")
		}
		if header.Size < 0 || header.Size > MaxLayerBytes-total {
			return nil, e.New(e.CodeVerifyFailed, "runtime layer exceeds its expanded size bound")
		}
		if header.Mode < 0 || header.Mode & ^int64(0o7777) != 0 {
			return nil, e.New(e.CodeVerifyFailed, "runtime member has invalid mode bits")
		}
		total += header.Size
		nextHeader = stream.offset + (header.Size+511)/512*512
		for key := range header.PAXRecords {
			if key != "path" && key != "linkpath" && key != "mtime" && key != "atime" && key != "ctime" {
				return nil, e.New(e.CodeVerifyFailed, "runtime layer has an unsupported PAX extension")
			}
		}
		if err := requireParents(root, name); err != nil {
			return nil, stageError(ctx, err)
		}
		seen[name] = header.Typeflag
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 || header.Linkname != "" {
				return nil, e.New(e.CodeVerifyFailed, "runtime directory has payload or a link target")
			}
			if name != "." {
				if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
					return nil, stageError(ctx, err)
				}
				info, err := root.Lstat(name)
				if err != nil || !info.IsDir() {
					return nil, e.New(e.CodeVerifyFailed, "runtime directory replaces a non-directory")
				}
			}
			directories = append(directories, directoryMode{name, os.FileMode(header.Mode) & 0o777, header.ModTime})
		case tar.TypeReg:
			if header.Linkname != "" {
				return nil, e.New(e.CodeVerifyFailed, "runtime regular file has a link target")
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, stageError(ctx, err)
			}
			_, copyErr := io.Copy(file, reader)
			modeErr := file.Chmod(os.FileMode(header.Mode) & 0o777)
			closeErr := file.Close()
			if err := errors.Join(copyErr, modeErr, closeErr); err != nil {
				return nil, stageError(ctx, err)
			}
			if err := root.Chtimes(name, header.ModTime, header.ModTime); err != nil {
				return nil, stageError(ctx, err)
			}
		case tar.TypeSymlink:
			if header.Size != 0 || header.Linkname == "" || !utf8.ValidString(header.Linkname) || strings.ContainsRune(header.Linkname, 0) {
				return nil, e.New(e.CodeVerifyFailed, "runtime symbolic link is invalid")
			}
			if err := root.Symlink(header.Linkname, name); err != nil {
				return nil, stageError(ctx, err)
			}
		case tar.TypeLink:
			target, err := memberName(header.Linkname, false)
			if err != nil || header.Size != 0 {
				return nil, e.New(e.CodeVerifyFailed, "runtime hard link is invalid")
			}
			links = append(links, hardlink{name, target})
		default:
			return nil, e.New(e.CodeVerifyFailed, "runtime layer contains an unsupported member type")
		}
	}
	if seen["."] != tar.TypeDir {
		return nil, e.New(e.CodeVerifyFailed, "runtime layer has no explicit root directory")
	}
	if err := requireZeroTrailer(stream); err != nil {
		return nil, stageError(ctx, err)
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != layer.Digest() {
		return nil, e.New(e.CodeVerifyFailed, "runtime layer changed during extraction")
	}
	if err := createLinks(root, links, seen); err != nil {
		return nil, stageError(ctx, err)
	}
	// Directories stayed private and writable throughout extraction. Restore
	// ordinary permissions only after every archive write has finished.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := root.Chtimes(directories[i].name, directories[i].mtime, directories[i].mtime); err != nil {
			return nil, stageError(ctx, err)
		}
		if err := root.Chmod(directories[i].name, directories[i].mode); err != nil {
			return nil, stageError(ctx, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, stageError(ctx, err)
	}
	return &Tree{root: root, parent: private, digest: layer.Digest(), directories: directories}, nil
}

type directoryMode struct {
	name  string
	mode  os.FileMode
	mtime time.Time
}

type hardlink struct{ name, target string }

func memberName(raw string, directory bool) (string, error) {
	// Runtime layers use Unix paths: only '/' separates components. Preserve
	// literal backslashes (including systemd's escaped unit names) without
	// unescaping them or interpreting them as alternate separators.
	name := strings.TrimPrefix(raw, "./")
	if directory {
		name = strings.TrimSuffix(name, "/")
		if raw == "./" || raw == "." {
			name = "."
		}
	}
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) ||
		strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || (!directory && name == ".") {
		return "", e.New(e.CodeVerifyFailed, "runtime layer path is not root-relative and canonical")
	}
	return name, nil
}

func requireParents(root *os.Root, name string) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(parent, "/") {
		current = path.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return e.New(e.CodeVerifyFailed, "runtime layer writes through a non-directory parent")
		}
	}
	return nil
}

func restoreDirectoryAccess(root *os.Root, directories []directoryMode) error {
	if err := root.Chmod(".", 0o700); err != nil {
		return err
	}
	for _, directory := range directories {
		if err := root.Chmod(directory.name, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func createLinks(root *os.Root, links []hardlink, seen map[string]byte) error {
	// mkosi's tar can emit a hard link before its regular-file target. Only direct
	// regular targets are admitted; chains, cycles and links to symlinks fail.
	for _, link := range links {
		if seen[link.target] != tar.TypeReg {
			return e.New(e.CodeVerifyFailed, "runtime hard link does not name a regular archive member")
		}
		if err := root.Link(link.target, link.name); err != nil {
			return err
		}
	}
	return nil
}

func requireZeroTrailer(reader io.Reader) error {
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		for _, value := range buffer[:n] {
			if value != 0 {
				return e.New(e.CodeVerifyFailed, "runtime layer has trailing nonzero bytes")
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func stageError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "runtime staging canceled")
	}
	return e.Wrap(e.CodeVerifyFailed, err, "staging runtime root filesystem")
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
	offset int64
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.reader.Read(buffer)
	reader.offset += int64(n)
	return n, err
}
