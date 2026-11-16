// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BuildToolPath is the inherited sealed executable descriptor. It is an invocation
// address, never a host path or a tool selection mechanism.
const BuildToolPath = "/proc/self/fd/200"

// BuildToolBinding is the transport-free identity of one auxiliary native tool.
// Archive and executable identities remain distinct even when acquisition is shared.
type BuildToolBinding struct {
	Name             string `json:"name"`
	Platform         string `json:"platform"`
	Format           string `json:"format"`
	Member           string `json:"member"`
	ArchiveDigest    string `json:"archiveDigest"`
	ExecutableDigest string `json:"executableDigest"`
}

func cloneBuildTool(binding *BuildToolBinding) *BuildToolBinding {
	if binding == nil {
		return nil
	}
	copy := *binding
	return &copy
}

func validateBuildTool(n Node) error {
	tool := n.BuildTool
	if tool == nil {
		if n.BuildToolURL != "" {
			return e.New(e.CodeUnpinnedSource, "build %q has tool transport without a binding", n.ID)
		}
		return nil
	}
	if n.BuilderType != BuilderOCIHermetic || n.Network != NetworkNone || !n.SourceReadOnly || n.WriteDir == "" ||
		(tool.Platform != "linux/amd64" && tool.Platform != "linux/arm64") || tool.Platform != n.Platform ||
		(n.ToolchainPlatform != "" && n.ToolchainPlatform != tool.Platform) {
		return e.New(e.CodeEngineCapability, "build %q tool requires an isolated offline native Linux source build", n.ID)
	}
	if !canonicalNodeID.MatchString(tool.Name) || !strings.HasPrefix(n.BuildToolURL, "https://") || !safeReference(n.BuildToolURL) ||
		!canonical.IsDigest(tool.ArchiveDigest) || !canonical.IsDigest(tool.ExecutableDigest) || !safeRelativePath(tool.Member) ||
		strings.Contains(tool.Member, "=") || (tool.Format != "raw" && tool.Format != "tar-gzip") ||
		(tool.Format == "raw" && tool.ArchiveDigest != tool.ExecutableDigest) {
		return e.New(e.CodeUnpinnedSource, "build %q requires an exact native tool archive, member and executable pin", n.ID)
	}
	for _, argument := range n.Command {
		if strings.Contains(argument, BuildToolPath) {
			return nil
		}
	}
	return e.New(e.CodeInvalidCatalog, "build %q does not invoke its bound tool descriptor", n.ID)
}
