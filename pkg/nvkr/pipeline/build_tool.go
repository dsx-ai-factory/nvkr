// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	_ "embed"
	"strconv"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

//go:embed build_tool_bootstrap.pl
var buildToolBootstrap string

func admitBuildTool(node ir.Node, opts RunOptions) error {
	if node.BuildTool == nil {
		return nil
	}
	if !opts.Launch || opts.Package {
		return e.New(e.CodeEngineCapability, "auxiliary tool requires launched source execution without packaging")
	}
	if opts.DryRun {
		return nil
	}
	if err := AdmitPinnedToolHost(node); err != nil {
		return err
	}
	if opts.ImageAccess == "" {
		return e.New(e.CodeEngineCapability, "auxiliary tool requires explicit image admission")
	}
	if opts.ToolExecutable == "" {
		return e.New(e.CodeProvenanceMissing, "build %q lacks its tool/%s input", node.ID, node.BuildTool.Name)
	}
	return nil
}

func captureBuildTool(node ir.Node, opts RunOptions) (*heldfile.File, error) {
	if node.BuildTool == nil || opts.DryRun {
		return nil, nil
	}
	if err := admitBuildTool(node, opts); err != nil {
		return nil, err
	}
	file, err := heldfile.OpenContext(opts.ctx(), opts.ToolExecutable, heldfile.Options{
		Label: "build-tool", Executable: true, MaxBytes: maxPinnedToolBytes, ExpectedDigest: node.BuildTool.ExecutableDigest,
	})
	if err != nil {
		return nil, mapCancellation(opts.ctx(), e.Wrap(e.CodeVerifyFailed, err, "capturing build %q tool", node.ID), "build tool capture")
	}
	if err := validateStaticTool(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func buildToolCommand(node ir.Node, file *heldfile.File) []string {
	args := []string{"-e", buildToolBootstrap, "--", node.Platform, file.Digest(), strconv.FormatInt(file.Size(), 10)}
	return append(args, node.Command...)
}
