// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPlanRejectsActionPinsOutsideCatalogSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]catalog.ActionPin)
	}{
		{"missing action", func(pins map[string]catalog.ActionPin) { delete(pins, "checkout") }},
		{"extra action", func(pins map[string]catalog.ActionPin) { pins["other"] = pins["checkout"] }},
		{"renamed action", func(pins map[string]catalog.ActionPin) {
			pins["other"] = pins["checkout"]
			delete(pins, "checkout")
		}},
		{"repository", func(pins map[string]catalog.ActionPin) {
			pin := pins["checkout"]
			pin.Repo = "other/checkout"
			pins["checkout"] = pin
		}},
		{"release", func(pins map[string]catalog.ActionPin) {
			pin := pins["checkout"]
			pin.Version = "v7.0.2"
			pins["checkout"] = pin
		}},
		{"commit", func(pins map[string]catalog.ActionPin) {
			pin := pins["checkout"]
			pin.SHA = strings.Repeat("b", 40)
			pins["checkout"] = pin
		}},
		{"empty actions", func(pins map[string]catalog.ActionPin) { clear(pins) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			if err := plan.Validate(); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			tc.edit(plan.Actions)
			t.Run("validate", func(t *testing.T) {
				assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
			})
			t.Run("render", func(t *testing.T) {
				bundle, err := RenderGitHub(context.Background(), plan)
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				if bundle != nil {
					t.Fatal("modified action pins produced a workflow bundle")
				}
			})
		})
	}
}

func TestGitHubActionCatalogChangeRequiresRecompile(t *testing.T) {
	plan := githubTestPlan(t)
	before, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original := plan.Actions["checkout"]
	changed := original
	changed.SHA = strings.Repeat("b", 40)
	changed.Version = "v7.0.2"
	plan.CatalogSnapshot.Actions["checkout"] = changed
	stack, err := catalog.ApplyStack(plan.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	recompiled, err := Compile(context.Background(), plan.Definition, plan.Environment, stack, plan.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	recompiled.DefinitionPath, recompiled.EnvironmentPath = plan.DefinitionPath, plan.EnvironmentPath
	plan = recompiled
	after, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if before.PlanDigest == after.PlanDigest {
		t.Fatal("action change did not move Run identity")
	}
	if !bytes.Contains(after.Files[GitHubWorkflowPath], []byte(changed.Uses())) || bytes.Contains(after.Files[GitHubWorkflowPath], []byte(original.Uses())) {
		t.Fatal("renderer did not consume new pin")
	}
	if err := ValidateGitHubBundle(context.Background(), plan, before); err == nil {
		t.Fatal("old workflow accepted under new action pins")
	}
}
