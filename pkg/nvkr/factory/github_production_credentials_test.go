// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestGitHubProductionRegistryCredentialBoundaries(t *testing.T) {
	base := referenceTestPlan(t)
	environment := base.Environment.Clone()
	environment.Publication.CredentialRef = ProductionRegistryWriteCredential
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), base.Definition, environment, stack, base.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	selected := map[string]string{}
	for _, stage := range stages {
		job := document.Jobs[stage.ID]
		if job.Permissions["packages"] != "" {
			t.Fatal("production retains repository package access")
		}
		kind := stage.Nodes[0].Kind
		selected[kind] = stage.ID
		want := ""
		switch kind {
		case NodePublish, NodeSign:
			want = "${{ secrets.NVKR_REGISTRY_WRITE }}"
		case NodeVerifyPublication, NodeAssembleRelease:
			want = "${{ secrets.NVKR_REGISTRY_READ }}"
		}
		if job.Env["NVKR_CREDENTIAL"] != want {
			t.Fatalf("%s has wrong registry credential", kind)
		}
	}
	final := document.Jobs["nvkr-lifecycle-run"]
	if final.Permissions["packages"] != "" || final.Env["NVKR_CREDENTIAL"] != "${{ secrets.NVKR_REGISTRY_READ }}" {
		t.Fatal("finalizer lost independent reader")
	}
	for _, tc := range []struct{ name, kind, token string }{
		{"writer on verifier", NodeVerifyPublication, "${{ secrets.NVKR_REGISTRY_WRITE }}"},
		{"workflow token on publisher", NodePublish, "${{ secrets.GITHUB_TOKEN }}"},
		{"reader on signer", NodeSign, "${{ secrets.NVKR_REGISTRY_READ }}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			id := selected[tc.kind]
			job := doc.Jobs[id]
			job.Env["NVKR_CREDENTIAL"] = tc.token
			doc.Jobs[id] = job
			raw, err := yaml.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			changed := relowerGitHubTestBundle(t, plan, bundle, raw)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}
