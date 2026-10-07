// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// RegistryFile is the hand-authored catalog entrypoint every provider serves.
// LockFile is the machine-managed pin lock; a provider may omit it (from-source
// components then fail closed as unpinned).
const (
	RegistryFile = "registry.yaml"
	LockFile     = "pins.lock.yaml"

	// MaxCatalogFileBytes is the per-file memory and I/O ceiling at the catalog
	// authority boundary. MaxCatalogTotalBytes additionally bounds one deep load.
	MaxCatalogFileBytes  int64 = 4 << 20
	MaxCatalogTotalBytes int64 = 32 << 20

	// MaxCatalogDirectoryEntries bounds the aggregate number of entries returned
	// by a deep load. MaxCatalogTraversalDepth bounds directories beneath one
	// requested overlay/mixin root.
	MaxCatalogDirectoryEntries = 1024
	MaxCatalogTraversalDepth   = 16
)

// DataProvider is the synchronous parser seam for diagnostic and in-memory inputs.
// Authority-bearing I/O uses ContextDataProvider and binds one context and resource
// budget across every deep read. Directory providers implement both interfaces.
type DataProvider interface {
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]string, error)
	Name() string
}

// ContextDataProvider is the authoritative catalog I/O seam. Implementations must
// honor cancellation during blocking work and return only bounded regular files and
// deterministic, bounded directory listings. ValidateFilePayload and
// ValidateDirectoryListing define the adapter result contract.
type ContextDataProvider interface {
	DataProvider
	ReadFileContext(context.Context, string) ([]byte, error)
	ReadDirContext(context.Context, string) ([]string, error)
}

// dirProvider resolves every path component beneath one held root without following
// symlinks. Platform helpers use descriptor-relative opens on supported Unix hosts;
// unsupported hosts fail closed rather than falling back to pathname checks.
type dirProvider struct{ root string }

func (provider dirProvider) ReadFile(name string) ([]byte, error) {
	return provider.ReadFileContext(context.Background(), name)
}

func (provider dirProvider) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	if err := validateCatalogPath(name); err != nil {
		return nil, err
	}
	if err := requireCatalogContext(ctx, "catalog file read"); err != nil {
		return nil, err
	}
	return readCatalogRegularBeneathContext(ctx, provider.root, name)
}

func (provider dirProvider) ReadDir(name string) ([]string, error) {
	return provider.ReadDirContext(context.Background(), name)
}

func (provider dirProvider) ReadDirContext(ctx context.Context, name string) ([]string, error) {
	if err := validateCatalogPath(name); err != nil {
		return nil, err
	}
	if err := requireCatalogContext(ctx, "catalog directory read"); err != nil {
		return nil, err
	}
	files, err := readCatalogDirectoryBeneathContext(ctx, provider.root, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(files)
	if err := ValidateDirectoryListing(name, files); err != nil {
		return nil, err
	}
	return files, nil
}

func (provider dirProvider) Name() string { return "dir:" + provider.root }

// Dir returns a bounded, no-follow provider backed by an on-disk catalog directory
// (--data). ContextDataProvider is implemented even though the compatibility return
// type remains DataProvider.
func Dir(dir string) DataProvider { return dirProvider{root: dir} }

// ValidateFilePayload validates the result of a ContextDataProvider file read.
// Adapters may use it before returning caller-controlled bytes.
func ValidateFilePayload(name string, raw []byte) error {
	if err := validateCatalogPath(name); err != nil {
		return err
	}
	if int64(len(raw)) > MaxCatalogFileBytes {
		return e.New(e.CodeInvalidCatalog, "catalog file %q exceeds the %d-byte bound", name, MaxCatalogFileBytes)
	}
	return nil
}

// ValidateDirectoryListing validates one provider-relative recursive listing. Names
// must be unique, safe, deterministically bounded relative paths. Callers may sort a
// defensive copy after validation.
func ValidateDirectoryListing(name string, entries []string) error {
	if err := validateCatalogPath(name); err != nil {
		return err
	}
	if len(entries) > MaxCatalogDirectoryEntries {
		return e.New(e.CodeInvalidCatalog, "catalog directory %q exceeds the %d-entry bound", name, MaxCatalogDirectoryEntries)
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := validateCatalogPath(entry); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "catalog directory %q returned unsafe entry", name)
		}
		if pathDepth(entry)-1 > MaxCatalogTraversalDepth {
			return e.New(e.CodeInvalidCatalog, "catalog directory %q entry %q exceeds the depth-%d bound", name, entry, MaxCatalogTraversalDepth)
		}
		if _, duplicate := seen[entry]; duplicate {
			return e.New(e.CodeInvalidCatalog, "catalog directory %q repeats entry %q", name, entry)
		}
		seen[entry] = struct{}{}
	}
	return nil
}

// BindContext returns a parser provider whose every deep call is routed to
// the provider's context-aware interface and whose aggregate bytes/listings are
// bounded for one catalog operation.
func BindContext(ctx context.Context, provider DataProvider) (DataProvider, error) {
	if err := requireCatalogContext(ctx, "catalog load"); err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, e.New(e.CodeInvalidCatalog, "catalog provider is nil")
	}
	contextProvider, ok := provider.(ContextDataProvider)
	if !ok {
		return nil, e.New(e.CodeEngineCapability,
			"catalog provider %q has no bounded context-aware authority interface", provider.Name())
	}
	return &boundContextProvider{ctx: ctx, provider: contextProvider}, nil
}

type boundContextProvider struct {
	ctx      context.Context
	provider ContextDataProvider

	mu               sync.Mutex
	totalBytes       int64
	directoryEntries int
}

func (provider *boundContextProvider) ReadFile(name string) ([]byte, error) {
	if err := requireCatalogContext(provider.ctx, "catalog file read"); err != nil {
		return nil, err
	}
	raw, err := provider.provider.ReadFileContext(provider.ctx, name)
	if err != nil {
		return nil, err
	}
	if err := ValidateFilePayload(name, raw); err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.totalBytes+int64(len(raw)) > MaxCatalogTotalBytes {
		return nil, e.New(e.CodeInvalidCatalog, "catalog load exceeds the %d-byte aggregate bound", MaxCatalogTotalBytes)
	}
	provider.totalBytes += int64(len(raw))
	return append([]byte(nil), raw...), nil
}

func (provider *boundContextProvider) ReadDir(name string) ([]string, error) {
	if err := requireCatalogContext(provider.ctx, "catalog directory read"); err != nil {
		return nil, err
	}
	entries, err := provider.provider.ReadDirContext(provider.ctx, name)
	if err != nil {
		return nil, err
	}
	if err := ValidateDirectoryListing(name, entries); err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.directoryEntries+len(entries) > MaxCatalogDirectoryEntries {
		return nil, e.New(e.CodeInvalidCatalog, "catalog load exceeds the %d-entry aggregate bound", MaxCatalogDirectoryEntries)
	}
	provider.directoryEntries += len(entries)
	result := append([]string(nil), entries...)
	sort.Strings(result)
	return result, nil
}

func (provider *boundContextProvider) Name() string { return provider.provider.Name() }

func validateCatalogPath(name string) error {
	if name == "" || name == "." || !fs.ValidPath(name) || strings.Contains(name, `\`) {
		return e.New(e.CodeInvalidCatalog, "unsafe catalog-relative path %q", name)
	}
	if pathDepth(name) > MaxCatalogTraversalDepth+1 {
		return e.New(e.CodeInvalidCatalog, "catalog-relative path %q exceeds the depth-%d bound", name, MaxCatalogTraversalDepth)
	}
	return nil
}

func pathDepth(name string) int { return strings.Count(name, "/") + 1 }

func requireCatalogContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeTimeout, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

func normalizeCatalogContextError(ctx context.Context, err error, operation string) error {
	if ctx == nil {
		return e.New(e.CodeTimeout, "%s requires a non-nil context", operation)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return e.Wrap(e.CodeTimeout, contextErr, "%s canceled", operation)
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return err
}

type catalogContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader catalogContextReader) Read(destination []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if len(destination) > 128<<10 {
		destination = destination[:128<<10]
	}
	return reader.reader.Read(destination)
}

func readCatalogFile(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	if err := requireCatalogContext(ctx, "catalog file read"); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(catalogContextReader{ctx: ctx, reader: reader}, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("catalog file exceeds the %d-byte bound", limit)
	}
	if err := requireCatalogContext(ctx, "catalog file read"); err != nil {
		return nil, err
	}
	return raw, nil
}

type catalogWalkState struct {
	entries int
	files   []string
}

func readCatalogDirectoryEntries(ctx context.Context, directory fs.ReadDirFile, state *catalogWalkState) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	for {
		if err := requireCatalogContext(ctx, "catalog directory read"); err != nil {
			return nil, err
		}
		batch, err := directory.ReadDir(128)
		if len(batch) == 0 && err == nil {
			return nil, fmt.Errorf("catalog directory reader made no progress")
		}
		state.entries += len(batch)
		if state.entries > MaxCatalogDirectoryEntries {
			return nil, fmt.Errorf("catalog directory exceeds the %d-entry bound", MaxCatalogDirectoryEntries)
		}
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	return entries, nil
}
