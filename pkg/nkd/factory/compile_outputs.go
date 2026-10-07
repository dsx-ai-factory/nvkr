// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func admitInputs(definition authored.Definition, environment authored.Environment) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if err := environment.Validate(); err != nil {
		return err
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
		MediaType: "application/vnd.nkd.pipeline-tool.v1", Producer: id,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "tools", name, pin.ExecutablePath))}
	return node, artifact
}
