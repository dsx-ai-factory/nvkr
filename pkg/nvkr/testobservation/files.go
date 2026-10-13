// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package testobservation owns captured portable test-result bytes. A Snapshot
// proves only file integrity against supplied pins; it is not an authenticated
// suite receipt and grants no signing, qualification or release authority.
package testobservation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const (
	MaxFiles            = 256
	MaxFileBytes  int64 = 64 << 20
	MaxTotalBytes int64 = 256 << 20
	maxPathBytes        = 1024
	maxPathDepth        = 8
)

// FileInput selects one regular file beneath the capture root. ExpectedDigest
// pins exact raw bytes independently of the source pathname, not their trust.
type FileInput struct {
	Name           string `json:"name"`
	ExpectedDigest string `json:"expectedDigest"`
	Role           string `json:"role"`
}

// FileDescription is an integrity observation, not a registered wire schema.
type FileDescription struct {
	Name   string
	Digest string
	Size   int64
}

type capturedFile struct {
	description FileDescription
	file        *heldfile.File
}

// Snapshot owns immutable held files. Source replacement after capture cannot
// change them. Close invalidates access; serialization cannot restore ownership.
type Snapshot struct {
	files     []capturedFile
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// Capture admits all selected paths and byte pins before filesystem access.
// Files must already be the reviewed public projection: this function performs
// no redaction, copies no unselected diagnostics and issues no suite verdict.
func Capture(ctx context.Context, root string, inputs []FileInput) (*Snapshot, error) {
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	if root == "" || len(inputs) == 0 || len(inputs) > MaxFiles {
		return nil, e.New(e.CodeUsage, "test observation requires a root and 1 through %d selected files", MaxFiles)
	}
	selected := append([]FileInput(nil), inputs...)
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	for i, input := range selected {
		if !capturePath(input.Name) || !canonical.IsDigest(input.ExpectedDigest) || (i > 0 && selected[i-1].Name == input.Name) {
			return nil, e.New(e.CodeUnpinnedInput, "test observation requires unique safe relative paths and exact file pins")
		}
	}
	directory, originalRoot, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, captureError(ctx, err, "opening test-result root")
	}
	defer directory.Close()
	snapshot := &Snapshot{}
	complete := false
	defer func() {
		if !complete {
			_ = snapshot.Close()
		}
	}()
	var total int64
	for _, input := range selected {
		if err := captureContext(ctx); err != nil {
			return nil, err
		}
		file, err := captureFile(ctx, directory, input, MaxTotalBytes-total)
		if err != nil {
			return nil, err
		}
		snapshot.files = append(snapshot.files, capturedFile{
			description: FileDescription{Name: input.Name, Digest: file.Digest(), Size: file.Size()}, file: file,
		})
		total += file.Size()
	}
	currentRoot, rootInfo, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, captureError(ctx, err, "rechecking test-result root")
	}
	_ = currentRoot.Close()
	if !os.SameFile(originalRoot, rootInfo) {
		return nil, e.New(e.CodeVerifyFailed, "test-result root was replaced during capture")
	}
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	complete = true
	return snapshot, nil
}

func capturePath(name string) bool {
	if name == "" || len(name) > maxPathBytes || strings.Count(name, "/") >= maxPathDepth {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
				return false
			}
		}
	}
	return true
}

func captureFile(ctx context.Context, root *os.File, input FileInput, remaining int64) (*heldfile.File, error) {
	parts := strings.Split(input.Name, "/")
	parent := root
	var parents []*os.File
	var parentInfo []os.FileInfo
	defer func() {
		for _, directory := range parents {
			_ = directory.Close()
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		if err := captureContext(ctx); err != nil {
			return nil, err
		}
		child, info, err := securepath.OpenChild(parent, part, true)
		if err != nil {
			return nil, captureError(ctx, err, "opening test-result parent")
		}
		parents = append(parents, child)
		parentInfo = append(parentInfo, info)
		parent = child
	}
	source, before, err := securepath.OpenChild(parent, parts[len(parts)-1], false)
	if err != nil {
		return nil, captureError(ctx, err, "opening test-result file")
	}
	defer source.Close()
	limit := min(MaxFileBytes, remaining)
	if limit <= 0 || before.Size() > limit {
		return nil, e.New(e.CodeVerifyFailed, "test-result files exceed the capture byte budget")
	}
	held, err := heldfile.FromReaderContext(ctx, source, heldfile.Options{
		Label: "test-result", MaxBytes: limit, ExpectedDigest: input.ExpectedDigest,
	})
	if err != nil {
		return nil, captureError(ctx, err, "capturing pinned test-result bytes")
	}
	after, err := source.Stat()
	if err != nil || !securepath.SameMetadata(before, after) || held.Size() != before.Size() {
		_ = held.Close()
		return nil, e.New(e.CodeVerifyFailed, "test-result file changed while captured")
	}
	if err := recheckCapturePath(ctx, root, parts, parentInfo, before); err != nil {
		_ = held.Close()
		return nil, err
	}
	return held, nil
}

func recheckCapturePath(ctx context.Context, root *os.File, parts []string, directories []os.FileInfo, original os.FileInfo) error {
	parent := root
	var opened []*os.File
	defer func() {
		for _, file := range opened {
			_ = file.Close()
		}
	}()
	for i, part := range parts {
		if err := captureContext(ctx); err != nil {
			return err
		}
		isDirectory := i < len(parts)-1
		current, info, err := securepath.OpenChild(parent, part, isDirectory)
		if err != nil {
			return captureError(ctx, err, "rechecking test-result path")
		}
		opened = append(opened, current)
		unchanged := false
		if isDirectory {
			unchanged = os.SameFile(directories[i], info)
		} else {
			unchanged = securepath.SameMetadata(original, info)
		}
		if !unchanged {
			return e.New(e.CodeVerifyFailed, "test-result path was replaced during capture")
		}
		parent = current
	}
	return nil
}

// Files returns a defensive sorted description of the captured exact bytes.
func (s *Snapshot) Files(ctx context.Context) ([]FileDescription, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	result := make([]FileDescription, len(s.files))
	for i, file := range s.files {
		result[i] = file.description
	}
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// Read returns a defensive copy of one exact selected file, bounded by capture
// admission. It neither revisits the source pathname nor returns an OS handle.
func (s *Snapshot) Read(ctx context.Context, name string) ([]byte, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	i := sort.Search(len(s.files), func(i int) bool { return s.files[i].description.Name >= name })
	if i == len(s.files) || s.files[i].description.Name != name {
		return nil, e.New(e.CodeVerifyFailed, "test-result file is outside the selected capture")
	}
	file := s.files[i].file
	raw, err := file.ReadAllContext(ctx, MaxFileBytes)
	if err != nil {
		return nil, captureError(ctx, err, "reading captured test-result bytes")
	}
	if int64(len(raw)) != s.files[i].description.Size || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != s.files[i].description.Digest {
		return nil, e.New(e.CodeVerifyFailed, "captured test-result bytes changed")
	}
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Snapshot) check(ctx context.Context) error {
	if err := captureContext(ctx); err != nil {
		return err
	}
	if s == nil || s.closed.Load() || len(s.files) == 0 {
		return e.New(e.CodeVerifyFailed, "test-result snapshot is unavailable or closed")
	}
	return nil
}

// Close invalidates the snapshot before releasing all held files. It is
// idempotent, including when capture fails after opening earlier files.
func (s *Snapshot) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		var failures []error
		for _, file := range s.files {
			failures = append(failures, file.file.Close())
		}
		if err := errors.Join(failures...); err != nil {
			s.closeErr = e.Wrap(e.CodeUnavailable, err, "closing captured test-result files")
		}
	})
	return s.closeErr
}

func captureContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "test-result capture requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "test-result capture canceled")
	}
	return nil
}

func captureError(ctx context.Context, err error, operation string) error {
	if canceled := captureContext(ctx); canceled != nil {
		return canceled
	}
	return e.Wrap(e.CodeVerifyFailed, err, "%s", operation)
}
