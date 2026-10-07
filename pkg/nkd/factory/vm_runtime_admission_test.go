// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

const vmTestGeneratorVersion = "test (test)"

func compiledVMNode(t *testing.T, policy execution.CapabilityPolicy) (*Plan, Node) {
	t.Helper()
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", policy)
	plan, err := Compile(t.Context(), definition, environment, stack, vmTestGeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild && node.IRNode != "" {
			return plan, node
		}
	}
	t.Fatal("compiled VM Plan has no build node")
	return nil, Node{}
}

func TestLocalVMRequestBindsExactPlanAndNode(t *testing.T) {
	plan, node := compiledVMNode(t, execution.CapabilityPreferred)
	request, err := localVMRequestForPlan(plan, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		t.Fatal(err)
	}
	currentDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if request.Subject != identity+"/"+currentDigest+"/"+node.ID ||
		!execution.Equal(request.Requirements, instruction.Requirements) ||
		!execution.EqualBinding(request.Binding, instruction.ExecutionBinding) {
		t.Fatalf("local VM request does not bind exact Plan node: %#v", request)
	}

	request.Requirements.Resources.MemoryMiB++
	request.Binding.Class.RunsOn = "changed"
	if execution.Equal(request.Requirements, instruction.Requirements) ||
		execution.EqualBinding(request.Binding, instruction.ExecutionBinding) {
		t.Fatal("returned local VM request aliases Plan storage")
	}
}

func TestLocalVMRequestRejectsPlanOrNodeSubstitution(t *testing.T) {
	plan, node := compiledVMNode(t, execution.CapabilityPreferred)
	var nonBuildNode string
	for _, candidate := range plan.Graph.Nodes {
		if candidate.Kind != NodeBuild {
			nonBuildNode = candidate.ID
			break
		}
	}
	if nonBuildNode == "" {
		t.Fatal("compiled VM Plan has no non-build node")
	}
	for _, test := range []struct {
		name   string
		plan   *Plan
		nodeID string
		mutate func(*Plan)
		code   e.Code
	}{
		{name: "nil Plan", nodeID: node.ID, code: e.CodeUsage},
		{name: "absent node", plan: plan, nodeID: "node-image/absent", code: e.CodeEngineCapability},
		{name: "non build node", plan: plan, nodeID: nonBuildNode, code: e.CodeEngineCapability},
		{name: "requirements substitution", plan: plan, nodeID: node.ID, code: e.CodePipelineDrift,
			mutate: func(changed *Plan) {
				for index := range changed.Builds[0].Graph.Nodes {
					if changed.Builds[0].Graph.Nodes[index].ID == node.IRNode {
						changed.Builds[0].Graph.Nodes[index].Requirements.Resources.MemoryMiB++
					}
				}
			}},
		{name: "binding substitution", plan: plan, nodeID: node.ID, code: e.CodePipelineDrift,
			mutate: func(changed *Plan) {
				for index := range changed.Builds[0].Graph.Nodes {
					if changed.Builds[0].Graph.Nodes[index].ID == node.IRNode {
						changed.Builds[0].Graph.Nodes[index].ExecutionBinding.Class.RunsOn = "other-route"
					}
				}
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.plan
			if candidate != nil && test.mutate != nil {
				raw, err := canonical.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
				candidate = new(Plan)
				if err := canonical.UnmarshalExact(raw, candidate); err != nil {
					t.Fatal(err)
				}
				test.mutate(candidate)
			}
			request, err := localVMRequestForPlan(candidate, test.nodeID)
			if request.Subject != "" || factoryErrorCode(err) != test.code {
				t.Fatalf("request = %#v, error = %v; want %s", request, err, test.code)
			}
		})
	}
}

func TestLocalVMRequestRejectsOrdinaryBuild(t *testing.T) {
	plan := buildOnlyPlan(t, "ordinary")
	var nodeID string
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild {
			nodeID = node.ID
			break
		}
	}
	if nodeID == "" {
		t.Fatal("ordinary fixture has no build node")
	}
	request, err := localVMRequestForPlan(plan, nodeID)
	if request.Subject != "" || factoryErrorCode(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "not a vm-image producer") {
		t.Fatalf("ordinary build request = %#v, error = %v", request, err)
	}
}

func TestLocalVMRequestBindsRelocatedPlanBytesNotOnlyLogicalIdentity(t *testing.T) {
	plan, node := compiledVMNode(t, execution.CapabilityPreferred)
	logical, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	plan.identityDigest = logical
	before, err := localVMRequestForPlan(plan, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan.GeneratorVersion = "changed-but-valid"
	if got, err := plan.IdentityDigest(); err != nil || got != logical {
		t.Fatalf("relocated logical identity = %q, %v; want %q", got, err, logical)
	}
	after, err := localVMRequestForPlan(plan, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Subject == after.Subject {
		t.Fatal("valid relocated Plan mutation retained runtime-admission subject")
	}
}
