// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"
)

// TestOperationSessionRouteInventory freezes where each target places every
// operation kind. Owned work was admitted by the local Run's whole Plan;
// admitted work passes the execute-node gate in process; contained work
// launches the pinned execute-node child. Hosted source fetch reaches its
// credential-scoped owner before the session, and hosted admission refuses raw
// Debian outputs, so neither has a hosted row.
func TestOperationSessionRouteInventory(t *testing.T) {
	ordinary := githubTestPlan(t)
	raw, _, _, _, _ := rawDebianFixture(t)
	for _, test := range []struct {
		name          string
		plan          *Plan
		local, hosted map[string]operationRoute
	}{
		{name: "ordinary", plan: ordinary,
			local: map[string]operationRoute{NodeFetch: routeOwned, NodeBuild: routeOwned, NodeVerify: routeAdmitted,
				NodeSBOM: routeContained, NodePackage: routeContained, NodePublish: routeAdmitted},
			hosted: map[string]operationRoute{NodeBuild: routeAdmitted, NodeVerify: routeContained,
				NodeSBOM: routeContained, NodePackage: routeContained, NodePublish: routeAdmitted}},
		{name: "raw debian", plan: raw,
			local: map[string]operationRoute{NodeFetch: routeOwned, NodeBuild: routeOwned, NodeVerify: routeOwned, NodeSBOM: routeOwned}},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := localOperationSession(test.plan, withLocalDefaults(LocalRunOptions{}), nil, nil)
			hosted := githubOperationSession(test.plan, GitHubStepOptions{})
			kinds := map[string]bool{}
			for _, node := range test.plan.Graph.Nodes {
				kinds[node.Kind] = true
				if got, want := local.route(node), test.local[node.Kind]; got != want {
					t.Errorf("local %s routes %s, want %s", node.ID, got, want)
				}
				if test.hosted == nil || node.Kind == NodeFetch {
					continue
				}
				execution, err := test.plan.GitHubExecutionFor(node)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := hosted.route(execution), test.hosted[node.Kind]; got != want {
					t.Errorf("hosted %s routes %s, want %s", node.ID, got, want)
				}
			}
			if len(kinds) != len(test.local) {
				t.Fatalf("fixture kinds %v differ from the frozen inventory %v", kinds, test.local)
			}
		})
	}
}
