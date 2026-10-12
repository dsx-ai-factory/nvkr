// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package transportarchive creates and restores deterministic workspace carriers.
// Pack and Unpack own bounded archive handling, held filesystem access and atomic
// no-replace publication. Carriers confer no artifact or release authority.
package transportarchive

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const (
	maxTransportEntries = 1_000_000
	maxTransportRoots   = 4096
	// MaxArchiveBytes is the shared aggregate bound for one authenticated
	// workspace carrier. Callers must reject larger execution closures before
	// starting the consuming subprocess or VM.
	MaxArchiveBytes           int64 = 64 << 30
	maxTransportBytes               = MaxArchiveBytes
	transportCopyBufferSize         = 128 << 10
	transportReadDirBatchSize       = 1024
)

var errTransportNoReplaceUnsupported = errors.New("atomic no-replace transport publication is unsupported")

type transportEntry struct {
	relative string
	base     string
	kind     string
	link     string
	info     fs.FileInfo
}

type transportSymlinkInfo struct {
	name string
	mode fs.FileMode
	size int64
}

func (info transportSymlinkInfo) Name() string       { return info.name }
func (info transportSymlinkInfo) Size() int64        { return info.size }
func (info transportSymlinkInfo) Mode() fs.FileMode  { return info.mode }
func (info transportSymlinkInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (info transportSymlinkInfo) IsDir() bool        { return false }
func (info transportSymlinkInfo) Sys() any           { return nil }

// Pack creates an atomic, deterministic tar carrier for exact workspace
// paths. The tar is transport only (not an identity); consumers still authenticate
// source receipts, runner bytes, result receipts, and artifact digests independently.
// The archive must be beneath root so publication can remain descriptor-relative.
func Pack(ctx context.Context, archive, root string, paths []string) error {
	if err := requireTransportContext(ctx, "packing transport"); err != nil {
		return err
	}
	rootAbs, cleanPaths, err := transportRoots(root, paths)
	if err != nil {
		return err
	}
	archiveRelative, err := transportArchiveRelative(rootAbs, archive, cleanPaths)
	if err != nil {
		return err
	}
	rootDir, _, err := securepath.OpenDirectory(rootAbs)
	if err != nil {
		return e.Wrap(e.CodeReleaseIncomplete, err, "opening transport root %s", rootAbs)
	}
	defer rootDir.Close()
	entries, err := enumerateTransportEntries(ctx, rootDir, cleanPaths, maxTransportEntries)
	if err != nil {
		return transportError(ctx, e.CodeReleaseIncomplete, err, "enumerating transport members")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })
	var sourceBytes int64
	for _, entry := range entries {
		if entry.kind != "file" {
			continue
		}
		if entry.info.Size() < 0 || entry.info.Size() > maxTransportBytes-sourceBytes {
			return e.New(e.CodeReleaseIncomplete,
				"transport source exceeds its %d-byte aggregate bound", maxTransportBytes)
		}
		sourceBytes += entry.info.Size()
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "packing transport canceled")
	}

	archiveParent, archiveName, err := securepath.CreateParent(rootDir, archiveRelative)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"resolving transport archive %s beneath its root", archive)
	}
	defer archiveParent.Close()
	var archiveStat unix.Stat_t
	if err := unix.Fstatat(int(archiveParent.Fd()), archiveName, &archiveStat,
		unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return e.New(e.CodeImmutableViolation, "transport archive %s already exists", archive)
	} else if err != unix.ENOENT {
		return e.Wrap(e.CodeInternal, err, "checking transport archive %s", archive)
	}
	tmp, tmpName, err := securepath.CreateTemporarySibling(archiveParent)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating transport archive")
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = unix.Unlinkat(int(archiveParent.Fd()), tmpName, 0)
	}
	tw := tar.NewWriter(&transportLimitWriter{writer: tmp, remaining: maxTransportBytes})
	for _, ent := range entries {
		if err := ctx.Err(); err != nil {
			cleanup()
			return e.Wrap(e.CodeTimeout, err, "packing transport canceled")
		}
		observed, err := inspectTransportRelative(rootDir, ent.relative)
		if err != nil {
			cleanup()
			return transportError(ctx, e.CodeReleaseIncomplete, err, "reopening transport member %s", ent.relative)
		}
		if err := validateTransportEntryUnchanged(ent, observed); err != nil {
			if observed.File != nil {
				_ = observed.File.Close()
			}
			cleanup()
			return e.Wrap(e.CodeReleaseIncomplete, err, "revalidating transport member %s", ent.relative)
		}
		header, err := tar.FileInfoHeader(ent.info, ent.link)
		if err != nil {
			if observed.File != nil {
				_ = observed.File.Close()
			}
			cleanup()
			return e.Wrap(e.CodeReleaseIncomplete, err, "encoding transport header %s", ent.relative)
		}
		header.Name = ent.relative
		header.ModTime, header.AccessTime, header.ChangeTime = time.Unix(0, 0), time.Time{}, time.Time{}
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		header.Mode &= 0o777
		header.Format = tar.FormatPAX
		if err := tw.WriteHeader(header); err != nil {
			if observed.File != nil {
				_ = observed.File.Close()
			}
			cleanup()
			return e.Wrap(e.CodeInternal, err, "writing transport header %s", ent.relative)
		}
		if ent.kind == "file" {
			written, copyErr := copyTransportExact(ctx, tw, observed.File, ent.info.Size())
			after, statErr := observed.File.Stat()
			closeErr := observed.File.Close()
			if copyErr != nil {
				cleanup()
				return transportError(ctx, e.CodeReleaseIncomplete, copyErr, "copying transport member %s", ent.relative)
			}
			if statErr != nil {
				cleanup()
				return transportError(ctx, e.CodeReleaseIncomplete, statErr, "restating transport member %s", ent.relative)
			}
			if closeErr != nil {
				cleanup()
				return transportError(ctx, e.CodeReleaseIncomplete, closeErr, "closing transport member %s", ent.relative)
			}
			if written != ent.info.Size() || !securepath.SameMetadata(ent.info, after) {
				cleanup()
				return e.New(e.CodeReleaseIncomplete, "transport member %s changed while packing", ent.relative)
			}
		} else if observed.File != nil {
			if err := observed.File.Close(); err != nil {
				cleanup()
				return transportError(ctx, e.CodeReleaseIncomplete, err, "closing transport member %s", ent.relative)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return e.Wrap(e.CodeTimeout, err, "packing transport canceled")
	}
	if err := tw.Close(); err != nil {
		cleanup()
		return e.Wrap(e.CodeInternal, err, "closing transport archive")
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return e.Wrap(e.CodeInternal, err, "setting transport archive permissions")
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return e.Wrap(e.CodeTimeout, err, "packing transport canceled")
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return e.Wrap(e.CodeInternal, err, "syncing transport archive")
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return e.Wrap(e.CodeInternal, err, "syncing transport archive")
	}
	// Cancellation is observed immediately before the atomic publication point.
	// Once rename begins, completing the commit is preferable to reporting a
	// cancellation while leaving an archive whose publication status is ambiguous.
	if err := ctx.Err(); err != nil {
		cleanup()
		return e.Wrap(e.CodeTimeout, err, "packing transport canceled")
	}
	if err := PublishAtNoReplace(int(archiveParent.Fd()), tmpName,
		int(archiveParent.Fd()), archiveName); err != nil {
		cleanup()
		if errors.Is(err, errTransportNoReplaceUnsupported) {
			return e.Wrap(e.CodeEngineCapability, err,
				"publishing transport archive %s", archive)
		}
		return e.Wrap(e.CodeImmutableViolation, err,
			"publishing transport archive %s without replacement", archive)
	}
	if syncErr := archiveParent.Sync(); syncErr != nil {
		removeErr := unix.Unlinkat(int(archiveParent.Fd()), archiveName, 0)
		resyncErr := archiveParent.Sync()
		if removeErr == nil && resyncErr == nil {
			return e.Wrap(e.CodeImmutableViolation, syncErr,
				"persisting transport archive publication")
		}
		return e.Wrap(e.CodeImmutableViolation, errors.Join(syncErr, removeErr, resyncErr),
			"persisting or rolling back transport archive publication")
	}
	return nil
}

// enumerateTransportEntries walks only through already-open directory
// descriptors. Each ReadDir call is bounded, and the global entry bound is
// enforced before an entry is retained. Directory descriptors are closed before
// queued descendants are processed so a deeply nested input cannot exhaust the
// process descriptor table before reaching the entry bound.
func enumerateTransportEntries(ctx context.Context, root *os.File, bases []string, limit int) ([]transportEntry, error) {
	if ctx == nil {
		return nil, fmt.Errorf("transport enumeration requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == nil || limit <= 0 {
		return nil, fmt.Errorf("transport enumeration requires an open root and positive entry limit")
	}

	entries := make([]transportEntry, 0)
	pending := make([]int, 0)
	seen := make(map[string]bool)
	appendEntry := func(relative, base string, inspected securepath.Entry) error {
		if len(entries) >= limit {
			if inspected.File != nil {
				_ = inspected.File.Close()
			}
			return fmt.Errorf("transport exceeds its %d-entry bound", limit)
		}
		if seen[relative] {
			if inspected.File != nil {
				_ = inspected.File.Close()
			}
			return fmt.Errorf("overlapping transport members include %s twice", relative)
		}
		seen[relative] = true

		entry := transportEntry{relative: relative, base: base, kind: inspected.Kind, link: inspected.Target}
		switch inspected.Kind {
		case "directory", "file":
			entry.info = inspected.Info
		case "symlink":
			if !safeSymlinkWithin(filepath.ToSlash(filepath.Dir(relative)), inspected.Target, base) {
				return fmt.Errorf("transport symlink %s escapes member root %s", relative, base)
			}
			entry.info = transportSymlinkInfo{
				name: filepath.Base(filepath.FromSlash(relative)),
				mode: os.ModeSymlink | inspected.Mode.Perm(),
				size: int64(len(inspected.Target)),
			}
		default:
			if inspected.File != nil {
				_ = inspected.File.Close()
			}
			return fmt.Errorf("unsupported transport entry %s", relative)
		}
		entries = append(entries, entry)
		if inspected.Kind == "directory" {
			pending = append(pending, len(entries)-1)
		}
		if inspected.File != nil {
			if err := inspected.File.Close(); err != nil {
				return err
			}
		}
		return nil
	}

	for _, base := range bases {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(entries) >= limit {
			return nil, fmt.Errorf("transport exceeds its %d-entry bound", limit)
		}
		inspected, err := inspectTransportRelative(root, base)
		if err != nil {
			return nil, fmt.Errorf("inspecting transport member %s: %w", base, err)
		}
		if err := ctx.Err(); err != nil {
			if inspected.File != nil {
				_ = inspected.File.Close()
			}
			return nil, err
		}
		if err := appendEntry(base, base, inspected); err != nil {
			return nil, err
		}
	}

	for cursor := 0; cursor < len(pending); cursor++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parent := entries[pending[cursor]]
		opened, err := inspectTransportRelative(root, parent.relative)
		if err != nil {
			return nil, fmt.Errorf("reopening transport directory %s: %w", parent.relative, err)
		}
		if err := validateTransportEntryUnchanged(parent, opened); err != nil {
			if opened.File != nil {
				_ = opened.File.Close()
			}
			return nil, err
		}
		directory := opened.File
		for {
			if err := ctx.Err(); err != nil {
				_ = directory.Close()
				return nil, err
			}
			children, readErr := directory.ReadDir(transportReadDirBatchSize)
			if err := ctx.Err(); err != nil {
				_ = directory.Close()
				return nil, err
			}
			for _, child := range children {
				if err := ctx.Err(); err != nil {
					_ = directory.Close()
					return nil, err
				}
				name := child.Name()
				if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
					_ = directory.Close()
					return nil, fmt.Errorf("unsafe transport child name %q", name)
				}
				// A checkout's administrative database is not a build input and can
				// carry mutable remotes/hooks. Preserve the historical policy of
				// excluding only the declared member root's immediate .git child.
				if parent.relative == parent.base && name == ".git" {
					continue
				}
				if len(entries) >= limit {
					_ = directory.Close()
					return nil, fmt.Errorf("transport exceeds its %d-entry bound", limit)
				}
				relative := filepath.ToSlash(filepath.Join(filepath.FromSlash(parent.relative), name))
				inspected, err := securepath.InspectChild(directory, name, relative)
				if err != nil {
					_ = directory.Close()
					return nil, err
				}
				if err := ctx.Err(); err != nil {
					if inspected.File != nil {
						_ = inspected.File.Close()
					}
					_ = directory.Close()
					return nil, err
				}
				if err := appendEntry(relative, parent.base, inspected); err != nil {
					_ = directory.Close()
					return nil, err
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = directory.Close()
				return nil, readErr
			}
		}
		if err := ctx.Err(); err != nil {
			_ = directory.Close()
			return nil, err
		}
		after, statErr := directory.Stat()
		closeErr := directory.Close()
		if statErr != nil {
			return nil, statErr
		}
		if !securepath.SameMetadata(parent.info, after) {
			return nil, fmt.Errorf("transport directory %s changed while being enumerated", parent.relative)
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return entries, nil
}

func inspectTransportRelative(root *os.File, relative string) (securepath.Entry, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if relative == "" || filepath.IsAbs(clean) || filepath.ToSlash(clean) != relative {
		return securepath.Entry{}, fmt.Errorf("unsafe transport path %q", relative)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	current := root
	owned := false
	for _, part := range parts[:len(parts)-1] {
		next, _, err := securepath.OpenChild(current, part, true)
		if owned {
			_ = current.Close()
		}
		if err != nil {
			return securepath.Entry{}, err
		}
		current, owned = next, true
	}
	inspected, err := securepath.InspectChild(current, parts[len(parts)-1], relative)
	if owned {
		closeErr := current.Close()
		if err == nil && closeErr != nil {
			if inspected.File != nil {
				_ = inspected.File.Close()
				inspected.File = nil
			}
			err = closeErr
		}
	}
	return inspected, err
}

func validateTransportEntryUnchanged(expected transportEntry, observed securepath.Entry) error {
	if expected.kind != observed.Kind {
		return fmt.Errorf("transport member %s changed type", expected.relative)
	}
	switch expected.kind {
	case "directory", "file":
		if !securepath.SameMetadata(expected.info, observed.Info) {
			return fmt.Errorf("transport member %s changed metadata", expected.relative)
		}
	case "symlink":
		if expected.link != observed.Target || expected.info.Mode().Perm() != observed.Mode.Perm() {
			return fmt.Errorf("transport symlink %s changed", expected.relative)
		}
	default:
		return fmt.Errorf("transport member %s has unsupported type %q", expected.relative, expected.kind)
	}
	return nil
}

// openTransportTarSource detects a gzip-compressed carrier (such as a real `helm
// package` .tgz) by its magic bytes and transparently decompresses it; an
// uncompressed carrier is read as plain tar. Detection only consumes from the
// returned buffered reader, so the caller sees the complete stream from the
// beginning regardless of which branch is taken. allowCompressed scopes gzip
// acceptance to the one caller that actually needs it (Helm chart carriers);
// every other transport consumer keeps requiring plain tar, so this widened
// decompression exposure and its budget do not silently apply to Packer,
// APT, or the generic `nvkr transport` carriers.
//
// maxHelmTransportDecompressedBytes bounds the decompressed size of the one
// gzip-compressed transport lane (a real `helm package` .tgz, read via
// DetectTransportChartRoot / DetectAndUnpackChartRoot). Unlike
// maxTransportBytes — sized for the full uncompressed workspace/VM carriers
// this package also transports — this lane's on-disk (compressed) size can
// be tens of MB while still decompressing to many times that (a
// "decompression bomb"), so it gets its own, Helm-chart-sized decompressed
// ceiling instead of inheriting the much larger aggregate budget.
const maxHelmTransportDecompressedBytes = 512 << 20

// The returned reader carries a single aggregate decompressed-byte budget: the
// gzip lane gets maxHelmTransportDecompressedBytes, every other (plain tar)
// lane keeps the full maxTransportBytes its on-disk size precondition already
// bounds 1:1. Without a smaller budget on the gzip lane, a small decompression
// bomb could expand without bound; the budget is charged for every
// decompressed byte, including data tar.Reader.Next skips between entries.
func openTransportTarSource(source io.Reader, allowCompressed bool) (io.Reader, func(), error) {
	buffered := bufio.NewReader(source)
	magic, err := buffered.Peek(2)
	if err != nil && err != io.EOF {
		return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "inspecting transport archive")
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		if !allowCompressed {
			return nil, nil, e.New(e.CodeVerifyFailed, "transport archive is gzip-compressed but this carrier requires plain tar")
		}
		gz, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "opening compressed transport archive")
		}
		return &transportLimitReader{reader: gz, remaining: maxHelmTransportDecompressedBytes, bound: maxHelmTransportDecompressedBytes},
			func() { _ = gz.Close() }, nil
	}
	return &transportLimitReader{reader: buffered, remaining: maxTransportBytes, bound: maxTransportBytes}, func() {}, nil
}

// transportLimitReader fails closed once an untrusted stream has yielded more
// than its aggregate byte budget, rather than truncating at the bound the way
// io.LimitedReader would. A stream that ends with exactly the budget consumed
// is not over budget: the boundary is confirmed with a one-byte probe read of
// the underlying stream rather than inferred merely from the counter reaching
// zero, so the reader does not reject a maximum-size but otherwise legal input.
type transportLimitReader struct {
	reader    io.Reader
	remaining int64
	bound     int64
}

func (reader *transportLimitReader) Read(p []byte) (int, error) {
	if reader.remaining <= 0 {
		probe := make([]byte, 1)
		n, err := reader.reader.Read(probe)
		if n > 0 {
			return 0, fmt.Errorf("transport archive exceeds its %d-byte decompressed bound", reader.bound)
		}
		return 0, err
	}
	if int64(len(p)) > reader.remaining {
		p = p[:reader.remaining]
	}
	n, err := reader.reader.Read(p)
	reader.remaining -= int64(n)
	return n, err
}

// detectTransportChartRootFrom scans an already-open tar/tar.gz stream and
// returns the name of its single top-level directory, without extracting any
// file contents or requiring the caller to know that name in advance. A real
// `helm package` archive has no fixed root name (it is rooted at the chart's
// own name), so a caller that requires a named allow-list (Unpack) must
// discover that name first. Multiple distinct top-level entries, or an archive
// with no top-level directory, fail closed; extraction independently
// re-validates every entry.
func detectTransportChartRootFrom(ctx context.Context, source io.Reader, allowCompressed bool) (string, error) {
	tarSource, closeTarSource, err := openTransportTarSource(source, allowCompressed)
	if err != nil {
		return "", err
	}
	defer closeTarSource()
	tr := tar.NewReader(tarSource)
	root := ""
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", e.Wrap(e.CodeTimeout, err, "inspecting transport canceled")
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", e.Wrap(e.CodeVerifyFailed, err, "reading transport archive")
		}
		entries++
		if entries > maxTransportEntries {
			return "", e.New(e.CodeVerifyFailed, "transport exceeds %d entries", maxTransportEntries)
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !filepath.IsLocal(name) {
			return "", e.New(e.CodeVerifyFailed, "transport contains non-local path %q", header.Name)
		}
		top := name
		if idx := strings.IndexByte(top, '/'); idx >= 0 {
			top = top[:idx]
		}
		if top == "" {
			continue
		}
		if root == "" {
			root = top
		} else if root != top {
			return "", e.New(e.CodeVerifyFailed,
				"transport archive has multiple top-level roots %q and %q", root, top)
		}
	}
	if root == "" {
		return "", e.New(e.CodeVerifyFailed, "transport archive has no top-level directory")
	}
	return root, nil
}

// DetectTransportChartRoot inspects an untrusted carrier and returns the name of
// its single top-level directory, without extracting any file contents. See
// detectTransportChartRootFrom. Prefer DetectAndUnpackChartRoot when the
// caller will immediately extract that same root: it holds one file
// descriptor across both steps instead of opening the path twice.
func DetectTransportChartRoot(ctx context.Context, archive string) (string, error) {
	if err := requireTransportContext(ctx, "inspecting transport"); err != nil {
		return "", err
	}
	file, info, err := securepath.OpenRegular(archive)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "opening transport archive %s", archive)
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > maxTransportBytes {
		return "", e.New(e.CodeVerifyFailed, "transport archive %s is not a regular file within the %d-byte limit", archive, maxTransportBytes)
	}
	root, err := detectTransportChartRootFrom(ctx, file, true)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "inspecting transport archive %s", archive)
	}
	return root, nil
}

// DetectAndUnpackChartRoot discovers a gzip-or-plain carrier's single
// top-level chart-root directory name and extracts exactly that root into a
// private staging directory beneath root, holding one file descriptor across
// both steps. A caller that opened the path twice instead (once to detect,
// once to extract) would leave a pathname-replacement window between the two
// opens; UnpackOpen's held-descriptor design exists specifically to close
// that window, so chart-root discovery must share its descriptor rather than
// reopen the path.
func DetectAndUnpackChartRoot(ctx context.Context, archive, root string) (string, error) {
	if err := requireTransportContext(ctx, "inspecting transport"); err != nil {
		return "", err
	}
	file, info, err := securepath.OpenRegular(archive)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "opening transport archive %s", archive)
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > maxTransportBytes {
		return "", e.New(e.CodeVerifyFailed, "transport archive %s is not a regular file within the %d-byte limit", archive, maxTransportBytes)
	}
	chartRoot, err := detectTransportChartRootFrom(ctx, file, true)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "inspecting transport archive %s", archive)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "rewinding transport archive %s", archive)
	}
	if err := unpackOpen(ctx, file, root, []string{chartRoot}, true); err != nil {
		return "", err
	}
	return chartRoot, nil
}

// Unpack extracts an untrusted carrier into a private staging directory,
// accepts only the producer's declared paths, and publishes them only after the full
// archive has been validated. Traversal, hard links, devices, duplicate entries,
// escaping symlinks, missing roots, and destination overwrite all fail closed.
// The carrier must be plain tar; a gzip-compressed carrier fails closed here
// (see DetectAndUnpackChartRoot for the one caller that needs compression).
func Unpack(ctx context.Context, archive, root string, allowed []string) error {
	if err := requireTransportContext(ctx, "unpacking transport"); err != nil {
		return err
	}
	archiveFile, _, err := securepath.OpenRegular(archive)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening transport archive %s", archive)
	}
	defer archiveFile.Close()
	return UnpackOpen(ctx, archiveFile, root, allowed)
}

// UnpackOpen applies the same bounded carrier validation as Unpack to an
// already-held regular-file inode. It does not close archive. This prevents a
// pathname replacement between an authority module holding transport bytes and
// the extraction boundary. The carrier must be plain tar.
func UnpackOpen(ctx context.Context, archiveFile *os.File, root string, allowed []string) error {
	return unpackOpen(ctx, archiveFile, root, allowed, false)
}

func unpackOpen(ctx context.Context, archiveFile *os.File, root string, allowed []string, allowCompressed bool) error {
	if err := requireTransportContext(ctx, "unpacking held transport"); err != nil {
		return err
	}
	rootAbs, cleanAllowed, err := transportRoots(root, allowed)
	if err != nil {
		return err
	}
	if archiveFile == nil {
		return e.New(e.CodeVerifyFailed, "held transport archive is absent")
	}
	archiveInfo, err := archiveFile.Stat()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "stating held transport archive")
	}
	if !archiveInfo.Mode().IsRegular() {
		return e.New(e.CodeVerifyFailed, "held transport archive is not a regular file")
	}
	if archiveInfo.Size() < 0 || archiveInfo.Size() > maxTransportBytes {
		return e.New(e.CodeVerifyFailed,
			"held transport archive has invalid size %d; expected 0 through %d bytes",
			archiveInfo.Size(), maxTransportBytes)
	}
	if err := os.MkdirAll(rootAbs, 0o755); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating transport root")
	}
	stage, err := os.MkdirTemp(rootAbs, ".nvkr-unpack-")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating private transport staging directory")
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return e.Wrap(e.CodeInternal, err, "securing transport staging directory")
	}
	snapshotDigest, file, err := snapshotTransport(ctx, archiveFile, archiveInfo,
		filepath.Join(stage, "carrier.tar"))
	if err != nil {
		return err
	}
	defer file.Close()

	// Bind extraction writes to the held staging directory, including resolution
	// through symlinks. Lexical validation alone is not a filesystem boundary.
	stageRoot, err := os.OpenRoot(stage)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening transport staging root")
	}
	defer func() { _ = stageRoot.Close() }()
	tarSource, closeTarSource, err := openTransportTarSource(file, allowCompressed)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening held transport archive")
	}
	defer closeTarSource()
	tr := tar.NewReader(tarSource)
	seen := map[string]bool{}
	directoryModes := map[string]fs.FileMode{}
	directories := map[string]bool{"": true}
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "unpacking transport canceled")
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading transport archive")
		}
		entries++
		if entries > maxTransportEntries {
			return e.New(e.CodeVerifyFailed, "transport exceeds %d entries", maxTransportEntries)
		}
		memberName := header.Name
		if header.Typeflag == tar.TypeDir {
			// GNU tar terminates directory names with one slash. Normalize only
			// that spelling before path validation and duplicate detection.
			memberName = strings.TrimSuffix(memberName, "/")
		}
		name, ok := safeArchiveName(memberName, cleanAllowed)
		if !ok || !filepath.IsLocal(name) || seen[name] {
			return e.New(e.CodeVerifyFailed, "transport contains unexpected or duplicate path %q", header.Name)
		}
		seen[name] = true
		destination := filepath.Join(stage, filepath.FromSlash(name))
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			directories[parent] = true
			// File-only archives (including Helm charts) omit directory headers.
			// Their implicit parents must not inherit the extractor's umask;
			// an explicit header, before or after this entry, retains precedence.
			if _, present := directoryModes[parent]; !present {
				directoryModes[parent] = 0o755
			}
		}
		if err := noSymlinkParents(stage, destination); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "transport path %s has a symlink parent", name)
		}
		if err := stageRoot.MkdirAll(filepath.Dir(filepath.FromSlash(name)), 0o755); err != nil {
			return e.Wrap(e.CodeInternal, err, "creating transport staging parent")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := stageRoot.Mkdir(filepath.FromSlash(name), 0o700); err != nil && !os.IsExist(err) {
				return e.Wrap(e.CodeVerifyFailed, err, "creating transport directory %s", name)
			}
			directoryModes[name] = fs.FileMode(header.Mode) & 0o777
			directories[name] = true
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maxTransportBytes {
				return e.New(e.CodeVerifyFailed, "transport file %s has invalid size %d", name, header.Size)
			}
			out, err := stageRoot.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(header.Mode)&0o777)
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "creating transport file %s", name)
			}
			written, copyErr := copyTransportExact(ctx, out, tr, header.Size)
			var modeErr, syncErr error
			if copyErr == nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					copyErr = ctxErr
				} else {
					// Creation permissions are filtered by the process umask.
					// Restore the archive mode on this held inode before syncing.
					modeErr = out.Chmod(fs.FileMode(header.Mode) & 0o777)
					if modeErr == nil {
						syncErr = out.Sync()
					}
				}
			}
			closeErr := out.Close()
			if copyErr != nil {
				return transportError(ctx, e.CodeVerifyFailed, copyErr, "copying transport file %s", name)
			}
			if modeErr != nil {
				return transportError(ctx, e.CodeInternal, modeErr, "setting transport file mode %s", name)
			}
			if syncErr != nil {
				return transportError(ctx, e.CodeInternal, syncErr, "syncing transport file %s", name)
			}
			if closeErr != nil {
				return transportError(ctx, e.CodeVerifyFailed, closeErr, "closing transport file %s", name)
			}
			if written != header.Size {
				return e.New(e.CodeVerifyFailed, "transport file %s is truncated", name)
			}
		case tar.TypeSymlink:
			base := containingTransportRoot(name, cleanAllowed)
			if !safeSymlinkWithin(filepath.ToSlash(filepath.Dir(name)), header.Linkname, base) {
				return e.New(e.CodeVerifyFailed, "transport symlink %s escapes member root %s", name, base)
			}
			if err := stageRoot.Symlink(header.Linkname, filepath.FromSlash(name)); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "creating transport symlink %s", name)
			}
		default:
			return e.New(e.CodeVerifyFailed, "transport path %s has forbidden tar type %d", name, header.Typeflag)
		}
	}
	modeNames := make([]string, 0, len(directoryModes))
	for name := range directoryModes {
		modeNames = append(modeNames, name)
	}
	sort.Slice(modeNames, func(i, j int) bool { return strings.Count(modeNames[i], "/") > strings.Count(modeNames[j], "/") })
	for _, name := range modeNames {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "unpacking transport canceled")
		}
		if err := stageRoot.Chmod(filepath.FromSlash(name), directoryModes[name]); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "setting transport directory mode %s", name)
		}
	}
	directoryNames := make([]string, 0, len(directories))
	for name := range directories {
		directoryNames = append(directoryNames, name)
	}
	sort.Slice(directoryNames, func(i, j int) bool {
		return strings.Count(directoryNames[i], "/") > strings.Count(directoryNames[j], "/")
	})
	for _, name := range directoryNames {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "persisting transport staging tree")
		}
		if err := syncTransportDirectory(filepath.Join(stage, filepath.FromSlash(name))); err != nil {
			return e.Wrap(e.CodeInternal, err, "persisting transport staging directory %s", name)
		}
	}
	currentDigest, currentInfo, err := stableTransportDigest(ctx, archiveFile, archiveInfo)
	if err != nil {
		return transportError(ctx, e.CodeVerifyFailed, err, "rechecking transport archive")
	}
	if !os.SameFile(archiveInfo, currentInfo) || currentDigest != snapshotDigest {
		return e.New(e.CodeVerifyFailed, "transport archive changed while unpacking")
	}
	return publishTransportRoots(ctx, stage, rootAbs, cleanAllowed, publishTransportNoReplace)
}

type transportRootPublisher func(stage, destination string) error

// publishTransportRoots performs every reversible parent/precondition operation
// before the first rename. Each rename is atomic no-replace and each parent is
// persisted before the next root is exposed. Ordinary publication failures roll
// back roots exposed by this invocation; a process crash can leave only
// unauthenticated carrier bytes, which consumers never treat as a receipt.
func publishTransportRoots(ctx context.Context, stage, root string, paths []string,
	publish transportRootPublisher,
) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "publishing transport roots requires a non-nil context")
	}
	if publish == nil {
		return e.New(e.CodeInternal, "transport root publisher is nil")
	}
	parents := make(map[string]bool)
	for _, allowedPath := range paths {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "unpacking transport canceled")
		}
		staged := filepath.Join(stage, filepath.FromSlash(allowedPath))
		if _, err := os.Lstat(staged); err != nil {
			return e.Wrap(e.CodeReleaseIncomplete, err,
				"transport is missing declared root %s", allowedPath)
		}
		destination := filepath.Join(root, filepath.FromSlash(allowedPath))
		parentHandle, finalName, err := securepath.CreateParentBeneath(root, filepath.FromSlash(allowedPath))
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err,
				"resolving transport destination %s beneath its root", allowedPath)
		}
		var destinationStat unix.Stat_t
		statErr := unix.Fstatat(int(parentHandle.Fd()), finalName, &destinationStat,
			unix.AT_SYMLINK_NOFOLLOW)
		closeErr := parentHandle.Close()
		if statErr == nil {
			return e.New(e.CodeImmutableViolation,
				"transport destination %s already exists", allowedPath)
		}
		if statErr != unix.ENOENT {
			return e.Wrap(e.CodeInternal, statErr,
				"checking transport destination %s", allowedPath)
		}
		if closeErr != nil {
			return e.Wrap(e.CodeInternal, closeErr,
				"closing transport destination parent %s", allowedPath)
		}
		parent := filepath.Dir(destination)
		for current := parent; ; current = filepath.Dir(current) {
			parents[current] = true
			if current == root {
				break
			}
			if next := filepath.Dir(current); next == current {
				return e.New(e.CodeUsage,
					"transport destination %s escapes root", allowedPath)
			}
		}
	}
	parentNames := make([]string, 0, len(parents))
	for parent := range parents {
		parentNames = append(parentNames, parent)
	}
	sort.Slice(parentNames, func(i, j int) bool {
		return strings.Count(parentNames[i], string(filepath.Separator)) >
			strings.Count(parentNames[j], string(filepath.Separator))
	})
	for _, parent := range parentNames {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "unpacking transport canceled")
		}
		if err := syncTransportDirectory(parent); err != nil {
			return e.Wrap(e.CodeInternal, err,
				"persisting transport destination parent")
		}
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "unpacking transport canceled")
	}

	published := make([]string, 0, len(paths))
	rollback := func(cause error) error {
		var rollbackErrors []error
		for index := len(published) - 1; index >= 0; index-- {
			if err := os.RemoveAll(published[index]); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
		for _, parent := range parentNames {
			if err := syncTransportDirectory(parent); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
		if len(rollbackErrors) == 0 {
			return cause
		}
		return e.Wrap(e.CodeImmutableViolation, errors.Join(append([]error{cause}, rollbackErrors...)...),
			"transport publication failed and rollback was incomplete")
	}
	for _, allowedPath := range paths {
		staged := filepath.Join(stage, filepath.FromSlash(allowedPath))
		destination := filepath.Join(root, filepath.FromSlash(allowedPath))
		if err := publish(staged, destination); err != nil {
			if errors.Is(err, errTransportNoReplaceUnsupported) {
				return rollback(e.Wrap(e.CodeEngineCapability, err,
					"publishing transported root %s", allowedPath))
			}
			return rollback(e.Wrap(e.CodeImmutableViolation, err,
				"publishing transported root %s without replacement", allowedPath))
		}
		published = append(published, destination)
		if err := syncTransportDirectory(filepath.Dir(destination)); err != nil {
			return rollback(e.Wrap(e.CodeImmutableViolation, err,
				"persisting transported root %s", allowedPath))
		}
	}
	return nil
}

func snapshotTransport(ctx context.Context, source *os.File, expected fs.FileInfo,
	destination string,
) (string, *os.File, error) {
	if source == nil || expected == nil {
		return "", nil, e.New(e.CodeInternal, "transport snapshot has no held source")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", nil, e.Wrap(e.CodeVerifyFailed, err, "rewinding held transport archive")
	}
	opened, err := source.Stat()
	if err != nil || !securepath.SameMetadata(expected, opened) {
		return "", nil, e.New(e.CodeVerifyFailed, "transport archive changed before snapshot")
	}
	out, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return "", nil, e.Wrap(e.CodeInternal, err, "creating private transport snapshot")
	}
	fail := func(err error) (string, *os.File, error) {
		_ = out.Close()
		_ = os.Remove(destination)
		return "", nil, err
	}
	if err := os.Remove(destination); err != nil {
		return fail(e.Wrap(e.CodeInternal, err, "unlinking private transport snapshot"))
	}
	hash := sha256.New()
	written, copyErr := copyTransportExact(ctx, io.MultiWriter(out, hash), source, expected.Size())
	after, statErr := source.Stat()
	if copyErr != nil {
		return fail(transportError(ctx, e.CodeVerifyFailed, copyErr, "snapshotting transport archive"))
	}
	if statErr != nil {
		return fail(transportError(ctx, e.CodeVerifyFailed, statErr, "restating transport archive"))
	}
	if written != expected.Size() || !securepath.SameMetadata(expected, after) {
		return fail(e.New(e.CodeVerifyFailed, "transport archive changed while snapshotting"))
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return fail(e.Wrap(e.CodeInternal, err, "rewinding private transport snapshot"))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), out, nil
}

func stableTransportDigest(ctx context.Context, file *os.File, expected fs.FileInfo) (string, fs.FileInfo, error) {
	if file == nil || expected == nil {
		return "", nil, fmt.Errorf("transport recheck has no held source")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", nil, err
	}
	before, err := file.Stat()
	if err != nil || !securepath.SameMetadata(expected, before) {
		return "", nil, fmt.Errorf("transport is no longer a regular file")
	}
	hash := sha256.New()
	written, err := copyTransportExact(ctx, hash, file, expected.Size())
	if err != nil {
		return "", nil, err
	}
	after, err := file.Stat()
	if err != nil || written != expected.Size() || !securepath.SameMetadata(expected, after) {
		return "", nil, fmt.Errorf("transport changed during recheck")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), after, nil
}

func copyTransport(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("transport copy requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	buffer := make([]byte, transportCopyBufferSize)
	reader := transportContextReader{ctx: ctx, reader: source}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := reader.Read(buffer)
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if n > 0 {
			written, writeErr := destination.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
			if err := ctx.Err(); err != nil {
				return total, err
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

type transportContextReader struct {
	ctx    context.Context
	reader io.Reader
}

type transportLimitWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *transportLimitWriter) Write(buffer []byte) (int, error) {
	if writer == nil || writer.writer == nil || writer.remaining < int64(len(buffer)) {
		return 0, fmt.Errorf("transport archive exceeds its %d-byte bound", maxTransportBytes)
	}
	written, err := writer.writer.Write(buffer)
	writer.remaining -= int64(written)
	return written, err
}

func (reader transportContextReader) Read(p []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(p)
}

func copyTransportExact(ctx context.Context, destination io.Writer, source io.Reader, size int64) (int64, error) {
	if size < 0 {
		return 0, fmt.Errorf("transport copy size %d is negative", size)
	}
	limited := &io.LimitedReader{R: source, N: size}
	written, err := copyTransport(ctx, destination, limited)
	if err != nil {
		return written, err
	}
	if limited.N != 0 || written != size {
		return written, io.ErrUnexpectedEOF
	}
	return written, nil
}

// CopyExact reuses the carrier's bounded, context-aware copy primitive. It copies
// only size bytes and rejects a short source; it grants no filesystem authority.
func CopyExact(ctx context.Context, destination io.Writer, source io.Reader, size int64) (int64, error) {
	if err := requireTransportContext(ctx, "copying bounded transport bytes"); err != nil {
		return 0, err
	}
	written, err := copyTransportExact(ctx, destination, source, size)
	if err != nil {
		return written, transportError(ctx, e.CodeVerifyFailed, err, "copying bounded transport bytes")
	}
	return written, nil
}

func noSymlinkParents(root, destination string) error {
	relative, err := filepath.Rel(root, filepath.Dir(destination))
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", current)
		}
	}
	return nil
}

func transportArchiveRelative(root, archive string, members []string) (string, error) {
	archiveAbs, err := filepath.Abs(archive)
	if err != nil {
		return "", e.Wrap(e.CodeUsage, err, "resolving transport archive")
	}
	relative, err := filepath.Rel(root, archiveAbs)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", e.New(e.CodeUsage,
			"transport archive %s must be a file beneath transport root %s", archive, root)
	}
	relative = filepath.Clean(relative)
	for _, member := range members {
		memberPath := filepath.FromSlash(member)
		if relative == memberPath || strings.HasPrefix(relative, memberPath+string(filepath.Separator)) {
			return "", e.New(e.CodeUsage,
				"transport archive %s overlaps declared member %s", archive, member)
		}
	}
	return relative, nil
}

func transportRoots(root string, paths []string) (string, []string, error) {
	if len(paths) == 0 {
		return "", nil, e.New(e.CodeUsage, "transport requires at least one path")
	}
	if len(paths) > maxTransportRoots {
		return "", nil, e.New(e.CodeUsage,
			"transport exceeds its %d-root bound", maxTransportRoots)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", nil, e.Wrap(e.CodeUsage, err, "resolving transport root")
	}
	clean := make([]string, 0, len(paths))
	for _, raw := range paths {
		if strings.TrimSpace(raw) == "" {
			return "", nil, e.New(e.CodeTrustPolicyMissing, "transport requires a member path")
		}
		if !securepath.WorkspaceRelative(raw) {
			return "", nil, e.New(e.CodeInvalidCatalog, "transport member %q must be a safe workspace-relative path", raw)
		}
		value := filepath.ToSlash(filepath.Clean(raw))
		clean = append(clean, value)
	}
	sort.Strings(clean)
	for index := 1; index < len(clean); index++ {
		previous, value := clean[index-1], clean[index]
		if value == previous || strings.HasPrefix(value, previous+"/") {
			return "", nil, e.New(e.CodeUsage,
				"transport roots %q and %q overlap", previous, value)
		}
	}
	return rootAbs, clean, nil
}

func syncTransportDirectory(path string) error {
	directory, _, err := securepath.OpenDirectory(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func safeArchiveName(raw string, allowed []string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || clean != raw {
		return "", false
	}
	return clean, containingTransportRoot(clean, allowed) != ""
}

func containingTransportRoot(name string, roots []string) string {
	for _, root := range roots {
		if name == root || strings.HasPrefix(name, root+"/") {
			return root
		}
	}
	return ""
}

func safeSymlinkWithin(parent, target, base string) bool {
	if target == "" || filepath.IsAbs(target) || strings.ContainsRune(target, '\x00') {
		return false
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.FromSlash(parent), target)))
	return resolved == base || strings.HasPrefix(resolved, base+"/")
}

func transportError(ctx context.Context, code e.Code, err error, format string, args ...any) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return e.Wrap(e.CodeTimeout, ctxErr, format, args...)
		}
	}
	return e.Wrap(code, err, format, args...)
}

func requireTransportContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}
