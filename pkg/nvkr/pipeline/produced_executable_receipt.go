// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// producedExecutableReceipt retains the exact result of one concrete producer.
// It grants only consumption of that executable, never whole-Run completeness
// or execution qualification. Its binding preserves the terminal receipt wire
// shape used by existing internal-tool evidence.
type producedExecutableReceipt struct {
	binding        string
	result         ArtifactResult
	root, relative string
	info           os.FileInfo
}

type producedExecutableReceiptView struct {
	BuildDigest string                           `json:"buildDigest"`
	Nodes       []producedExecutableNodeView     `json:"nodes"`
	Artifacts   []producedExecutableArtifactView `json:"artifacts"`
}

type producedExecutableNodeView struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Duration int64  `json:"duration"`
}

type producedExecutableArtifactView struct {
	NodeID   string         `json:"nodeId"`
	Result   ArtifactResult `json:"result"`
	LiveRoot string         `json:"liveRoot,omitempty"`
	LivePath string         `json:"livePath,omitempty"`
}

func captureProducedExecutableReceipt(ctx context.Context, buildDigest string, node ir.Node, result ArtifactResult, opts RunOptions) (producedExecutableReceipt, error) {
	var zero producedExecutableReceipt
	if result.Ref != "" || result.Packaged {
		return zero, e.New(e.CodeVerifyFailed, "produced executable must be a raw regular file")
	}
	var root, expected string
	switch node.Kind {
	case ir.KindBuild:
		if result.Toolchain != node.Toolchain || result.BuilderType != node.BuilderType || result.Path != node.ArtifactPath {
			return zero, e.New(e.CodeVerifyFailed, "produced executable differs from its pinned build declaration")
		}
		root, expected = privOutDir(opts, node), artifactHostPath(opts, node)
	case ir.KindPull:
		if result.SHA256 != node.Digest {
			return zero, e.New(e.CodeVerifyFailed, "produced executable differs from its pinned acquisition")
		}
		root = prebuiltDir(opts, node)
		name := node.ArtifactPath
		if name == "" {
			name = node.Component
		}
		var err error
		expected, err = pathWithin(root, name)
		if err != nil {
			return zero, e.Wrap(e.CodeVerifyFailed, err, "locating acquired executable")
		}
	default:
		return zero, e.New(e.CodeVerifyFailed, "produced executable requires a build or pull")
	}
	if result.AbsPath != expected {
		return zero, e.New(e.CodeVerifyFailed, "produced executable live path differs from its graph-derived path")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "locating executable root")
	}
	relative, err := filepath.Rel(absoluteRoot, expected)
	if err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "locating produced executable")
	}
	digest, size, info, err := hashRegularBeneathContext(ctx, root, relative)
	if err != nil {
		return zero, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing produced executable receipt"), "produced executable capture")
	}
	if digest != result.SHA256 || size != result.Size {
		return zero, e.New(e.CodeVerifyFailed, "produced executable changed during receipt capture")
	}
	binding, err := canonical.Digest(canonical.KindTerminalVerification, producedExecutableReceiptView{
		BuildDigest: buildDigest,
		Nodes:       []producedExecutableNodeView{{ID: node.ID, Kind: node.Kind, Status: "ok"}},
		Artifacts:   []producedExecutableArtifactView{{NodeID: node.ID, Result: result, LiveRoot: root, LivePath: result.AbsPath}},
	})
	if err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "binding produced executable receipt")
	}
	return producedExecutableReceipt{binding: binding, result: result, root: root, relative: relative, info: info}, nil
}

func (receipt producedExecutableReceipt) require(ctx context.Context) error {
	if receipt.binding == "" || receipt.info == nil {
		return e.New(e.CodeVerifyFailed, "produced executable lacks its original receipt")
	}
	digest, size, info, err := hashRegularBeneathContext(ctx, receipt.root, receipt.relative)
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "produced executable revalidation canceled")
	}
	if err != nil || !os.SameFile(receipt.info, info) || digest != receipt.result.SHA256 || size != receipt.result.Size {
		return e.New(e.CodeVerifyFailed, "produced executable changed after receipt capture")
	}
	return nil
}
