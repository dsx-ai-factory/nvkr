// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// openNativePackerBundle authenticates one local NVKR toolchain bundle against the
// exact semantic identity and host platform selected by the Plan. Publication
// here means an existing local OCI layout, never a remote push. The packer-toolchain
// purpose defines /usr/local/bin programs and /usr/local/lib/packer/plugins files;
// ordinary binary bundles do not grant this execution capability.
func openNativePackerBundle(ctx context.Context, publication, name, expected, platform string) (result *pipeline.PackerToolchain, returnedErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "native Packer bundle requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening native Packer bundle")
	}
	if !canonical.IsDigest(expected) {
		return nil, e.New(e.CodeUnpinnedInput, "native Packer bundle requires an exact semantic digest")
	}

	inspection, err := delivery.Inspect(ctx, publication)
	if err != nil {
		return nil, err
	}
	found := false
	for _, object := range inspection.Root.Objects {
		if object.Name != name {
			continue
		}
		if object.ArtifactType != delivery.MediaBinaryBundle || object.SemanticDigest != expected {
			return nil, e.New(e.CodeUnverifiedRef, "selected native Packer bundle differs from the Plan toolchain")
		}
		found = true
	}
	if !found {
		return nil, e.New(e.CodeUnverifiedRef, "selected native Packer bundle is absent")
	}
	root, err := os.MkdirTemp("", "nvkr-packer-bundle-")
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "creating native Packer bundle owner")
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			if result != nil {
				err = errors.Join(err, result.Close())
				result = nil
			}
			returnedErr = e.Wrap(e.CodeEngineCapability, errors.Join(returnedErr, err), "cleaning native Packer bundle staging")
		}
	}()
	extracted := filepath.Join(root, "files")
	bundle, err := delivery.ExtractBinaryBundle(ctx, publication, name, extracted)
	if err != nil {
		return nil, err
	}
	digest, err := bundle.Digest()
	if err != nil {
		return nil, err
	}
	if digest != expected || bundle.OS+"/"+bundle.Arch != platform || bundle.Purpose != "packer-toolchain" {
		return nil, e.New(e.CodeUnverifiedRef, "native Packer bundle identity, platform or purpose differs")
	}
	var program, programDigest string
	var tools []pipeline.PackerHostTool
	var plugins []pipeline.PackerPluginFile
	for _, entry := range bundle.Entries {
		source := filepath.Join(extracted, entry.Name)
		switch {
		case entry.InstallPath == "/usr/local/bin/packer":
			if program != "" || entry.Mode&0o111 == 0 {
				return nil, e.New(e.CodeInvalidCatalog, "native Packer bundle has an ambiguous executable")
			}
			program, programDigest = source, entry.SHA256
		case filepath.Dir(entry.InstallPath) == "/usr/bin" || filepath.Dir(entry.InstallPath) == "/bin":
			if entry.Mode&0o111 == 0 {
				return nil, e.New(e.CodeInvalidCatalog, "system dependency must be executable")
			}
			// Keep the declared command alias (tar may resolve to bsdtar), while
			// executing the exact regular file at its protected system path.
			tools = append(tools, pipeline.PackerHostTool{Name: entry.Name, Path: entry.InstallPath, SHA256: entry.SHA256})
		case strings.HasPrefix(entry.InstallPath, "/usr/local/bin/"):
			name := strings.TrimPrefix(entry.InstallPath, "/usr/local/bin/")
			if !filepath.IsLocal(name) || filepath.Base(name) != name || entry.Mode&0o111 == 0 {
				return nil, e.New(e.CodeInvalidCatalog, "native Packer bundle has an invalid host tool")
			}
			tools = append(tools, pipeline.PackerHostTool{Name: name, Path: source, SHA256: entry.SHA256})
		case strings.HasPrefix(entry.InstallPath, "/usr/local/lib/packer/plugins/") || strings.HasPrefix(entry.InstallPath, pipeline.NativePackerFilePrefix):
			name := strings.TrimPrefix(entry.InstallPath, "/usr/local/lib/packer/plugins/")
			if strings.HasPrefix(entry.InstallPath, pipeline.NativePackerFilePrefix) {
				if entry.Mode&0o111 != 0 {
					return nil, e.New(e.CodeInvalidCatalog, "native Packer data must not be executable")
				}
				name = strings.TrimPrefix(entry.InstallPath, "/")
			}
			if !filepath.IsLocal(name) || filepath.Clean(name) != name {
				return nil, e.New(e.CodeInvalidCatalog, "native Packer bundle has an invalid plugin path")
			}
			destination := filepath.Join(root, "plugins", name)
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return nil, e.Wrap(e.CodeEngineCapability, err, "staging native Packer plugin")
			}
			// Plugins and native data share the existing bounded private file
			// closure. The toolchain takes its own checked snapshot before cleanup.
			if err := os.Link(source, destination); err != nil {
				return nil, e.Wrap(e.CodeEngineCapability, err, "staging native Packer plugin file")
			}
			plugins = append(plugins, pipeline.PackerPluginFile{Path: name, SHA256: entry.SHA256, Executable: entry.Mode&0o111 != 0})
		default:
			return nil, e.New(e.CodeInvalidCatalog, "native Packer bundle contains an unsupported toolchain file role")
		}
	}
	if program == "" {
		return nil, e.New(e.CodeReleaseIncomplete, "native Packer bundle contains no Packer executable")
	}
	toolchain, err := pipeline.OpenPackerToolchain(ctx, program, programDigest, tools, filepath.Join(root, "plugins"), plugins)
	if err != nil {
		return nil, err
	}

	return toolchain, nil
}
