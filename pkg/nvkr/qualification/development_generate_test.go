// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func developmentGenerationFixture(t *testing.T) (*factory.Plan, lifecycle.QualificationPolicy) {
	t.Helper()
	plan, err := factory.LoadPlanContext(t.Context(), "../../../.github/nvkr-dev/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := lifecycle.LoadQualificationPolicy(t.Context(), "../../../inputs/policies/nvke-development-qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return plan, policy
}

func TestDevelopmentQualificationGenerationAndIndependentOwnership(t *testing.T) {
	plan, policy := developmentGenerationFixture(t)
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "inputs/policies/nvke-development-qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	top := decodeQualificationWorkflow(t, bundle.files[development.QualificationWorkflow])
	if len(top.On) != 1 || top.On["workflow_dispatch"] == nil {
		t.Fatal("development trigger is not explicit dispatch")
	}
	if top.Jobs["qualify"].Permissions["packages"] != "" || top.Jobs["native-smoke"].Permissions["packages"] != "" {
		t.Fatal("development obtained repository package authority")
	}
	if top.Jobs["native-smoke"].Secrets[development.ReadSecret] == "" || top.Jobs["native-smoke"].Secrets[development.WriteSecret] != "" {
		t.Fatal("native smoke lost read/write credential separation")
	}
	for _, name := range []string{development.QualificationWorkflow, development.SmokeWorkflow} {
		raw := bundle.files[name]
		for _, required := range []string{"environment: nvkr-dev-release", "run qualification-source --policy", "steps.development-source.outputs.commit", "path: .nvkr/development-source", "persist-credentials: false"} {
			if !bytes.Contains(raw, []byte(required)) {
				t.Fatalf("%s missing %s", name, required)
			}
		}
		if strings.Contains(string(raw), "pull_request_target") || strings.Contains(string(raw), "environment: nvkr-release") {
			t.Fatal("development workflow crossed production trust boundary")
		}
	}
	root := t.TempDir()
	production, _ := generationBundle(t)
	if err := WriteWorkflows(t.Context(), root, production); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, QualificationManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkflows(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedPolicy(t.Context(), root, policy); err != nil {
		t.Fatal(err)
	}
	if err := CheckWorkflows(t.Context(), root, production); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, QualificationManifestPath))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("development changed production owner")
	}
	if err := WriteWorkflows(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
}

func TestDevelopmentQualificationGenerationRejectsMixedLanes(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*lifecycle.QualificationPolicy)
		code e.Code
	}{
		{"production mode", func(p *lifecycle.QualificationPolicy) { p.Source.Development = false }, e.CodeEngineCapability},
		{"production qualifier", func(p *lifecycle.QualificationPolicy) { p.QualificationWorkflow = ".github/workflows/nvkr-qualify.yml" }, e.CodeTrustPolicyMissing},
		{"production smoke", func(p *lifecycle.QualificationPolicy) { p.Smoke.Workflow = ".github/workflows/nvkr-smoke.yml" }, e.CodeTrustPolicyMissing},
		{"foreign repository", func(p *lifecycle.QualificationPolicy) { p.Source.Repository = "other/repo" }, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, policy := developmentGenerationFixture(t)
			test.edit(&policy)
			bundle, err := RenderWorkflows(t.Context(), plan, policy, "policy.yaml")
			requireCode(t, err, test.code)
			if bundle != nil {
				t.Fatal("refused policy emitted workflows")
			}
		})
	}
}

func TestDevelopmentQualificationRejectsForeignOwner(t *testing.T) {
	plan, policy := developmentGenerationFixture(t)
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "inputs/policies/nvke-development-qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	production, _ := generationBundle(t)
	if err := WriteWorkflows(t.Context(), root, production); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkflows(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	raw, err := production.manifestBytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, bundle.manifest.manifestPath()), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	requireCode(t, WriteWorkflows(t.Context(), root, bundle), e.CodePipelineDrift)
	if err := CheckWorkflows(t.Context(), root, production); err != nil {
		t.Fatal(err)
	}
}
