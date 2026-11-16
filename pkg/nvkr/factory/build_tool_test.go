// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

func TestBuildToolRunInputAndEvidence(t *testing.T) {
	definition, environment, stack := pinnedToolFactoryInputs(t, func(cat *catalog.Catalog, definition *authored.Definition) {
		delete(cat.Components, "recipe")
		definition.Outputs[0].Components = []string{"consumer"}
		consumer := cat.Components["consumer"]
		consumer.Materials = nil
		consumer.BuildTool = map[string]string{"linux/amd64": "aicr"}
		consumer.BuildToolPins = map[string]catalog.ToolPin{"linux/amd64": pinnedToolFixturePin}
		consumer.BuildCommand = []string{"make", "TOOL=" + catalog.BuildToolToken}
		cat.Components["consumer"] = consumer
	})
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	nodes, artifacts := planIndex(plan)
	consumer := nodes["rendered/build-consumer"]
	if !slices.Contains(consumer.Inputs, "tool/aicr") || artifacts["tool/aicr"].Kind != "pipeline-tool" {
		t.Fatal("source producer omitted its shared Run-only executable input")
	}
	_, instruction, err := plan.producerInstruction(consumer)
	if err != nil {
		t.Fatal(err)
	}
	if instruction.BuildTool == nil || instruction.Command[1] != "TOOL="+ir.BuildToolPath {
		t.Fatal("compiler lost the bound invocation")
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if productionBuildType(plan) != provenance.BuildTypeWrappedBuild {
		t.Fatal("upstream release executable gained a hermetic claim")
	}
	if err := requirePinnedToolDependencies(plan, resolvedDependencies(plan)); err != nil {
		t.Fatal(err)
	}
	if err := requirePinnedToolDependencies(plan, nil); err == nil {
		t.Fatal("missing tool dependencies were accepted")
	}
	consumer.Inputs = slices.DeleteFunc(consumer.Inputs, func(s string) bool { return s == "tool/aicr" })
	for i := range plan.Graph.Nodes {
		if plan.Graph.Nodes[i].ID == consumer.ID {
			plan.Graph.Nodes[i] = consumer
		}
	}
	if err := plan.Validate(); err == nil {
		t.Fatal("consumer without its exact tool edge was accepted")
	}
}
