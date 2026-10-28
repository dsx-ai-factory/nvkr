// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func githubPartitionTestPlan(t *testing.T) *Plan {
	t.Helper()
	plan := githubTestPlan(t)
	plan.Environment.GitHubExecutionPartitions = true
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	return plan
}

func TestGitHubPartitionBundleAuthenticatesAllMembers(t *testing.T) {
	plan := githubPartitionTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var workers []string
	for _, name := range sortedFileNames(bundle.Files) {
		if githubPartitionWorkflowSuffix.MatchString(name) {
			workers = append(workers, name)
		}
	}
	if len(workers) == 0 {
		t.Fatal("partition mode emitted no workers")
	}
	for _, test := range []struct {
		name   string
		code   e.Code
		mutate func(map[string][]byte)
	}{
		{"missing worker", e.CodePipelineDrift, func(files map[string][]byte) { delete(files, workers[0]) }},
		{"extra worker", e.CodePipelineDrift, func(files map[string][]byte) {
			files[strings.TrimSuffix(bundle.WorkflowPath, ".yml")+"__partition-49.yml"] = files[workers[0]]
		}},
		{"changed worker command", e.CodeEngineCapability, func(files map[string][]byte) {
			files[workers[0]] = bytes.Replace(files[workers[0]], []byte("run: ':'"), []byte("run: exit 0"), 1)
		}},
		{"caller dependency substitution", e.CodeEngineCapability, func(files map[string][]byte) {
			files[bundle.WorkflowPath] = bytes.Replace(files[bundle.WorkflowPath], []byte("outputs.results"), []byte("outputs.forged"), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := maps.Clone(bundle.Files)
			test.mutate(files)
			if maps.EqualFunc(files, bundle.Files, bytes.Equal) {
				t.Fatal("mutation did not change bundle")
			}
			changed := newGeneratedBundle(EngineGitHub, githubLayoutFor(plan), files, bundle.PlanDigest)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), test.code)
		})
	}
	root := t.TempDir()
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	plan.Environment.GitHubExecutionPartitions = false
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	flat, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteGeneratedBundle(t.Context(), root, flat); err != nil {
		t.Fatal(err)
	}
	for _, name := range workers {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("worker was not retired: %s: %v", name, err)
		}
	}
}

func TestGitHubPartitionBundleMaintainsDefinitionAndBuildIdentities(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	definitionDigest := plan.DefinitionDigest
	builds := make(map[string]string, len(plan.Builds))
	for _, build := range plan.Builds {
		builds[build.Name] = build.BuildDigest
	}
	plan.Environment.GitHubExecutionPartitions = true
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ManifestFor(bundle); err != nil {
		t.Fatal(err)
	}
	if plan.DefinitionDigest != definitionDigest {
		t.Fatal("partitioning changed Definition identity")
	}
	for _, build := range plan.Builds {
		if builds[build.Name] != build.BuildDigest {
			t.Fatal("partitioning changed native build identity")
		}
	}
	if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubPartitionBundleModes(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"development", githubDevelopmentTestPlan},
		{"runtime plan", githubRuntimePlanFixture},
		{"named", func(t *testing.T) *Plan { return namedGitHubTestPlan(t, "partitioned") }},
		{"build only", func(t *testing.T) *Plan { return buildOnlyPlan(t, "ubuntu-24.04") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			plan.Environment.GitHubExecutionPartitions = true
			var err error
			plan.EnvironmentDigest, err = plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := ManifestFor(bundle); err != nil {
				t.Fatal(err)
			}
			workers := map[string][]byte{}
			for path, raw := range bundle.Files {
				if githubPartitionWorkflowSuffix.MatchString(path) {
					workers[path] = raw
				}
			}
			assertGitHubPartitionMembership(t, plan, bundle.Files[bundle.WorkflowPath], workers)
		})
	}
}
