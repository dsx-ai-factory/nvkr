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

func TestMaintainedM2OSFetchIsAnonymous(t *testing.T) {
	const root = "../../../inputs/definitions/nvke"
	const repository = "https://github.com/dsx-ai-factory/nvke.git"
	const dsxRepository = "https://github.com/dsx-ai-factory/project-dsx-identity"
	const ovnRepository = "https://github.com/dsx-ai-factory/ovn-kubernetes"
	publicRepositories := map[string]bool{repository: true, ovnRepository: true}
	environment, err := definition.LoadEnvironment(t.Context(), filepath.Join(root, "../../environments/github-release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(environment.SourceAccess) != 0 {
		t.Fatal("maintained public sources must not select credentials")
	}
	for _, path := range []string{filepath.Join(root, "../../environments/github-release.yaml"), filepath.Join(root, "../../environments/github-development.yaml"), "../../../inputs/environments/github-diagnostics.yaml"} {
		selected, err := definition.LoadEnvironment(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := selected.SourceAccess[dsxRepository]; exists {
			t.Fatal("temporary SPIRE rollback must not retain unused DSX credential access", path)
		}
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
			producer, err := buildgraph.DiskRootName(selected.Components)
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.CompileForHostPlatforms(selected, map[string]execution.HostPlatform{producer: execution.HostPlatform(output.Platform)})
			if err != nil {
				t.Fatal(err)
			}
			// This real selected graph is a compiler diagnostic, not admission of
			// the candidate's unmeasured full inspection profiles.
			plan := &Plan{Definition: authored, Environment: environment, Builds: []BuildPlan{{Name: output.Name, CatalogSnapshot: cat, Graph: graph}}}
			publicFetches := make(map[string]int)
			for _, source := range graph.Nodes {
				if source.Repo == dsxRepository || source.Component == "dsx-imds-node-attestor-agent" {
					t.Fatal("temporary SPIRE rollback must not schedule a DSX fetch or build")
				}
				node := Node{BuildTarget: output.Name, IRNode: source.ID, Kind: NodeBuild}
				if source.Kind == ir.KindFetch {
					node.Kind = NodeFetch
				}
				projected := githubSourceEnvironment(plan, node)
				if len(projected) != 0 {
					t.Errorf("public source received credential/mirror projection: %s", source.ID)
				}
				if source.Kind == ir.KindFetch && publicRepositories[source.Repo] {
					publicFetches[source.Repo]++
				}
			}
			if publicFetches[repository] != 5 || publicFetches[ovnRepository] != 3 || len(publicFetches) != 2 {
				t.Fatalf("public source fetches = %v, want five NVKE and three OVN fork source closures only", publicFetches)
			}
		})
	}
	if len(seen) != 2 || !seen["linux/amd64"] || !seen["linux/arm64"] {
		t.Fatal("both native disk source-access projections are required")
	}
}
