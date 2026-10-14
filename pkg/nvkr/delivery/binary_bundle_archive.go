// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func (writer layoutWriter) addBundleLayer(ctx context.Context, captures []capturedBinary, epoch int64) (Descriptor, error) {
	tmp, err := os.CreateTemp(writer.root, ".nvkr-bundle-layer-*.tmp")
	if err != nil {
		return Descriptor{}, e.Wrap(e.CodeInternal, err, "creating binary bundle layer")
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	hash := sha256.New()
	gzipWriter, err := gzip.NewWriterLevel(io.MultiWriter(tmp, hash), gzip.BestCompression)
	if err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	gzipWriter.ModTime = time.Time{}
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	mtime := time.Unix(epoch, 0).UTC()
	for _, capture := range captures {
		if err := deliveryContext(ctx); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, err
		}
		header := &tar.Header{Name: "files/" + capture.input.Name, Mode: int64(capture.input.Mode),
			Size: capture.file.Size(), ModTime: mtime, Uid: 0, Gid: 0,
			Uname: "", Gname: "", Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, err
		}
		if _, err := capture.file.WriteToContext(ctx, tarWriter); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, classifyContext(ctx, err)
		}
		if err := capture.file.AssertUnchangedContext(ctx); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		_ = tmp.Close()
		return Descriptor{}, err
	}
	if err := gzipWriter.Close(); err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	info, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	if err := tmp.Close(); err != nil {
		return Descriptor{}, err
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	destination := filepath.Join(writer.root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return Descriptor{}, err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return Descriptor{}, err
	}
	return Descriptor{MediaType: MediaBinaryBundleLayer, Digest: digest, Size: info.Size()}, nil
}

func extractBundleLayer(ctx context.Context, held *heldfile.File, out string, bundle BinaryBundle) error {
	return walkBundleLayer(ctx, held, bundle, func(entry BinaryEntry, payload io.Reader) error {
		file, err := os.OpenFile(filepath.Join(out, entry.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(entry.Mode))
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "creating extracted binary %q", entry.Name)
		}
		_, copyErr := io.Copy(file, payload)
		if copyErr == nil {
			copyErr = file.Sync()
		}
		if err := errors.Join(copyErr, file.Close()); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "extracting binary %q", entry.Name)
		}
		return nil
	})
}

// walkBundleLayer owns the complete archive grammar for extraction and held
// member streaming. A visitor must consume its bounded member synchronously;
// neither the reader nor a partial write establishes a successful check.
func walkBundleLayer(ctx context.Context, held *heldfile.File, bundle BinaryBundle, visit func(BinaryEntry, io.Reader) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if held == nil || visit == nil {
		return e.New(e.CodeGateUnmet, "binary-bundle traversal requires held bytes and a visitor")
	}
	if len(bundle.Entries) > maxDeliveryObjects {
		return e.New(e.CodeEngineCapability, "binary-bundle entry count exceeds its bound")
	}
	want := make(map[string]BinaryEntry, len(bundle.Entries))
	for _, entry := range bundle.Entries {
		if !canonicalToken(entry.Name) || strings.ContainsAny(entry.Name, "/\\") || entry.Name == "." || entry.Name == ".." ||
			entry.Size < 0 || entry.Mode == 0 || entry.Mode > 0o777 || !isDigest(entry.SHA256) {
			return e.New(e.CodeVerifyFailed, "binary-bundle entry is not an exact regular-file binding")
		}
		if entry.Size > maxDeliveryFileBytes {
			return e.New(e.CodeEngineCapability, "binary-bundle member %q exceeds its byte bound", entry.Name)
		}
		name := "files/" + entry.Name
		if _, exists := want[name]; exists {
			return e.New(e.CodeVerifyFailed, "binary-bundle config repeats member %q", entry.Name)
		}
		want[name] = entry
	}
	gzipReader, err := gzip.NewReader(contextReader{ctx: ctx, reader: io.NewSectionReader(held, 0, held.Size())})
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "opening binary-bundle gzip layer"))
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(contextReader{ctx: ctx, reader: gzipReader})
	seen := map[string]bool{}
	for {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading binary-bundle tar layer"))
		}
		entry, ok := want[header.Name]
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size != entry.Size || header.Mode != int64(entry.Mode) {
			return e.New(e.CodeVerifyFailed, "binary-bundle tar contains an undeclared or mismatched entry %q", header.Name)
		}
		seen[header.Name] = true
		hash := sha256.New()
		member := &io.LimitedReader{R: tarReader, N: entry.Size}
		if err := visit(entry, io.TeeReader(member, hash)); err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading binary-bundle member %q", entry.Name))
		}
		got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if member.N != 0 || got != entry.SHA256 {
			return e.New(e.CodeVerifyFailed, "binary-bundle member %q does not match its config", entry.Name)
		}
	}
	if len(seen) != len(want) {
		missing := make([]string, 0)
		for name := range want {
			if !seen[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return e.New(e.CodeReleaseIncomplete, "binary-bundle layer omits entries: %s", strings.Join(missing, ", "))
	}
	return deliveryContext(ctx)
}
