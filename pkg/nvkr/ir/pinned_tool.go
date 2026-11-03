// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

var pinnedToolEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PinnedToolExecutorEnv names the variables the executor owns (ADR-27 §3). A node's
// literal environment cannot redefine them.
var PinnedToolExecutorEnv = map[string]string{"HOME": "/nvkr/home", "TMPDIR": "/nvkr/tmp", "TZ": "UTC", "LC_ALL": "C"}

// validatePinnedToolNode admits the source-less pinned-tool KindBuild (ADR-27 §2). It
// carries no fetch, source mount or vendor pin; its only dependencies are material
// producers, which Graph.Validate binds exactly.
func validatePinnedToolNode(n Node) error {
	if err := rejectUnexpectedNodeFields(n, allowFields(
		"requirements", "executionBinding", "component", "builderType", "tool", "artifact", "digest",
		"executableArchive", "toolchain", "platform", "network", "env", "command", "workdir",
		"materials", "outputType", "artifactPath", "version")); err != nil {
		return err
	}
	if n.ExecutionBinding != nil {
		if err := n.ExecutionBinding.ValidateFor(n.Requirements); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "pinned-tool node %q has invalid execution binding", n.ID)
		}
	}
	if !canonicalNodeID.MatchString(n.Component) || n.ID != "build-"+n.Component {
		return e.New(e.CodeInvalidCatalog, "pinned-tool node %q must be named build-<canonical component>", n.ID)
	}
	if strings.TrimSpace(n.Tool) == "" || n.Tool != strings.TrimSpace(n.Tool) || strings.ContainsAny(n.Tool, "/\x00") {
		return e.New(e.CodeUnpinnedSource, "pinned-tool node %q names no canonical tool", n.ID)
	}
	archive := n.ExecutableArchive
	if !strings.HasPrefix(n.Artifact, "https://") || !safeReference(n.Artifact) || !canonical.IsDigest(n.Digest) || archive == nil ||
		!canonical.IsDigest(archive.Digest) || !safeRelativePath(archive.Member) || strings.Contains(archive.Member, "=") ||
		(archive.Format != "raw" && archive.Format != "tar-gzip" && archive.Format != "zip") ||
		(archive.Format == "raw" && archive.Digest != n.Digest) {
		return e.New(e.CodeUnpinnedSource, "pinned-tool node %q requires an exact HTTPS tool archive, member and executable digest", n.ID)
	}
	if !exactOCIIdentity(n.Toolchain) {
		return e.New(e.CodeTagToolchain, "pinned-tool node %q toolchain %q is not an exact OCI digest pin", n.ID, n.Toolchain)
	}
	if err := target.ValidatePlatform(n.Platform); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "pinned-tool node %q", n.ID)
	}
	if n.Network != NetworkNone {
		return e.New(e.CodeInvalidCatalog, "pinned-tool node %q network mode %q must be %q", n.ID, n.Network, NetworkNone)
	}
	if n.Workdir != PinnedToolWorkdir {
		return e.New(e.CodeInvalidCatalog, "pinned-tool node %q workdir %q is not %q", n.ID, n.Workdir, PinnedToolWorkdir)
	}
	for _, argument := range n.Command {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') || strings.Contains(argument, "{{") {
			return e.New(e.CodeInvalidCatalog, "pinned-tool node %q has an unrendered or invalid argument", n.ID)
		}
	}
	previous := ""
	for _, variable := range n.Env {
		if !pinnedToolEnvName.MatchString(variable.Name) || variable.Name <= previous || strings.ContainsRune(variable.Value, '\x00') {
			return e.New(e.CodeInvalidCatalog, "pinned-tool node %q environment must be sorted unique literal names", n.ID)
		}
		if _, owned := PinnedToolExecutorEnv[variable.Name]; owned {
			return e.New(e.CodeInvalidCatalog, "pinned-tool node %q may not redefine executor-owned %s", n.ID, variable.Name)
		}
		previous = variable.Name
	}
	switch n.OutputType {
	case OutputFile:
		if !safeRelativePath(n.ArtifactPath) || strings.Contains(n.ArtifactPath, "/") {
			return e.New(e.CodeInvalidCatalog, "pinned-tool node %q file output requires one output file name", n.ID)
		}
	case OutputOCILayout:
		if n.ArtifactPath != "" {
			return e.New(e.CodeInvalidCatalog, "pinned-tool node %q OCI layout output has no artifactPath", n.ID)
		}
	default:
		return e.New(e.CodeInvalidCatalog, "pinned-tool node %q outputType %q is not file or oci-layout", n.ID, n.OutputType)
	}
	if !safeVersion(n.Version) {
		return e.New(e.CodeInvalidCatalog, "pinned-tool node %q has no canonical tool version", n.ID)
	}
	return validateMaterialBindings(n.ID, n.Materials)
}

// validatePinnedToolMaterials requires each material producer to emit one regular
// file (ADR-27 §4). Pulled OCI images and OCI layouts are not staged.
func validatePinnedToolMaterials(node Node, byID map[string]Node) error {
	for _, binding := range node.Materials {
		producer := byID[binding.Producer]
		if producer.OutputType == "oci-image" || producer.OutputType == OutputOCILayout {
			return e.New(e.CodeEngineCapability,
				"pinned-tool node %q material %q is not one regular file", node.ID, binding.Binding)
		}
	}
	return nil
}
