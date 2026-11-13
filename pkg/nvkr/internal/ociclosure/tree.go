// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package ociclosure captures immutable OCI bytes and verifies complete offline
// descriptor closures. It grants no execution or publication authority.
package ociclosure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const (
	maxTerminalTreeEntries = 100_000
	maxTerminalInlineBytes = 256 << 20
	maxOCIManifestBytes    = 8 << 20
)

// TreeState is an opaque description used for regular-tree diagnostics.
type TreeState []terminalTreeEntry

type terminalTreeEntry struct {
	relative string
	info     os.FileInfo
	digest   string
}

type terminalHeldFile struct {
	digest string
	size   int64
	inline bool
	raw    []byte
}

type Tree struct {
	files       map[string]terminalHeldFile
	dirs        map[string]bool
	digest      string
	inlineBytes int64
}

type treeView struct {
	SchemaVersion string          `json:"schemaVersion"`
	Entries       []treeEntryView `json:"entries"`
}

type treeEntryView struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   string `json:"mode"`
	Digest string `json:"digest,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// Snapshot turns a producer-controlled tree into immutable in-process
// facts while walking only already-open directory descriptors. Large blobs are
// retained as their independently computed digest and size; bounded OCI documents
// are defensively copied into private memory for semantic parsing. No retained
// pathname exists for a same-UID process to rename, replace, or temporarily swap.
func Snapshot(ctx context.Context, root string) (*Tree, error) {
	if ctx == nil {
		return nil, fmt.Errorf("artifact-tree capture requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, sourceInfo, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("artifact tree %s is not a real directory: %w", root, err)
	}
	defer source.Close()
	tree := &Tree{files: map[string]terminalHeldFile{}, dirs: map[string]bool{}}
	state, views, err := captureTerminalDirectoryContext(ctx, source, sourceInfo, "", tree)
	if err != nil {
		return nil, err
	}

	// Rewalk the same held root inode after copying. This detects additions,
	// removals, replacements, and in-place content changes during the snapshot.
	reopened, reopenedInfo, err := reopenDirectoryDescriptor(source)
	if err != nil {
		return nil, err
	}
	after, captureErr := captureRegularTreeOpenContext(ctx, reopened, reopenedInfo)
	closeErr := reopened.Close()
	if captureErr != nil {
		return nil, captureErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !sameTreeState(state, after) {
		return nil, fmt.Errorf("artifact tree %s changed while being captured", root)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Path < views[j].Path })
	tree.digest, err = canonical.Digest(canonical.KindTerminalTree, treeView{
		SchemaVersion: canonical.KindTerminalTree, Entries: views,
	})
	if err != nil {
		return nil, err
	}
	return tree, nil
}

func reopenDirectoryDescriptor(directory *os.File) (*os.File, os.FileInfo, error) {
	if directory == nil {
		return nil, nil, fmt.Errorf("cannot reopen a nil directory descriptor")
	}
	fd, err := unix.Openat(int(directory.Fd()), ".",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	reopened := os.NewFile(uintptr(fd), directory.Name())
	if reopened == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("reopening directory descriptor")
	}
	info, err := reopened.Stat()
	if err != nil || !info.IsDir() {
		_ = reopened.Close()
		return nil, nil, fmt.Errorf("reopened descriptor is not a directory")
	}
	return reopened, info, nil
}

func captureTerminalDirectoryContext(ctx context.Context, source *os.File, sourceInfo os.FileInfo, prefix string, tree *Tree) ([]terminalTreeEntry, []treeEntryView, error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("artifact-tree capture requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if tree == nil || len(tree.files)+len(tree.dirs) >= maxTerminalTreeEntries {
		return nil, nil, fmt.Errorf("artifact tree exceeds its %d-entry qualification bound", maxTerminalTreeEntries)
	}
	entries := []terminalTreeEntry{{relative: ".", info: sourceInfo}}
	viewPath := "."
	if prefix != "" {
		viewPath = filepath.ToSlash(prefix)
	}
	views := []treeEntryView{{Path: viewPath, Type: "directory", Mode: fmt.Sprintf("%04o", sourceInfo.Mode().Perm())}}
	tree.dirs[viewPath] = true
	children, err := ReadDirectoryEntriesBounded(source, maxTerminalTreeEntries)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := child.Name()
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			return nil, nil, fmt.Errorf("unsafe artifact tree child name %q", name)
		}
		relative := name
		if prefix != "" {
			relative = filepath.Join(prefix, name)
		}
		if child.IsDir() {
			opened, info, err := securepath.OpenChild(source, name, true)
			if err != nil {
				return nil, nil, fmt.Errorf("opening artifact directory %s: %w", relative, err)
			}
			childrenState, childrenViews, captureErr := captureTerminalDirectoryContext(ctx, opened, info, relative, tree)
			after, sourceStatErr := opened.Stat()
			sourceCloseErr := opened.Close()
			if captureErr != nil {
				return nil, nil, captureErr
			}
			if sourceStatErr != nil || !securepath.SameMetadata(info, after) {
				return nil, nil, fmt.Errorf("artifact directory %s changed while being captured", relative)
			}
			if sourceCloseErr != nil {
				return nil, nil, sourceCloseErr
			}
			childrenState[0].relative = relative
			entries = append(entries, childrenState...)
			views = append(views, childrenViews...)
			continue
		}

		opened, info, err := securepath.OpenChild(source, name, false)
		if err != nil {
			return nil, nil, fmt.Errorf("artifact tree entry %s is not a regular file: %w", relative, err)
		}
		held, observed, captureErr := captureTerminalRegularContext(ctx, opened, info, relative)
		closeErr := opened.Close()
		if captureErr != nil {
			return nil, nil, captureErr
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		slashPath := filepath.ToSlash(relative)
		if len(tree.files)+len(tree.dirs) >= maxTerminalTreeEntries {
			return nil, nil, fmt.Errorf("artifact tree exceeds its %d-entry qualification bound", maxTerminalTreeEntries)
		}
		if held.inline {
			if held.size > maxTerminalInlineBytes-tree.inlineBytes {
				return nil, nil, fmt.Errorf("artifact tree exceeds its %d-byte inline qualification bound", maxTerminalInlineBytes)
			}
			tree.inlineBytes += held.size
		}
		tree.files[slashPath] = held
		entries = append(entries, terminalTreeEntry{relative: relative, info: observed, digest: held.digest})
		views = append(views, treeEntryView{
			Path: slashPath, Type: "file", Mode: fmt.Sprintf("%04o", observed.Mode().Perm()),
			Digest: held.digest, Size: held.size,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })
	return entries, views, nil
}

func captureTerminalRegularContext(ctx context.Context, source *os.File, before os.FileInfo, relative string) (terminalHeldFile, os.FileInfo, error) {
	if ctx == nil {
		return terminalHeldFile{}, nil, fmt.Errorf("artifact capture requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return terminalHeldFile{}, nil, err
	}
	if source == nil || before == nil || !before.Mode().IsRegular() || before.Size() < 0 {
		return terminalHeldFile{}, nil, fmt.Errorf("artifact file %s is not a regular file", relative)
	}
	hash := sha256.New()
	var bounded bytes.Buffer
	writer := io.Writer(hash)
	if before.Size() <= maxOCIManifestBytes {
		writer = io.MultiWriter(hash, &bounded)
	}
	size, copyErr := CopyContext(ctx, writer, io.LimitReader(source, before.Size()+1))
	after, statErr := source.Stat()
	if copyErr == nil && statErr == nil &&
		(!securepath.SameMetadata(before, after) || size != before.Size() || after.Size() != before.Size()) {
		copyErr = fmt.Errorf("artifact file %s changed while being captured", relative)
	}
	if copyErr != nil {
		return terminalHeldFile{}, nil, copyErr
	}
	if statErr != nil {
		return terminalHeldFile{}, nil, statErr
	}
	held := terminalHeldFile{digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), size: size}
	if size <= maxOCIManifestBytes {
		held.inline = true
		held.raw = append([]byte(nil), bounded.Bytes()...)
	}
	return held, after, nil
}

func (tree *Tree) ReadRegular(ctx context.Context, relative string, limit int64) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("reading held OCI bytes requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tree == nil || limit < 0 || filepath.IsAbs(relative) ||
		filepath.ToSlash(filepath.Clean(relative)) != relative || strings.HasPrefix(relative, "../") {
		return nil, fmt.Errorf("unsafe held OCI path %q", relative)
	}
	file, ok := tree.files[relative]
	if !ok || file.size > limit || !file.inline || int64(len(file.raw)) != file.size {
		return nil, fmt.Errorf("held OCI file %s is unavailable within the %d-byte limit", relative, limit)
	}
	return append([]byte(nil), file.raw...), nil
}

func (tree *Tree) BlobInventory(ctx context.Context) (map[string]int64, error) {
	if tree == nil {
		return nil, fmt.Errorf("held OCI tree is nil")
	}
	if len(tree.dirs) != 3 || !tree.dirs["."] || !tree.dirs["blobs"] || !tree.dirs["blobs/sha256"] {
		return nil, fmt.Errorf("held OCI layout has an unexpected directory set")
	}
	if _, ok := tree.files["oci-layout"]; !ok {
		return nil, fmt.Errorf("held OCI layout omits oci-layout")
	}
	if _, ok := tree.files["index.json"]; !ok {
		return nil, fmt.Errorf("held OCI layout omits index.json")
	}
	actual := make(map[string]int64, len(tree.files)-2)
	for path, file := range tree.files {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "offline OCI verification canceled")
		}
		if path == "oci-layout" || path == "index.json" {
			continue
		}
		const prefix = "blobs/sha256/"
		name := strings.TrimPrefix(path, prefix)
		if name == path || len(name) != 64 || strings.ContainsRune(name, '/') || file.digest != "sha256:"+name {
			return nil, fmt.Errorf("held OCI layout contains unexpected or misnamed file %s", path)
		}
		actual[file.digest] = file.size
	}
	return actual, nil
}

func CaptureTreeState(ctx context.Context, root string) (TreeState, error) {
	if ctx == nil {
		return nil, fmt.Errorf("artifact-tree verification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rootDir, rootInfo, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("artifact tree %s is not a real directory: %w", root, err)
	}
	defer rootDir.Close()
	return captureRegularTreeOpenContext(ctx, rootDir, rootInfo)
}

func captureRegularTreeOpenContext(ctx context.Context, rootDir *os.File, rootInfo os.FileInfo) ([]terminalTreeEntry, error) {
	if ctx == nil {
		return nil, fmt.Errorf("artifact-tree verification requires a non-nil context")
	}
	entries := []terminalTreeEntry{{relative: ".", info: rootInfo}}
	var walk func(*os.File, string) error
	walk = func(directory *os.File, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := ReadDirectoryEntriesBounded(directory, maxTerminalTreeEntries)
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= maxTerminalTreeEntries {
				return fmt.Errorf("artifact tree exceeds its %d-entry qualification bound", maxTerminalTreeEntries)
			}
			name := child.Name()
			relative := name
			if prefix != "" {
				relative = filepath.Join(prefix, name)
			}
			if child.IsDir() {
				opened, info, err := securepath.OpenChild(directory, name, true)
				if err != nil {
					return fmt.Errorf("opening artifact directory %s: %w", relative, err)
				}
				entries = append(entries, terminalTreeEntry{relative: relative, info: info})
				err = walk(opened, relative)
				closeErr := opened.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
				continue
			}
			opened, info, err := securepath.OpenChild(directory, name, false)
			if err != nil {
				return fmt.Errorf("artifact tree entry %s is not a regular file: %w", relative, err)
			}
			digest, _, observed, hashErr := HashOpenRegularContext(ctx, opened, info)
			closeErr := opened.Close()
			if hashErr != nil {
				return hashErr
			}
			if closeErr != nil {
				return closeErr
			}
			entries = append(entries, terminalTreeEntry{relative: relative, info: observed, digest: digest})
		}
		return nil
	}
	err := walk(rootDir, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })
	return entries, nil
}

func ReadDirectoryEntriesBounded(directory *os.File, maxEntries int) ([]os.DirEntry, error) {
	entries := make([]os.DirEntry, 0)
	for {
		batch, err := directory.ReadDir(1024)
		entries = append(entries, batch...)
		if len(entries) > maxEntries {
			return nil, fmt.Errorf("directory exceeds its %d-entry qualification bound", maxEntries)
		}
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func sameTreeState(want, got []terminalTreeEntry) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i].relative != got[i].relative || want[i].digest != got[i].digest ||
			want[i].info == nil || got[i].info == nil || !os.SameFile(want[i].info, got[i].info) ||
			want[i].info.Mode() != got[i].info.Mode() || want[i].info.Size() != got[i].info.Size() {
			return false
		}
	}
	return true
}

// Digest returns the existing typed terminal-tree identity of the captured bytes.
func (tree *Tree) Digest() string {
	if tree == nil {
		return ""
	}
	return tree.digest
}

// Size totals captured regular-file bytes with overflow and cancellation checks.
func (tree *Tree) Size(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, e.New(e.CodeUsage, "OCI sizing requires context")
	}
	if tree == nil {
		return 0, e.New(e.CodeVerifyFailed, "OCI tree is absent")
	}
	var size int64
	for _, file := range tree.files {
		if err := ctx.Err(); err != nil {
			return 0, e.Wrap(e.CodeTimeout, err, "sizing held OCI layout")
		}
		if file.size > 0 && size > int64(^uint64(0)>>1)-file.size {
			return 0, e.New(e.CodeVerifyFailed, "OCI layout size overflows int64")
		}
		size += file.size
	}
	return size, nil
}
