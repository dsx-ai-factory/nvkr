// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

// bindHelmImages edits only a private extracted source tree. Image digests come
// from inspected native image packages; publication routes never enter this call.
func bindHelmImages(ctx context.Context, root string, binding HelmImageBinding, digests map[string]string) error {
	if err := binding.ValidateBindings(); err != nil {
		return err
	}
	if len(digests) != len(binding.Images) {
		return e.New(e.CodeVerifyFailed, "Helm image digests differ from declared slots")
	}
	// Reject links before any mutation, including contained aliases that could
	// make one selected file overwrite another member of the source bundle.
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return fmt.Errorf("unsupported Helm source member %s", path)
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "admitting Helm binding tree")
	}

	valuesPath := filepath.Join(root, binding.Chart, "values.yaml")
	raw, err := os.ReadFile(valuesPath)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading Helm values")
	}
	values, err := documentpath.Decode(raw)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "invalid Helm values")
	}
	var paths []string
	for _, slot := range sortedKeys(binding.Images) {
		b := binding.Images[slot]
		paths = append(paths, b.Values.Digest)
	}
	selected, err := documentpath.Select(values, paths, false)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "resolving Helm values")
	}
	seen := map[*yaml.Node]bool{}
	for _, p := range paths {
		n := selected[p]
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || seen[n] {
			return e.New(e.CodeVerifyFailed, "Helm values target %q must be a distinct string scalar", p)
		}
		seen[n] = true
	}
	// Validate every digest before writing. Templates and other values stay upstream-owned.
	for _, slot := range sortedKeys(binding.Images) {
		digest := digests[slot]
		if !canonical.IsDigest(digest) {
			return e.New(e.CodeVerifyFailed, "invalid image digest for Helm slot %q", slot)
		}
		mapping := binding.Images[slot].Values
		if mapping.DigestFormat == "sha256-hex" {
			digest = strings.TrimPrefix(digest, "sha256:")
		}
		selected[mapping.Digest].Value = digest
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding Helm values")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "binding Helm images")
	}
	if err := os.WriteFile(valuesPath, encoded, 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing Helm values")
	}
	// Only a wrapper owns binder bookkeeping. When the source root is the
	// chart itself, every file (including checksums.txt) belongs to upstream.
	if binding.Chart == "." {
		return nil
	}
	var checksums strings.Builder
	for _, path := range files {
		if path == filepath.Join(root, "checksums.txt") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "hashing bound Helm tree")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "hashing Helm member")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "naming Helm member")
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(raw), filepath.ToSlash(relative))
	}
	if err := os.WriteFile(filepath.Join(root, "checksums.txt"), []byte(checksums.String()), 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing Helm checksums")
	}
	return nil
}
