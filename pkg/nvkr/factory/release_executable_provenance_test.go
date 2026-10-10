// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

func TestReleaseExecutableResolvedDependencies(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("../../../definitions/nvke-m2", "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"helm", "kubectl"} {
		t.Run(component, func(t *testing.T) {
			resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: []string{component}, Platform: "linux/amd64", OutputFormat: definition.OutputExecutable})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			dependencies := resolvedDependencies(&Plan{Builds: []BuildPlan{{Graph: graph}}})
			// These are synthetic provenance input identities, not a live Run.
			digest := "sha256:" + strings.Repeat("a", 64)
			started := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
			run, err := provenance.NewLocalRunContext(digest, started, started.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			predicate := provenance.ProductionPredicate(run, provenance.ProductionInputs{
				BuildType: provenance.BuildTypeAcquired, PlanDigest: digest, RunPlanReference: "release-member-fixture",
				DefinitionDigest: digest, EnvironmentDigest: digest, CatalogDigest: digest,
				GeneratorVersion: "fixture", SigningMode: provenance.SigningModeDryRun, ResolvedDependencies: dependencies,
			})
			if err := provenance.ValidateProduction(predicate); err != nil {
				t.Fatalf("resolved dependencies rejected by production owner: %v", err)
			}
			for _, node := range graph.Nodes {
				if node.Kind != ir.KindPull {
					continue
				}
				want := node.Digest
				if node.ExecutableArchive != nil {
					want = node.ExecutableArchive.Digest
				}
				found, member := false, false
				for _, dependency := range dependencies {
					if dependency.URI == node.Artifact {
						found = true
						if dependency.Digest["sha256"] != strings.TrimPrefix(want, "sha256:") {
							t.Errorf("transport URI %s has executable digest, want archive %s", dependency.URI, want)
						}
					}
					if strings.HasPrefix(dependency.URI, "nvkr:prebuilt/") && dependency.Digest["sha256"] == strings.TrimPrefix(node.Digest, "sha256:") {
						binding, err := url.Parse(dependency.URI)
						if err != nil {
							t.Fatal(err)
						}
						member = node.ExecutableArchive != nil && binding.Opaque == "prebuilt/"+component &&
							binding.Query().Get("archive") == node.Artifact && binding.Query().Get("member") == node.ExecutableArchive.Member
					}
				}
				if !found || (node.ExecutableArchive != nil && !member) {
					t.Fatalf("missing transport or selected member binding: %#v", dependencies)
				}
			}
		})
	}
}
