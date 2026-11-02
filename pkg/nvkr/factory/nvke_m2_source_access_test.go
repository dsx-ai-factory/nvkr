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
	const root = "../../../inputs/definitions/nvke"
	const repository = "https://github.com/dsx-ai-factory/nvke.git"
	const mirror = "https://github.com/dsx-ai-factory/nvke"
	const dsxRepository = "https://github.com/dsx-ai-factory/project-dsx-identity"
	const ovnRepository = "https://github.com/dsx-ai-factory/ovn-kubernetes"
	privateRepositories := map[string]string{repository: mirror, ovnRepository: ovnRepository}
	environment, err := definition.LoadEnvironment(t.Context(), filepath.Join(root, "../../environments/github-release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if access, ok := environment.SourceAccess[mirror]; !ok || access.Mirror != mirror || access.CredentialRef != "github:NKE_SOURCE_TOKEN" {
		t.Fatal("existing CLI/controller source access was changed")
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
			privateFetches := make(map[string]int)
			for _, source := range graph.Nodes {
				if source.Repo == dsxRepository || source.Component == "dsx-imds-node-attestor-agent" {
					t.Fatal("temporary SPIRE rollback must not schedule a DSX fetch or build")
				}
				node := Node{BuildTarget: output.Name, IRNode: source.ID, Kind: NodeBuild}
				if source.Kind == ir.KindFetch {
					node.Kind = NodeFetch
				}
				projected := githubSourceEnvironment(plan, node)
				selectedMirror, private := privateRepositories[source.Repo]
				if source.Kind != ir.KindFetch || !private {
					if projected[sourceTokenEnv] != "" {
						t.Errorf("credential projected outside selected private source fetch %s", source.ID)
					}
					continue
				}
				privateFetches[source.Repo]++
				if projected[sourceTokenEnv] != "${{ secrets.NKE_SOURCE_TOKEN }}" || projected["GIT_CONFIG_VALUE_0"] != source.Repo || projected["GIT_CONFIG_KEY_0"] != "url."+selectedMirror+".insteadOf" || projected["GIT_CONFIG_KEY_2"] != "credential."+selectedMirror+".helper" {
					t.Errorf("private source fetch %s has no exact approved credential/mirror projection", source.ID)
				}
			}
			if privateFetches[repository] != 4 || privateFetches[ovnRepository] != 3 || len(privateFetches) != 2 {
				t.Fatalf("private source fetches = %v, want four NVKE and three OVN fork source closures only", privateFetches)
			}
		})
	}
	if len(seen) != 2 || !seen["linux/amd64"] || !seen["linux/arm64"] {
		t.Fatal("both native disk source-access projections are required")
	}
}
