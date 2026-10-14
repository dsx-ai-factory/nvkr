// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func admitInputs(definition authored.Definition, environment authored.Environment) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if err := environment.Validate(); err != nil {
		return err
	}
	for _, output := range definition.Outputs {
		if output.Format == authored.OutputDebianPackage && (environment.Publication != nil || environment.Signing != nil) {
			return e.New(e.CodeEngineCapability, "Debian publication/signing requires final-byte archive evidence joins")
		}

		if output.Format == authored.OutputDebianPackage && definition.SBOMTool != "" {
			platforms := output.TargetPlatforms()
			if len(platforms) != 1 || platforms[0] != environment.UtilityPlatform {
				return e.New(e.CodeEngineCapability, "Debian scanner requires its exact native producer/utility platform before catalog work")
			}
			profile, ok := environment.DiskInspectionProfiles[environment.UtilityPlatform]
			if !ok {
				return e.New(e.CodeEngineCapability, "Debian scanner requires its complete native Environment inspection profile")
			}
			if err := requireProductionDiskScratch(profile); err != nil {
				return err
			}
		}
	}
	if publication := environment.Publication; publication != nil {
		if environment.GitHubDevelopment {
			namespace, err := development.ParseNamespace(publication.RepositoryPrefix)
			if err != nil {
				return err
			}
			for _, output := range definition.Outputs {
				for _, leaf := range []string{output.Name, "releases-" + output.Name} {
					if err := namespace.ValidateLeaf(leaf); err != nil {
						return err
					}
				}
			}
		}
		for _, name := range sortedKeys(publication.ImageTags) {
			matches := 0
			for _, output := range definition.Outputs {
				if output.Name == name && output.Format == authored.OutputOCI && len(output.Platforms) > 0 {
					matches++
				}
			}
			if matches != 1 {
				return e.New(e.CodeInvalidCatalog, "publication imageTags output %q requires one explicit aggregate OCI output", name)
			}
		}
	}
	if len(environment.GitHubRunners) > 0 {
		platforms := map[string]bool{environment.UtilityPlatform: true}
		for _, output := range definition.Outputs {
			for _, platform := range output.TargetPlatforms() {
				platforms[platform] = true
			}
		}
		for _, platform := range sortedKeys(platforms) {
			if _, ok := environment.GitHubRunners[platform]; !ok {
				return e.New(e.CodeEngineCapability, "githubRunners has no native route for required platform %s", platform)
			}
		}
	}
	if environment.Signing != nil && environment.Signing.Mode == SigningModeCosign && definition.CosignTool == "" {
		return e.New(e.CodeTrustPolicyMissing, "cosign signing requires Definition.cosignTool")
	}
	return nil
}

// Run-wide tools use the Definition catalog; each generator uses its output's
// catalog. One tool name identifies one acquisition, so all selected pins must agree.
func selectedTools(definition authored.Definition, environment authored.Environment, source *catalog.Catalog,
) (map[string]catalog.ToolPin, error) {
	if source == nil {
		return nil, e.New(e.CodeInvalidCatalog, "tool selection requires a resolved catalog")
	}
	names := []string{}
	if definition.SBOMTool != "" {
		names = append(names, definition.SBOMTool)
	}
	if environment.Signing != nil && environment.Signing.Mode == SigningModeCosign {
		names = append(names, definition.CosignTool)
	}
	tools := make(map[string]catalog.ToolPin, len(names))
	selectTool := func(name string, selected *catalog.Catalog) error {
		pin, ok := selected.Tools[name]
		if !ok {
			return e.New(e.CodeUnpinnedSource, "selected catalog tool %q has no pin", name)
		}
		if pin.Platform != environment.UtilityPlatform {
			return e.New(e.CodeEngineCapability, "tool %q platform %s does not match utility platform %s", name, pin.Platform, environment.UtilityPlatform)
		}
		if previous, exists := tools[name]; exists && previous != pin {
			return e.New(e.CodeUnpinnedSource, "selected catalog tool %q has conflicting pins", name)
		}
		tools[name] = pin
		return nil
	}
	for _, name := range uniqueSorted(names) {
		if err := selectTool(name, source); err != nil {
			return nil, err
		}
	}
	return tools, nil
}

func compileToolOperation(name string, pin catalog.ToolPin, runsOn string) (Node, Artifact) {
	id := "tool/" + name
	node := Node{ID: id, Kind: NodeAcquire, RunsOn: runsOn,
		Network: ir.NetworkDefault, URL: pin.URL, Digest: pin.ArchiveDigest,
		Executable: pin.ExecutablePath, ExecutableDigest: pin.ExecutableDigest, ToolFormat: pin.Format,
		Outputs: []string{id}}
	artifact := Artifact{ID: id, Name: name, Kind: "pipeline-tool",
		MediaType: "application/vnd.nvkr.pipeline-tool.v1", Producer: id,
		Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "tools", name, pin.ExecutablePath))}
	return node, artifact
}
