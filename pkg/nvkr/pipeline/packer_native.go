// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// NativePackerFilePrefix names data in the authenticated toolchain. Its logical
// path is stable; only the live toolchain owner resolves it to private storage.
const NativePackerFilePrefix = "/nvkr/toolchain/"

// NativeVariables returns a transport view of variables bound to captured
// toolchain data. It confers no authority after the toolchain is closed. The
// original Plan and logical build identity retain their stable file names.
func (toolchain *PackerToolchain) NativeVariables(ctx context.Context, variables []PackerVariable) ([]PackerVariable, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "native Packer variables require a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "resolving native Packer variables")
	}
	if toolchain == nil {
		return nil, e.New(e.CodeEngineCapability, "native Packer toolchain is absent")
	}
	if err := lockPackerToolchain(ctx, toolchain); err != nil {
		return nil, err
	}
	defer toolchain.mu.RUnlock()
	if toolchain.closed || toolchain.executable == nil {
		return nil, e.New(e.CodeEngineCapability, "native Packer toolchain is closed")
	}
	resolved := slices.Clone(variables)
	for i := range resolved {
		value := resolved[i].Value
		if !strings.HasPrefix(value, NativePackerFilePrefix) {
			continue
		}
		relative := strings.TrimPrefix(value, "/")
		if !filepath.IsLocal(relative) || filepath.Clean(relative) != relative {
			return nil, e.New(e.CodeInvalidCatalog, "native Packer file reference is not a clean logical path")
		}
		found := false
		for _, file := range toolchain.plugins {
			if file.Path == relative && file.Mode&0o111 == 0 {
				found = true
				break
			}
		}
		if !found {
			return nil, e.New(e.CodeUnverifiedRef, "native Packer file %q is absent from the selected bundle", value)
		}
		resolved[i].Value = filepath.Join(toolchain.pluginsDir, relative)
	}
	return resolved, nil
}

func runNativePackerNode(ctx context.Context, toolchain *PackerToolchain, source *materializedSource,
	node ir.Node, input VMImagePackerRun, workspace *os.File,
) (artifact *PackerArtifact, returnedErr error) {
	if node.Packer.AcquisitionArchive != "" && (node.Requirements == nil || node.Requirements.Resources == nil) {
		return nil, e.New(e.CodeEngineCapability, "native Packer acquisition collection requires the declared resource budget")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "native Packer requires the admitted executor deadline")
	}
	absolute, err := filepath.Abs(input.Workspace)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "resolving native Packer workspace projection")
	}
	held, err := workspace.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "observing native Packer workspace")
	}
	named, err := os.Lstat(absolute)
	if err != nil || !named.IsDir() || !os.SameFile(held, named) {
		return nil, e.Wrap(e.CodeEngineCapability, err, "native Packer workspace projection differs from its admitted directory")
	}
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "allocating native Packer attempt")
	}
	work, err := createPackerWorkspaceRoot(workspace, attempt)
	if err != nil {
		return nil, err
	}
	work.hostPath = filepath.Join(absolute, work.name)
	defer func() {
		if err := work.cleanup(); err != nil {
			if artifact != nil {
				err = errors.Join(err, artifact.Close())
				artifact = nil
			}
			returnedErr = e.Wrap(e.CodeEngineCapability, errors.Join(returnedErr, err), "cleaning native Packer workspace")
		}
	}()
	if _, err := toolchain.execution.projectDirectory(work.hostPath, work.root); err != nil {
		return nil, err
	}
	if err := materializeHeldInput(ctx, work.hostPath, "manifest.json", input.Manifest); err != nil {
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "staging native Packer material manifest")
	}
	accelerator := "tcg"
	for _, capability := range node.ExecutionBinding.Capabilities {
		if !capability.Enabled {
			continue
		}
		switch capability.Name {
		case execution.CapabilityKVM:
			accelerator = "kvm"
		case execution.CapabilityHVF:
			accelerator = "hvf"
		}
	}
	config := node.Packer
	request := PackerBuildRequest{
		ProjectPath: config.ProjectPath, BuildName: config.BuildName, PackerVersion: config.PackerVersion,
		ArchitectureVariable: config.ArchitectureVariable, Architecture: packerArchitecture(node.Platform),
		AcceleratorVariable: config.AcceleratorVariable, Accelerator: accelerator,
		MaterialManifestVariable: config.MaterialManifestVariable,
		MaterialManifestPath:     filepath.Join(work.hostPath, "manifest.json"), MaterialManifestSHA256: input.Manifest.Digest(),
		Materials: input.Materials, OutputDirectoryVariable: config.OutputDirectoryVariable, OutputParent: work.hostPath,
		BuildLogDirectoryVariable: config.BuildLogDirectoryVariable, HTTPDirectoryVariable: config.HTTPDirectoryVariable,
		SSHPrivateKeyVariable: config.SSHPrivateKeyVariable, SSHPublicKeyVariable: config.SSHPublicKeyVariable,
		ArtifactPath: node.ArtifactPath, Timeout: time.Until(deadline), Stdout: input.Stdout, Stderr: input.Stderr,
	}
	if config.AcquisitionArchive != "" {
		request.acquisitions = &nativePackerAcquisitionRequest{
			archivePath: config.AcquisitionArchive, workspace: workspace, budget: *node.Requirements.Resources,
		}
	}
	for _, variable := range config.Variables {
		request.Variables = append(request.Variables, PackerVariable(variable))
	}
	request.Variables, err = toolchain.NativeVariables(ctx, request.Variables)
	if err != nil {
		return nil, err
	}
	return runPacker(ctx, toolchain, source, request)
}
