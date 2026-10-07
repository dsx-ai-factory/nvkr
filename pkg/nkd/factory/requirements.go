// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func rejectUnsupportedNodeRequirements(node Node, adapter string) error {
	if node.Requirements == nil {
		return nil
	}
	return e.New(e.CodeEngineCapability,
		"%s cannot enforce execution requirements for node %q", adapter, node.ID)
}

func rejectUnsupportedPlanRequirements(plan *Plan, adapter string) error {
	selected := make(map[string]map[string]struct{})
	for _, node := range plan.Graph.Nodes {
		if err := rejectUnsupportedNodeRequirements(node, adapter); err != nil {
			return err
		}
		if node.Kind == NodeBuild {
			if selected[node.BuildTarget] == nil {
				selected[node.BuildTarget] = make(map[string]struct{})
			}
			selected[node.BuildTarget][node.IRNode] = struct{}{}
		}
	}
	for _, build := range plan.Builds {
		for _, component := range build.ResolvedComponents {
			if component.BuildRequirements != nil {
				return e.New(e.CodeEngineCapability,
					"%s cannot enforce execution requirements for component %q", adapter, component.Name)
			}
		}
		selectedNodes := selected[build.Name]
		if len(selectedNodes) == 0 || build.Graph == nil {
			continue
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild {
				continue
			}
			if _, ok := selectedNodes[node.ID]; !ok {
				continue
			}
			if node.Requirements != nil {
				return e.New(e.CodeEngineCapability,
					"%s cannot enforce execution requirements for node %q", adapter, node.ID)
			}
		}
	}
	return nil
}

// rejectUnsupportedLocalPlanRequirements keeps ordinary requirement-bearing
// nodes closed while allowing vm-image requirements to reach the dedicated
// single-use host-admission boundary. It grants no execution authority: the
// local VM dispatcher must still consume AdmitLocalVMBuild before resolving a
// driver or touching its toolchain.
func rejectUnsupportedLocalPlanRequirements(plan *Plan, adapter string) error {
	selected := make(map[string]map[string]struct{})
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild {
			if selected[node.BuildTarget] == nil {
				selected[node.BuildTarget] = make(map[string]struct{})
			}
			selected[node.BuildTarget][node.IRNode] = struct{}{}
		}
		if node.Requirements == nil {
			continue
		}
		_, instruction, err := plan.producerInstruction(node)
		if err != nil || instruction.BuilderType != ir.BuilderVMImage {
			return e.New(e.CodeEngineCapability,
				"%s cannot enforce execution requirements for node %q", adapter, node.ID)
		}
	}
	for _, build := range plan.Builds {
		selectedNodes := selected[build.Name]
		vmComponents := make(map[string]struct{})
		if build.Graph != nil {
			for _, node := range build.Graph.Nodes {
				if node.Kind != ir.KindBuild {
					continue
				}
				if _, ok := selectedNodes[node.ID]; !ok {
					continue
				}
				if node.BuilderType == ir.BuilderVMImage {
					vmComponents[node.Component] = struct{}{}
					continue
				}
				if node.Requirements != nil {
					return e.New(e.CodeEngineCapability,
						"%s cannot enforce execution requirements for node %q", adapter, node.ID)
				}
			}
		}
		for _, component := range build.ResolvedComponents {
			if component.BuildRequirements == nil {
				continue
			}
			if _, ok := vmComponents[component.Name]; !ok {
				return e.New(e.CodeEngineCapability,
					"%s cannot enforce execution requirements for component %q", adapter, component.Name)
			}
		}
	}
	return nil
}
