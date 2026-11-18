// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func buildMkosi(node ir.Node, options RunOptions, scratch string) (_ *ArtifactResult, returnErr error) {
	if node.Kind != ir.KindBuild || node.BuilderType != ir.BuilderMkosi || node.Mkosi == nil || node.Platform != "linux/"+runtime.GOARCH ||
		!options.Launch || options.DryRun || options.Package || options.SourceWorkspace != nil || options.NativeGo != nil || options.ToolExecutable != "" {
		return nil, e.New(e.CodeEngineCapability, "mkosi requires launched native assembly with its own source and material bindings")
	}
	if err := admitBuildMaterials(node, options); err != nil {
		return nil, err
	}
	if len(node.Materials) != 0 {
		if _, _, err := options.BuildMaterials.validateFor(options.ctx(), node); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(options.ctx(), 30*time.Minute)
	defer cancel()
	options.Ctx = ctx
	first, err := assembleMkosi(ctx, node, options, scratch)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, first.workspace.close()) }()
	second, err := assembleMkosi(ctx, node, options, scratch)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, second.workspace.close()) }()
	if first.subject != second.subject || first.size != second.size || first.layoutDigest != second.layoutDigest ||
		first.inputDigest != second.inputDigest || first.materialDigest != second.materialDigest {
		return nil, e.New(e.CodeVerifyFailed, "fresh mkosi assemblies differ in their exact OCI closure or authenticated inputs")
	}
	destination := artifactHostPath(options, node)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "preparing mkosi result parent")
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "reserving fresh mkosi OCI result")
	}
	if err := copySourceTreeContext(ctx, first.output, destination, 0); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "persisting mkosi OCI result")
	}
	gotSize, err := verifyOCILayoutAndSize(ctx, destination, first.subject)
	if err != nil || gotSize != first.size {
		return nil, e.New(e.CodeVerifyFailed, "mkosi OCI result changed during collection")
	}
	return &ArtifactResult{Component: node.Component, OutputType: node.OutputType, Qualification: ArtifactUnqualified,
		InputDigest: first.inputDigest, MaterialManifestDigest: first.materialDigest, BuilderType: ir.BuilderMkosi, EngineClass: ir.BuilderMkosi,
		Path: node.ArtifactPath, SHA256: first.subject, Size: first.size, TransportPath: destination,
		AbsPath: filepath.Join(destination, "blobs", "sha256", strings.TrimPrefix(first.subject, "sha256:")),
	}, nil
}

type mkosiAssembly struct {
	workspace                                                  *mkosiWorkspace
	output, subject, layoutDigest, inputDigest, materialDigest string
	size                                                       int64
}

// Each invocation has fresh output, workspace and cache trees. The caller owns
// both until their exact OCI bytes agree, before exposing either result.
func assembleMkosi(ctx context.Context, node ir.Node, options RunOptions, scratch string) (_ *mkosiAssembly, returnErr error) {
	workspace, err := newMkosiWorkspace(ctx, scratch)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, workspace.close())
		}
	}()
	source, err := materializeSource(node, options)
	if err != nil {
		return nil, err
	}
	defer source.cleanup()
	wantSource, err := sourceTreeDigestContext(ctx, source.Dir)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "measuring admitted mkosi source snapshot")
	}
	if err := copySourceTreeContext(ctx, source.Dir, workspace.source, 0); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "staging source-pinned mkosi module")
	}
	gotSource, err := sourceTreeDigestContext(ctx, workspace.source)
	if err != nil || gotSource != wantSource {
		return nil, e.New(e.CodeVerifyFailed, "mkosi source changed while staging the namespace projection")
	}
	operations, materialDigest, err := prepareMkosiWork(ctx, node, workspace.source, workspace.inputs, workspace.work, options.BuildMaterials)
	if err != nil {
		return nil, err
	}
	if err := workspace.ownWritableState(ctx); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "preparing mapped assembly scratch ownership")
	}
	for index, operation := range operations {
		if index == len(node.Mkosi.Roots) {
			root := mkosiWorkPath(workspace.work, operations[index-1].Output)
			if err := stageMkosiPackageCopies(ctx, node.Mkosi, root, workspace.inputs); err != nil {
				return nil, err
			}
		}
		if err := workspace.execute(ctx, operation, options.Stdout, options.Stderr); err != nil {
			return nil, mapCancellation(ctx, e.Wrap(e.CodeBuildFailed, err, "assembling mkosi root %s as %s", operation.Root, operation.Format), "mkosi assembly")
		}
		if index < len(node.Mkosi.Roots) {
			if err := retainMkosiReport(ctx, workspace.work, workspace.inputs, operation, node.Mkosi.Roots[index]); err != nil {
				return nil, err
			}
		} else if _, err := readMkosiReport(ctx, workspace.work, operation, node.Mkosi.Roots[len(node.Mkosi.Roots)-1]); err != nil {
			return nil, err
		}
	}
	output := mkosiWorkPath(workspace.work, operations[len(operations)-1].Output)
	subject, size, err := inspectMkosiOutput(ctx, node, output, workspace.inputs, workspace.parent)
	if err != nil {
		return nil, err
	}
	layout, err := ociclosure.Snapshot(ctx, output)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "measuring complete mkosi reproduction result")
	}
	accepted = true
	return &mkosiAssembly{workspace: workspace, output: output, subject: subject, size: size,
		layoutDigest: layout.Digest(), inputDigest: source.InputDigest, materialDigest: materialDigest}, nil
}
