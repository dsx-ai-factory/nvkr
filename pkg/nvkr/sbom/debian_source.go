// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Debian routes intentionally omit the common toolchain fields. Recompile the
// complete description and match its Build identity before projecting the exact
// selected toolchain into source metadata. This is neither producer authority
// nor inspected dependency evidence; the package version still belongs to the
// pinned Debian reader, independently from the source tag in this document.
func perArtifactSourceComponents(res *buildgraph.Resolved, buildDigest string) ([]catalog.NamedComponent, error) {
	if res == nil {
		return nil, e.New(e.CodeInvalidCatalog, "per-artifact documents require a resolved build")
	}
	components := append([]catalog.NamedComponent(nil), res.Components...)
	debian := res.Debian != nil
	for _, component := range components {
		debian = debian || component.OutputType == catalog.OutputDebianPackage
	}
	if !debian {
		return components, nil
	}
	graph, err := buildgraph.Compile(res)
	if err != nil {
		return nil, err
	}
	digest, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	if digest != buildDigest {
		return nil, e.New(e.CodeUnverifiedRef, "Debian source document Build identity differs from its complete compiled producer")
	}
	for i := range components {
		for _, node := range graph.Nodes {
			if node.Kind == ir.KindBuild && node.OutputType == catalog.OutputDebianPackage && node.Component == components[i].Name {
				components[i].ToolchainImage = node.Toolchain
				components[i].ToolchainImages = nil
			}
		}
	}
	return components, nil
}
