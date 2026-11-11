// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/bundlearchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// AICRValidation describes the successful fixed post-render validation. Like the
// enclosing producer result it gains no execution authority merely by decoding it.
type AICRValidation struct {
	ToolDigest     string `json:"toolDigest"`
	IndexDigest    string `json:"indexDigest"`
	ManifestDigest string `json:"manifestDigest"`
	LayerDigest    string `json:"layerDigest"`
}

// ValidateAICRBundle runs only the fixed upstream verification command over a
// bounded private extraction. The caller binds its exact imported layout to this
// layer; this function returns no signature or qualified-execution authority.
func ValidateAICRBundle(ctx context.Context, n ir.Node, archive []byte, opts RunOptions) error {
	if err := requireRunContext(ctx, "AICR validation"); err != nil {
		return err
	}
	if err := n.Validate(); err != nil {
		return err
	}
	if opts.DryRun {
		return e.New(e.CodeEngineCapability, "AICR validation requires execution")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	opts.Ctx = ctx
	opts = withDefaults(opts)
	if err := admitPinnedTool(n, opts); err != nil {
		return err
	}
	if n.BuilderType != ir.BuilderPinnedTool || n.OutputType != ir.OutputOCILayout {
		return e.New(e.CodeEngineCapability, "AICR validation requires a pinned layout producer")
	}
	if opts.ImageAccess != "" {
		var err error
		opts.preparedImage, err = PrepareContainerImage(ctx, opts.Runtime, n.Toolchain, n.Platform, opts.ImageAccess)
		if err != nil {
			return err
		}
	}
	opts.reap = newReaper(ctx, opts.Runtime)
	defer opts.reap.close()
	temporary, err := os.MkdirTemp("", "nvkr-aicr-validation-")
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating AICR validation workspace")
	}
	defer func() { _ = removePrivateTree(temporary) }()
	toolDir, toolName, err := stagePinnedToolExecutable(ctx, n, opts.ToolExecutable, temporary)
	if err != nil {
		return err
	}

	inputs := filepath.Join(temporary, "inputs")
	if err := os.Mkdir(inputs, 0o700); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating AICR validation inputs")
	}
	if err := bundlearchive.Extract(ctx, archive, filepath.Join(inputs, "bundle")); err != nil {
		return err
	}
	// Recheck the held executable's staged bytes for this distinct invocation.
	if sum, _, _, err := hashRegularBeneathContext(ctx, toolDir, toolName); err != nil || sum != n.Digest {
		return e.New(e.CodeVerifyFailed, "AICR validation tool differs from producer pin")
	}
	output := filepath.Join(temporary, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating validator output")
	}
	validation := n
	validation.ID += "-aicr-verify"
	validation.Command = []string{"verify", ir.PinnedToolInputsRoot + "/bundle"}
	name := containerName(validation)
	if err := registerContainer(opts, name); err != nil {
		return err
	}
	if err := runContainerRuntime(opts, pinnedToolRunArgs(validation, filepath.Base(opts.Runtime), name, toolDir, toolName, inputs, output)...); err != nil {
		return mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "AICR post-render verification failed"), "AICR validation")
	}
	opts.reap.forget(name)
	return nil
}
