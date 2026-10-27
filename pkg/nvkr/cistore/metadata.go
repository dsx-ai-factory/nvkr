// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

const (
	metadataSchema = "nvkr.dev/ci-metadata/v1"
	// MaxMetadataRecordBytes bounds a public record including JSON escaping.
	MaxMetadataRecordBytes = 32 << 20
	MaxMetadataTextBytes   = 16 << 20
	MaxMetadataEntries     = 4096
	maxGitHubIndexBytes    = 128 << 20
)

// MetadataEntry describes exact text bytes, never an executable or opaque blob.
// The factory owner must additionally admit the complete artifact-specific schema
// and inventory before publishing. Transport syntax is not artifact authority.
type MetadataEntry struct {
	Path      string `json:"path"`
	Mode      int64  `json:"mode"`
	Directory bool   `json:"directory,omitempty"`
	Text      string `json:"text,omitempty"`
}

type MetadataContent struct {
	Root    string          `json:"root"`
	Entries []MetadataEntry `json:"entries"`
}

// ValidateGitHubRequest checks identity and cancellation without requiring a
// registry credential. Private transfer admission remains separate and mandatory.
func ValidateGitHubRequest(ctx context.Context, request Request) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "CI transfer requires context")
	}
	if ctx.Err() != nil {
		return safeIOError(ctx, e.CodeTimeout, "CI transfer canceled")
	}
	return request.validate()
}

// PublishGitHubMetadata publishes an already schema-admitted metadata snapshot.
// It never contacts GHCR or accepts storage credentials. The factory export is
// the policy boundary; this layer enforces text, size and safe restoration paths.
func PublishGitHubMetadata(ctx context.Context, request Request, member, directory string, content MetadataContent) (string, error) {
	if ctx == nil {
		return "", e.New(e.CodeUsage, "metadata export requires context")
	}
	content.Entries = slices.Clone(content.Entries)
	l := Locator{SchemaVersion: metadataSchema, Request: request, Paths: []string{member}, StripPrefix: path.Dir(member), Metadata: &content}
	if l.StripPrefix == "." {
		l.StripPrefix = ""
	}
	if err := l.Validate(request); err != nil {
		return "", err
	}
	raw, err := canonical.Marshal(l)
	if err != nil {
		return "", err
	}
	handle, info, err := securepath.OpenDirectory(directory)
	if err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "holding metadata output directory")
	}
	defer handle.Close()
	if err := writeGitHubLocatorSet(ctx, directory, handle, info, GitHubLocatorSelection{Name: request.Name}, []githubLocatorBytes{{name: request.Name, raw: raw}}); err != nil {
		return "", err
	}
	return path.Join(directory, request.Name+locatorSuffix), nil
}

func (l Locator) validateMetadata(request Request) error {
	if l.SchemaVersion != metadataSchema || l.Request != request || l.Metadata == nil || l.Reference != "" || l.ArchiveDigest != "" || l.ArchiveSize != 0 || len(l.Paths) != 1 || !safePath(l.Paths[0]) {
		return e.New(e.CodeVerifyFailed, "public metadata record has an invalid transport shape")
	}
	prefix := path.Dir(l.Paths[0])
	if prefix == "." {
		prefix = ""
	}
	if l.StripPrefix != prefix || !strings.HasPrefix(l.Paths[0], ".nvkr/carriers/") || !strings.HasSuffix(l.Paths[0], ".tar") {
		return e.New(e.CodeVerifyFailed, "public metadata requires one exact factory carrier")
	}
	m := l.Metadata
	if !safePath(m.Root) || len(m.Entries) == 0 || len(m.Entries) > MaxMetadataEntries {
		return e.New(e.CodeVerifyFailed, "public metadata has an invalid inventory")
	}
	seen := map[string]bool{}
	total, previous := 0, ""
	for _, entry := range m.Entries {
		if !safePath(entry.Path) || entry.Path <= previous || (entry.Path != m.Root && !strings.HasPrefix(entry.Path, m.Root+"/")) {
			return e.New(e.CodeVerifyFailed, "public metadata has an unordered or foreign member")
		}
		if entry.Path != m.Root && !seen[path.Dir(entry.Path)] {
			return e.New(e.CodeVerifyFailed, "public metadata omits a parent directory")
		}
		previous = entry.Path
		seen[entry.Path] = entry.Directory
		if entry.Directory {
			if entry.Text != "" || (entry.Mode != 0o755 && entry.Mode != 0o700) {
				return e.New(e.CodeVerifyFailed, "public metadata has invalid directory metadata")
			}
			continue
		}
		if entry.Mode != 0o644 && entry.Mode != 0o600 {
			return e.New(e.CodeVerifyFailed, "public metadata contains an executable or unsupported file mode")
		}
		total += len(entry.Text)
		if len(entry.Text) == 0 || len(entry.Text) > 8<<20 || total > MaxMetadataTextBytes || !utf8.ValidString(entry.Text) {
			return e.New(e.CodeVerifyFailed, "public metadata exceeds its text bound")
		}
		if canonical.ValidateJSONDocument([]byte(entry.Text)) != nil {
			ref := strings.TrimSuffix(entry.Text, "\n")
			digest, err := name.NewDigest(ref, name.StrictValidation)
			if err != nil || digest.Name() != ref || !validSHA256(digest.DigestStr()) || len(ref) > 4096 {
				return e.New(e.CodeVerifyFailed, "public metadata contains neither JSON nor an exact OCI reference")
			}
		}
	}
	if m.Entries[0].Path != m.Root {
		return e.New(e.CodeVerifyFailed, "public metadata omits its root")
	}
	raw, err := canonical.Marshal(l)
	if err != nil || len(raw) > MaxMetadataRecordBytes {
		return e.New(e.CodeVerifyFailed, "public metadata exceeds its record bound")
	}
	return nil
}

func metadataArchive(ctx context.Context, content *MetadataContent) ([]byte, error) {
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	for _, entry := range content.Entries {
		if ctx.Err() != nil {
			return nil, safeIOError(ctx, e.CodeTimeout, "metadata restoration canceled")
		}
		h := &tar.Header{Name: entry.Path, Mode: entry.Mode, Size: int64(len(entry.Text)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if entry.Directory {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := w.WriteHeader(h); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "frame metadata archive")
		}
		if !entry.Directory {
			if _, err := w.Write([]byte(entry.Text)); err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "frame metadata document")
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "finish metadata archive")
	}
	return buffer.Bytes(), nil
}

func restoreMetadata(ctx context.Context, request Request, l Locator, destination string, selection *NestedArchive) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "metadata restoration requires context")
	}
	if err := l.Validate(request); err != nil {
		return err
	}
	inner, err := metadataArchive(ctx, l.Metadata)
	if err != nil {
		return err
	}
	if selection != nil {
		if !slices.Equal(selection.Allowed, []string{l.Metadata.Root}) {
			return e.New(e.CodeVerifyFailed, "public metadata differs from the Plan-selected root")
		}
		return transportarchive.UnpackStream(ctx, bytes.NewReader(inner), int64(len(inner)), fmt.Sprintf("sha256:%x", sha256.Sum256(inner)), destination, selection.Allowed)
	}
	// The finalizer's existing recovery interface consumes one carrier file.
	// Only this small compatibility projection materializes a metadata TAR.
	var outer bytes.Buffer
	w := tar.NewWriter(&outer)
	member := path.Base(l.Paths[0])
	if err := w.WriteHeader(&tar.Header{Name: member, Mode: 0o644, Size: int64(len(inner)), Typeflag: tar.TypeReg}); err != nil {
		return e.Wrap(e.CodeInternal, err, "frame metadata carrier")
	}
	if _, err := w.Write(inner); err != nil {
		return e.Wrap(e.CodeInternal, err, "write metadata carrier")
	}
	if err := w.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "finish metadata carrier")
	}
	raw := outer.Bytes()
	return transportarchive.UnpackStream(ctx, bytes.NewReader(raw), int64(len(raw)), fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), destination, []string{member})
}
