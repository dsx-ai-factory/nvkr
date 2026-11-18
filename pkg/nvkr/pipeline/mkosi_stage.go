// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
	"golang.org/x/sys/unix"
)

// prepareMkosiWork accepts only an already authenticated source snapshot and
// process-local material capability. The execution owner supplies a fresh private
// work directory and joins all children before inspecting or removing it. These
// paths and invocation descriptions do not authorize assembly or runtime use.
func prepareMkosiWork(ctx context.Context, node ir.Node, source, inputs, work string, materials *BuildMaterials) ([]mkosi.Invocation, string, error) {
	if err := requireRunContext(ctx, "mkosi staging"); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", e.Wrap(e.CodeTimeout, err, "mkosi staging canceled")
	}
	bindings := make([]string, 0, len(node.Materials))
	for _, binding := range node.Materials {
		bindings = append(bindings, binding.Binding)
	}
	operations, err := node.Mkosi.Invocations(node.Platform, bindings)
	if err != nil {
		return nil, "", err
	}
	if err := admitBuildMaterials(node, RunOptions{BuildMaterials: materials}); err != nil {
		return nil, "", err
	}
	var manifestDigest string
	if len(bindings) != 0 {
		_, raw, err := materials.validateFor(ctx, node)
		if err != nil {
			return nil, "", err
		}
		manifestDigest = FileDigest(raw)
	}
	for _, directory := range []string{inputs, work} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			return nil, "", e.New(e.CodeBuildFailed, "mkosi staging requires fresh private input and work directories")
		}
	}
	for _, name := range []string{"home", "tmp"} {
		if err := os.Mkdir(filepath.Join(work, name), 0o755); err != nil {
			return nil, "", e.Wrap(e.CodeBuildFailed, err, "creating private mkosi process state")
		}
	}
	wrapper := strings.TrimPrefix(mkosi.ArchiveToolsDirectory, "/inputs/") + "/tar"
	if err := writeMkosiFile(ctx, inputs, wrapper, []byte(mkosi.ArchiveWrapper())); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(filepath.Join(inputs, wrapper), 0o555); err != nil {
		return nil, "", e.Wrap(e.CodeBuildFailed, err, "protecting fixed mkosi archive adapter")
	}
	for index, operation := range operations {
		for _, directory := range []string{operation.Workspace, operation.Cache, operation.PackageCache, operation.OutputDirectory} {
			if err := os.MkdirAll(mkosiWorkPath(work, directory), 0o755); err != nil {
				return nil, "", e.Wrap(e.CodeBuildFailed, err, "creating mkosi operation directories")
			}
		}
		for _, directory := range []string{operation.Project, operation.Packages} {
			if err := os.MkdirAll(filepath.Join(inputs, strings.TrimPrefix(directory, "/inputs/")), 0o755); err != nil {
				return nil, "", e.Wrap(e.CodeBuildFailed, err, "creating mkosi input directories")
			}
		}
		if err := writeMkosiFile(ctx, inputs, strings.TrimPrefix(operation.Project, "/inputs/")+"/mkosi.conf", []byte(node.Mkosi.Configuration)); err != nil {
			return nil, "", err
		}
		if operation.PackageConfiguration == "" {
			continue
		}
		if err := writeMkosiFile(ctx, inputs, strings.TrimPrefix(operation.Project, "/inputs/")+"/mkosi.conf.d/10-packages.conf", []byte(operation.PackageConfiguration)); err != nil {
			return nil, "", err
		}
		root := node.Mkosi.Roots[index]
		for _, item := range root.Inputs.Packages {
			relative := filepath.Join(mkosiInputDirectory, root.Name, "packages", strings.TrimPrefix(item.Digest, "sha256:")+".deb")
			destination := strings.TrimPrefix(operation.Packages, "/inputs/") + "/" + mkosi.PackageFilename(item)
			if err := copyMkosiPackage(ctx, source, relative, inputs, destination, item); err != nil {
				return nil, "", err
			}
		}
	}
	evidence := "evidence" + mkosi.EvidenceDirectory
	raw, err := canonical.Marshal(node.Mkosi.Logical())
	if err != nil {
		return nil, "", err
	}
	if err := writeMkosiFile(ctx, inputs, evidence+"/recipe.json", raw); err != nil {
		return nil, "", err
	}
	if err := writeMkosiFile(ctx, inputs, evidence+"/mkosi.conf", []byte(node.Mkosi.Configuration)); err != nil {
		return nil, "", err
	}
	for _, projection := range node.Mkosi.Files {
		selected, err := material.StagingPath(projection.Material)
		if err != nil {
			return nil, "", err
		}
		for _, input := range materials.inputs {
			if input.Path != selected {
				continue
			}
			relative := "file-materials" + projection.Path
			if err := materializeHeldInput(ctx, inputs, relative, input.Content); err != nil {
				return nil, "", e.Wrap(e.CodeBuildFailed, err, "staging runtime file material")
			}
			if err := os.Chmod(filepath.Join(inputs, relative), os.FileMode(projection.Mode)); err != nil {
				return nil, "", e.Wrap(e.CodeBuildFailed, err, "setting runtime file material mode")
			}
		}
	}
	for _, content := range node.Mkosi.Contents {
		if err := writeMkosiFile(ctx, inputs, "file-materials"+content.Path, []byte(content.Data)); err != nil {
			return nil, "", err
		}
	}
	return operations, manifestDigest, nil
}

func stageMkosiPackageCopies(ctx context.Context, spec *mkosi.Spec, root, inputs string) error {
	for _, copied := range spec.Copies {
		relative := strings.TrimPrefix(copied.Source, "/")
		file, info, err := openRegularBeneath(root, relative)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "opening measured runtime package file")
		}
		if err := file.Close(); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "closing measured runtime package file")
		}
		if info.Size() <= 0 || info.Size() > 256<<20 {
			return e.New(e.CodeVerifyFailed, "runtime package file exceeds its copy size bound")
		}
		destination := "file-materials" + copied.Path
		if err := copyMkosiPackage(ctx, root, relative, inputs, destination, material.PackageInput{Digest: copied.Digest, Size: info.Size()}); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(inputs, destination), os.FileMode(copied.Mode)); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "setting measured runtime package file mode")
		}
	}
	return nil
}

// operation paths come only from mkosi.Invocations, never from authored input.
func mkosiWorkPath(work, location string) string {
	return filepath.Join(work, strings.TrimPrefix(location, "/work/"))
}

func copyMkosiPackage(ctx context.Context, source, relative, work, destination string, item material.PackageInput) error {
	file, info, err := openRegularBeneath(source, relative)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening retained mkosi package")
	}
	defer func() { _ = file.Close() }()
	if info.Size() != item.Size {
		return e.New(e.CodeVerifyFailed, "retained mkosi package size differs from its pin")
	}
	_, size, err := writeVerifiedDownload(ctx, file, "retained mkosi package", work, destination, item.Digest, item.Size)
	if err != nil {
		return err
	}
	if size != item.Size {
		return e.New(e.CodeVerifyFailed, "staged mkosi package size differs from its pin")
	}
	if err := os.Chmod(filepath.Join(work, destination), 0o444); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "protecting staged mkosi package")
	}
	return nil
}

func writeMkosiFile(ctx context.Context, work, relative string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "mkosi staging canceled")
	}
	parent, name, err := securepath.CreateParentBeneath(work, relative)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "opening private mkosi evidence directory")
	}
	defer func() { _ = parent.Close() }()
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o444)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "reserving private mkosi file")
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := file.Write(raw)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "writing private mkosi file")
	}
	return nil
}

// retainMkosiReport runs only after the namespace child and every descendant
// have exited. The original report is checked, then copied unchanged into the
// evidence tree that the final packaging pass embeds in its OCI layer.
func retainMkosiReport(ctx context.Context, work, inputs string, operation mkosi.Invocation, root mkosi.Root) error {
	raw, err := readMkosiReport(ctx, work, operation, root)
	if err != nil {
		return err
	}
	return writeMkosiFile(ctx, inputs, "evidence"+mkosi.EvidenceDirectory+"/"+root.Name+".manifest.json", raw)
}

func readMkosiReport(ctx context.Context, work string, operation mkosi.Invocation, root mkosi.Root) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "mkosi report collection canceled")
	}
	file, info, err := openRegularBeneath(work, strings.TrimPrefix(operation.Manifest, "/work/"))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening original mkosi report")
	}
	defer func() { _ = file.Close() }()
	if info.Size() <= 0 || info.Size() > 8<<20 {
		return nil, e.New(e.CodeVerifyFailed, "mkosi report exceeds its size bound")
	}
	raw, err := io.ReadAll(io.LimitReader(pipelineContextReader{ctx: ctx, source: file}, 8<<20+1))
	if err != nil {
		return nil, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading original mkosi report"), "mkosi report collection")
	}
	if err := operation.CheckReport(root, raw); err != nil {
		return nil, err
	}
	return raw, nil
}
