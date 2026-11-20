// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
)

func TestGitLabDerivedOutputProjectionPreservesSharedBinding(t *testing.T) {
	root, original := gitlabFixture(t, 1)
	// Reuse the shared compiler's admitted Helm-plus-inventory fixture; derived
	// inventory does not describe executable outputs.
	definition, environment, stack := derivedTestInputs(t)
	environment.RunsOn = ""
	environment.GitLab = original.Environment.Clone().GitLab
	environment.Signing = nil
	input := definition.Outputs[0].Name
	environment.Publication = &authored.Publication{RepositoryPrefix: "registry.example.test/project", Anonymous: true}
	plan, err := compileDocumentsWithLoader(t.Context(), "definition.json", definition, "environment.json", environment, original.GeneratorVersion,
		func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitLab(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectGitLabGraph(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var binding *derivedoutput.Binding
	for _, required := range plan.LifecycleOutputs {
		if required.Name == "inventory" {
			binding = required.Derived
		}
	}
	if binding == nil || len(binding.Inputs) != 1 || binding.Inputs[0].Name != input {
		t.Fatal("shared compiler lost the exact derived sibling binding")
	}
	found := false
	for _, operation := range graph.Operations {
		if operation.Node.ID != "package/inventory" {
			continue
		}
		found = true
		if operation.Node.Kind != NodePackage || !reflect.DeepEqual(operation.Node.Derived, binding) || !containsString(operation.Node.Inputs, "package/"+input) {
			t.Fatal("GitLab projection changed the shared derivation or prerequisite")
		}
	}
	if !found {
		t.Fatal("GitLab projection omitted the derived output operation")
	}
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, GitLabPlanPath))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.LifecycleOutputs, plan.LifecycleOutputs) {
		t.Fatal("packed GitLab Plan lost derived output requirements")
	}
}
