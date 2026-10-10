// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestMaintainedM2OSFetchCredentialProjection(t *testing.T) {
	const root = "../../../definitions/nvke-m2"
	const repository = "https://github.com/dsx-ai-factory/nvke.git"
	const mirror = "https://github.com/dsx-ai-factory/nvke"
	environment, err := definition.LoadEnvironment(t.Context(), filepath.Join(root, "environment-github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if access, ok := environment.SourceAccess[mirror]; !ok || access.Mirror != mirror || access.CredentialRef != "github:NVKE_SOURCE_TOKEN" {
		t.Fatal("existing CLI/controller source access was changed")
	}
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, output := range authored.Outputs {
		if output.Format != definition.OutputDiskImage {
			continue
		}
		seen[output.Platform] = true
		t.Run(output.Name, func(t *testing.T) {
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, output.Catalog.Base)))
			if err != nil {
				t.Fatal(err)
			}
			selected, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format})
			if err != nil {
				t.Fatal(err)
			}
			producer := "nvke-node-image"
			if output.Platform == "linux/amd64" {
				producer += "-amd64"
			}
			graph, err := buildgraph.CompileForHostPlatforms(selected, map[string]execution.HostPlatform{producer: execution.HostPlatform(output.Platform)})
			if err != nil {
				t.Fatal(err)
			}
			// This real selected graph is a compiler diagnostic, not admission of
			// the candidate's unmeasured full inspection profiles.
			plan := &Plan{Definition: authored, Environment: environment, Builds: []BuildPlan{{Name: output.Name, CatalogSnapshot: cat, Graph: graph}}}
			privateFetches := 0
			for _, source := range graph.Nodes {
				node := Node{BuildTarget: output.Name, IRNode: source.ID, Kind: NodeBuild}
				if source.Kind == ir.KindFetch {
					node.Kind = NodeFetch
				}
				projected := githubSourceEnvironment(plan, node)
				if source.Kind != ir.KindFetch || source.Repo != repository {
					if projected[sourceTokenEnv] != "" {
						t.Errorf("credential projected outside selected private source fetch %s", source.ID)
					}
					continue
				}
				privateFetches++
				if projected[sourceTokenEnv] != "${{ secrets.NVKE_SOURCE_TOKEN }}" || projected["GIT_CONFIG_VALUE_0"] != repository || projected["GIT_CONFIG_KEY_0"] != "url."+mirror+".insteadOf" || projected["GIT_CONFIG_KEY_2"] != "credential."+mirror+".helper" {
					t.Errorf("private source fetch %s has no exact approved credential/mirror projection", source.ID)
				}
			}
			if privateFetches != 4 {
				t.Fatalf("got %d private source fetches, want all four selected NVKE sources", privateFetches)
			}
		})
	}
	if len(seen) != 2 || !seen["linux/amd64"] || !seen["linux/arm64"] {
		t.Fatal("both native disk source-access projections are required")
	}
}
