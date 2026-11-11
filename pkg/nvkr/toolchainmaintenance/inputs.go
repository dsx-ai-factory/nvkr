// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// checkPreparationInputs compares committed inputs, never the ambient working
// tree. Both revisions and both native architectures contribute to the closure.
func checkPreparationInputs(ctx context.Context, root, source string) error {
	paths := []string{".go-version", "go.mod", "go.sum", "go.work", "go.work.sum", "vendor", "toolchains/nvkr-packer", "toolchains/nvkr-disk-inspect", "scripts/prepare-native-toolchains.sh"}
	diff := func(paths []string) error {
		args := append([]string{"diff", "--quiet", "--no-ext-diff", "--no-textconv", source, "HEAD", "--"}, paths...)
		if _, err := command(ctx, root, "git", args...); err != nil {
			return e.Wrap(e.CodeGateUnmet, err, "preparation is stale or its toolchain inputs cannot be compared; prepare and review a new candidate")
		}
		return nil
	}
	// Refuse changed pins before asking the current compiler to load old modules.
	if err := diff(paths); err != nil {
		return err
	}
	for _, revision := range []string{source, "HEAD"} {
		closure, err := inspectorInputs(ctx, root, revision)
		if err != nil {
			return e.Wrap(e.CodeGateUnmet, err, "cannot resolve preparation inspector inputs at %s", revision)
		}
		paths = append(paths, closure...)
	}
	// Checkout attributes can change embedded/copied bytes without changing the
	// blobs themselves (for example, text line endings). Include every ancestor
	// attribute file; attributes inside selected directories are already covered.
	paths = append(paths, ".gitattributes")
	for _, path := range slices.Clone(paths) {
		path = strings.TrimPrefix(path, ":(literal)")
		for dir := filepath.Dir(path); dir != "."; dir = filepath.Dir(dir) {
			paths = append(paths, ":(literal)"+filepath.ToSlash(filepath.Join(dir, ".gitattributes")))
		}
	}
	slices.Sort(paths)
	return diff(slices.Compact(paths))
}

func inspectorInputs(ctx context.Context, root, revision string) ([]string, error) {
	scratch, err := os.MkdirTemp("", "nvkr-inspector-inputs-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "create inspector input snapshot")
	}
	defer os.RemoveAll(scratch)
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolve inspector snapshot directory")
	}
	// Archive transformations could hide a package's imports. Refuse them rather
	// than resolving a closure from a filtered or substituted source tree.
	names, err := command(ctx, root, "git", "ls-tree", "-r", "--name-only", "-z", revision)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(string(names), "\x00") {
		if filepath.Base(name) != ".gitattributes" {
			continue
		}
		attributes, err := command(ctx, root, "git", "show", revision+":"+name)
		if err != nil {
			return nil, err
		}
		if bytes.Contains(attributes, []byte("export-ignore")) || bytes.Contains(attributes, []byte("export-subst")) {
			return nil, e.New(e.CodeGateUnmet, "inspector snapshot does not admit Git archive transformations")
		}
	}
	archive := filepath.Join(scratch, "source.tar")
	if _, err := command(ctx, root, "git", "-c", "core.attributesFile=/dev/null", "archive", "--format=tar", "--output="+archive, revision); err != nil {
		return nil, err
	}
	snapshot := filepath.Join(scratch, "source")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "create source directory")
	}
	if _, err := command(ctx, scratch, "tar", "-xf", archive, "-C", snapshot); err != nil {
		return nil, err
	}
	for _, unsupported := range []string{"go.work", "vendor"} {
		if _, err := os.Lstat(filepath.Join(snapshot, unsupported)); !os.IsNotExist(err) {
			return nil, e.New(e.CodeGateUnmet, "inspector closure requires a standalone, non-vendored source module")
		}
	}
	pin, err := os.ReadFile(filepath.Join(snapshot, ".go-version"))
	if err != nil {
		return nil, e.Wrap(e.CodeGateUnmet, err, "read inspector compiler pin")
	}
	version, err := command(ctx, snapshot, "env", "GOENV=off", "GOTOOLCHAIN=local", "go", "env", "GOVERSION")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(version)) != "go"+strings.TrimSpace(string(pin)) {
		return nil, e.New(e.CodeGateUnmet, "inspector closure requires the pinned Go compiler")
	}
	paths := []string{}
	for _, arch := range []string{"amd64", "arm64"} {
		// Match the static helper build, ignoring ambient workspaces, build flags,
		// platform tuning and persistent Go environment settings. Listing executes no
		// repository code and does not build or run a producer in publication jobs.
		out, err := command(ctx, snapshot, "env", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local",
			"GOOS=linux", "GOARCH="+arch, "GOAMD64=v1", "GOARM64=v8.0", "CGO_ENABLED=0",
			"go", "list", "-mod=readonly", "-deps", "-f", `{{if .Module}}{{if .Module.Main}}{{.Dir}}{{else if .Module.Replace}}{{if not .Module.Replace.Version}}OUTSIDE_MODULE{{end}}{{end}}{{end}}`, "./cmd/nvkr-disk-inspect")
		if err != nil {
			return nil, err
		}
		found := false
		for _, dir := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if dir == "" {
				continue
			}
			rel, err := filepath.Rel(snapshot, dir)
			if err != nil || !filepath.IsLocal(rel) {
				return nil, e.New(e.CodeGateUnmet, "inspector dependency is outside its source module")
			}
			// A whole package directory includes embedded assets, assembly and other
			// non-Go inputs, including new files not present in the older closure.
			paths = append(paths, ":(literal)"+filepath.ToSlash(rel))
			found = true
		}
		if !found {
			return nil, e.New(e.CodeGateUnmet, "inspector module closure is empty")
		}
	}
	return paths, nil
}
