// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

// This file validates NVIDIA's supplied OVN output selections through generic NKD APIs.
// Component-specific Go tests are not a requirement for adding a catalog.

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestNKEOVNForkReleaseOutputs(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	const definitionPath = "definitions/nke-ovn-source/definition.yaml"
	const environmentPath = "definitions/nke-ovn-source/environment-github.yaml"
	authored, err := definition.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := definition.LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	compile := func(d definition.Definition, env definition.Environment) (*factory.Plan, error) {
		return factory.CompileDocuments(t.Context(), definitionPath, d, environmentPath, env, "dev (unknown)")
	}
	plan, err := compile(authored, environment)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != 4 || len(authored.Outputs) != 4 {
		t.Fatal("expected two images and two bound charts")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		image := "package/ovn-image-" + arch
		found := false
		for _, node := range plan.Graph.Nodes {
			if node.ID != "package/ovn-chart-"+arch {
				continue
			}
			found = true
			if !slices.Contains(node.Inputs, image) || node.HelmImage == nil || len(node.HelmImage.Images) != 6 {
				t.Fatalf("lost chart dependency: %+v", node)
			}
			for _, binding := range node.HelmImage.Images {
				if binding.From != image || binding.Values.DigestFormat != "sha256-hex" {
					t.Fatalf("wrong native image/digest representation: %+v", binding)
				}
			}
		}
		if !found {
			t.Fatal("missing native chart package")
		}
	}
	imageBuilds := 0
	for _, node := range plan.Graph.Nodes {
		if node.IRNode == "" {
			continue
		}
		execution, err := plan.ExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		want := factory.DefaultRunsOn
		if execution.Platform == "linux/arm64" {
			want = "ubuntu-24.04-arm"
		}
		if execution.RunsOn != want {
			t.Fatalf("%s routes %s to %s, want %s", node.ID, execution.Platform, execution.RunsOn, want)
		}
	}
	for _, build := range plan.Builds {
		for _, node := range build.Graph.Nodes {
			if node.Kind == ir.KindBuild && node.Component == "ovn-kube-ubuntu" {
				imageBuilds++
			}
		}
	}
	if imageBuilds != 2 {
		t.Fatalf("got %d native OVN builds, want exactly two", imageBuilds)
	}
	changed := environment.Clone()
	changed.Publication.RepositoryPrefix = "ghcr.io/example/ovn"
	relocated, err := compile(authored, changed)
	if err != nil {
		t.Fatal(err)
	}
	for i, build := range plan.Builds {
		if build.BuildDigest != relocated.Builds[i].BuildDigest {
			t.Fatal("publication destination changed build identity")
		}
	}
	explicit := environment.Clone()
	explicit.RunsOn = "ubuntu-24.04"
	explicitPlan, err := compile(authored, explicit)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range explicitPlan.Graph.Nodes {
		execution, err := explicitPlan.ExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		if execution.RunsOn != explicit.RunsOn {
			t.Fatal("automatic native route replaced an explicit runner selection")
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*definition.Definition)
	}{
		{name: "missing slot", change: func(d *definition.Definition) { delete(d.Outputs[0].Helm.Images, "node") }},
		{name: "wrong architecture", change: func(d *definition.Definition) { d.Outputs[0].Helm.Images["node"] = "ovn-image-arm64" }},
		{name: "missing output", change: func(d *definition.Definition) { d.Outputs[0].Helm.Images["node"] = "missing" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := authored.Clone()
			tc.change(&d)
			_, err := compile(d, environment)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("expected invalid-catalog, got %v", err)
			}
		})
	}
	bundle, err := factory.Render(t.Context(), plan, factory.EngineGitHub)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := factory.WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	regenerated, err := factory.Render(t.Context(), plan, factory.EngineGitHub)
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.CheckGeneratedBundle(t.Context(), root, regenerated); err != nil {
		t.Fatal(err)
	}
	workflow := bundle.Files[bundle.WorkflowPath]
	if !bytes.Equal(workflow, regenerated.Files[regenerated.WorkflowPath]) {
		t.Fatal("nondeterministic workflow")
	}
	for _, want := range []string{"ubuntu-latest", "ubuntu-24.04-arm", "packages: write", "id-token: write", "NKE_SOURCE_TOKEN", "nkd-release"} {
		if !strings.Contains(string(workflow), want) {
			t.Fatalf("missing generated contract %q", want)
		}
	}
	if strings.Contains(string(workflow), "pull_request_target") || strings.Contains(string(workflow), "self-hosted") {
		t.Fatal("candidate must retain the ordinary hosted route")
	}
}
