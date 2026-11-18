// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/rootfs"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

// RuntimeContents describes installed content observed through a live producer.
// It is evidence input, never a restorable production or execution capability.
// Archive hashes identify retained package inputs, not installed file hashes.
type RuntimeContents struct {
	Binding      ProducedRuntimeBinding
	RecipeDigest string
	Roots        []RuntimePackageRoot
	Files        []RuntimeFile
}

type RuntimePackageRoot struct {
	Name, Distribution, Release, Snapshot, ReportDigest string
	Packages                                            []mkosi.PackagePin
}

type RuntimeFile struct {
	Path, Digest, Material string
	Size                   int64
}

// InspectContents authenticates the complete live producer closure before
// staging its held layer. It reads the original embedded reports, checks each
// installed closure against the retained recipe, and measures projected files.
// The lock keeps Close from revoking the production while inspection is active.
func (tool *ProducedRuntime) InspectContents(ctx context.Context, scratch string) (_ *RuntimeContents, returnErr error) {
	state, err := tool.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer state.mu.Unlock()
	if state.recipe == nil {
		return nil, e.New(e.CodeProvenanceMissing, "runtime contents require the original assembly recipe")
	}
	var bindings []string
	for _, file := range state.recipe.Files {
		bindings = append(bindings, file.Material)
	}
	operations, err := state.recipe.Invocations(state.binding.Platform, bindings)
	if err != nil {
		return nil, err
	}
	staged, err := rootfs.Stage(ctx, state.layer, scratch)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, staged.Close()) }()
	directory, err := staged.Directory()
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	logical := state.recipe.Logical()
	want, err := canonical.Marshal(logical)
	if err != nil {
		return nil, err
	}
	raw, err := readMkosiRootFile(ctx, directory, mkosi.EvidenceDirectory+"/recipe.json", 8<<20)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw, want) {
		return nil, e.New(e.CodeVerifyFailed, "runtime contents changed the original assembly recipe")
	}
	contents := &RuntimeContents{Binding: state.binding, RecipeDigest: FileDigest(raw)}
	raw, err = readMkosiRootFile(ctx, directory, mkosi.EvidenceDirectory+"/mkosi.conf", 64<<10)
	if err != nil {
		return nil, err
	}
	if FileDigest(raw) != state.recipe.ConfigurationDigest {
		return nil, e.New(e.CodeVerifyFailed, "runtime contents changed the original mkosi configuration")
	}
	for index, root := range state.recipe.Roots {
		raw, err := readMkosiRootFile(ctx, directory, mkosi.EvidenceDirectory+"/"+root.Name+".manifest.json", 8<<20)
		if err != nil {
			return nil, err
		}
		if err := operations[index].CheckReport(root, raw); err != nil {
			return nil, err
		}
		contents.Roots = append(contents.Roots, RuntimePackageRoot{Name: root.Name, Distribution: root.Distribution,
			Release: root.Release, Snapshot: root.Inputs.Snapshot, ReportDigest: FileDigest(raw),
			Packages: slices.Clone(logical.Roots[index].Inputs.Packages)})
	}
	for _, file := range state.recipe.Files {
		digest, size, mode, err := hashMkosiRootFile(ctx, directory, strings.TrimPrefix(file.Path, "/"))
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading produced runtime file material")
		}
		if mode != os.FileMode(file.Mode) {
			return nil, e.New(e.CodeVerifyFailed, "runtime file material mode changed")
		}
		contents.Files = append(contents.Files, RuntimeFile{Path: file.Path, Digest: digest, Material: file.Material, Size: size})
	}
	// The held layer is immutable; also reject changes to the original result
	// directory while the potentially long extraction was in progress.
	if err := state.require(ctx); err != nil {
		return nil, err
	}
	return contents, nil
}

func readMkosiRootFile(ctx context.Context, root *os.File, absolute string, limit int64) (_ []byte, returnErr error) {
	file, info, err := openMkosiRootFile(ctx, root, strings.TrimPrefix(absolute, "/"))
	if err != nil {
		return nil, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "opening embedded runtime evidence"), "runtime evidence")
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, e.New(e.CodeVerifyFailed, "embedded runtime evidence exceeds its regular file bound")
	}
	raw, err := io.ReadAll(io.LimitReader(pipelineContextReader{ctx: ctx, source: file}, limit+1))
	if err != nil {
		return nil, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading embedded runtime evidence"), "runtime evidence")
	}
	if int64(len(raw)) != info.Size() {
		return nil, e.New(e.CodeVerifyFailed, "embedded runtime evidence changed while reading")
	}
	return raw, nil
}
