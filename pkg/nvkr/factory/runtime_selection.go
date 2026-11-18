// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// selectedRuntimeProducers derives content inputs from validated producer graphs,
// never from an Environment-supplied digest or an independently authored Plan row.
func selectedRuntimeProducers(input definition.Definition, environment definition.Environment, builds []BuildPlan) (map[string]ir.RuntimeSelection, error) {
	if len(environment.Runtime.ProducedRuntimes) == 0 {
		return nil, nil
	}
	tools, err := requiredInternalTools(input, builds)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]ir.RuntimeSelection, len(environment.Runtime.ProducedRuntimes))
	used := make(map[string]bool, len(environment.Runtime.ProducedRuntimes))
	for _, output := range sortedKeys(environment.Runtime.ProducedRuntimes) {
		name := environment.Runtime.ProducedRuntimes[output]
		found := false
		for _, target := range input.Outputs {
			if target.Name == output && target.Format == definition.OutputDiskImage {
				found = true
			}
		}
		if !found || used[name] {
			return nil, e.New(e.CodeEngineCapability, "runtime %q requires one distinct selected disk output %q", name, output)
		}
		used[name] = true
		var producer InternalToolPlan
		for _, tool := range tools {
			if tool.Name == name {
				producer = tool
			}
		}
		if producer.Name == "" || producer.Format != definition.OutputOCI {
			return nil, e.New(e.CodeEngineCapability, "runtime %q requires its exact native OCI internal producer", name)
		}
		for _, build := range builds {
			if build.Name != producer.BuildTarget {
				continue
			}
			for _, node := range build.Graph.Nodes {
				if node.Runtime != nil || node.BuilderType == ir.BuilderVMImage {
					return nil, e.New(e.CodeEngineCapability, "runtime %q cannot depend on VM runtime consumers", name)
				}
			}
		}
		selection := ir.RuntimeSelection{Tool: name, BuildDigest: producer.BuildDigest, Component: producer.Component,
			Platform: producer.Platform, Invocation: ir.RuntimeInvocation}
		if err := selection.Validate(); err != nil {
			return nil, err
		}
		selected[output] = selection
	}
	return selected, nil
}
