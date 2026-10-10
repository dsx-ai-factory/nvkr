// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// The sole generated executable document is the complete Plan; its exact
// canonical bytes and semantic digest must agree with every provider command.
func TestCommittedPlanMatchesGeneratedExecution(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	for _, engine := range []string{EngineGitHub} {
		t.Run(engine, func(t *testing.T) {
			bundle, err := Render(t.Context(), plan, engine)
			if err != nil {
				t.Fatal(err)
			}
			if len(bundle.Files) != 2 {
				t.Fatalf("expected only Plan and workflow, got %d members", len(bundle.Files))
			}
			var decoded Plan
			if err := canonical.UnmarshalExact(bundle.Files[bundle.PlanPath], &decoded); err != nil {
				t.Fatal(err)
			}
			digest, err := decoded.Digest()
			if err != nil || digest != bundle.PlanDigest {
				t.Fatalf("committed Plan identity differs: %s %v", digest, err)
			}
			workflow := string(bundle.Files[bundle.WorkflowPath])
			if !strings.Contains(workflow, "# planDigest: "+digest) || strings.Contains(workflow, "--nodes-digest") || strings.Contains(workflow, "/graphs/") {
				t.Fatal("workflow uses a parallel producer graph")
			}
			for _, node := range plan.Graph.Nodes {
				command := "factory github run-node --plan " + bundle.PlanPath + " --node " + shellQuote(node.ID)
				if !strings.Contains(workflow, command) {
					t.Fatalf("workflow omits exact Plan/node command: %s", command)
				}
			}
			for _, mutation := range []string{"noncanonical Plan", "extra graph"} {
				t.Run(mutation, func(t *testing.T) {
					changed := *bundle
					changed.Files = make(map[string][]byte, len(bundle.Files)+1)
					for path, raw := range bundle.Files {
						changed.Files[path] = append([]byte(nil), raw...)
					}
					if mutation == "noncanonical Plan" {
						changed.Files[bundle.PlanPath] = append(changed.Files[bundle.PlanPath], '\n')
					} else {
						changed.Files[".github/nkd-run/graphs/unused.json"] = []byte("{}")
					}
					assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodePipelineDrift)
				})
			}
		})
	}
}

// buildOnlyPlan compiles a plan from a delivery profile with NO routes/targets — the build-only
// mode — optionally overriding the runner via the delivery's runsOn.
func buildOnlyPlan(t *testing.T, deliveryRunsOn string) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, delivery := fixtureInputs("buildonly", "worker", deliveryRunsOn)
	plan, err := Compile(context.Background(), definition, delivery, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatalf("a build-only delivery (no routes) must compile: %v", err)
	}
	plan.DefinitionPath = "examples/factory.yaml"
	plan.EnvironmentPath = "examples/environment.yaml"
	return plan
}

// TestBuildOnlyDeliveryPublishesNothing is the core guarantee of the build-only mode: no publish
// node in the graph, and no publish/credential/registry surface in the generated pipeline.
func TestBuildOnlyDeliveryPublishesNothing(t *testing.T) {
	plan := buildOnlyPlan(t, "")
	for _, n := range plan.Graph.Nodes {
		if n.Kind == NodePublish {
			t.Fatalf("build-only delivery must emit no publish node; found %q", n.ID)
		}
	}
	for _, engine := range []struct {
		name   string
		render func() (*GeneratedBundle, error)
		path   string
	}{
		{"github", func() (*GeneratedBundle, error) { return RenderGitHub(context.Background(), plan) }, GitHubWorkflowPath},
	} {
		b, err := engine.render()
		if err != nil {
			t.Fatalf("%s build-only render: %v", engine.name, err)
		}
		wf := string(b.Files[engine.path])
		for _, forbidden := range []string{"NKD_CREDENTIAL", "publish/", "docker login", "CI_REGISTRY"} {
			if strings.Contains(wf, forbidden) {
				t.Fatalf("%s build-only pipeline must not contain %q:\n%s", engine.name, forbidden, wf)
			}
		}
	}
}

// TestCommittedCanonicalMembersHaveNoTrailingNewline guards every canonical bundle member
// (Plan) that is read back at runtime with canonical.UnmarshalExact,
// which rejects a trailing newline. Only the workflow (.yml) may end with one.
func TestCommittedCanonicalMembersHaveNoTrailingNewline(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	b, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for path, raw := range b.Files {
		if strings.HasSuffix(path, ".yml") {
			continue
		}
		if strings.HasSuffix(string(raw), "\n") {
			t.Fatalf("canonical member %s ends with a newline; UnmarshalExact will reject it at runtime", path)
		}
	}
}

// Tool acquisition is a Run operation; a catalog producer node carries an
// explicit reference into its output's authoritative build graph.
func TestToolAcquireNodeIsNotIRProjected(t *testing.T) {
	if irProjectedNode(Node{ID: "tool/cosign", Kind: NodeAcquire}) {
		t.Fatal("a tool acquire node (no BuildTarget/IRNode) must not be IR-projected")
	}
	if !irProjectedNode(Node{ID: "gpu/pull-x", Kind: NodeAcquire, BuildTarget: "gpu", IRNode: "pull-x"}) {
		t.Fatal("a build-projected acquire node must be IR-projected")
	}
	if irProjectedNode(Node{ID: "package/tools", Kind: NodePackage}) {
		t.Fatal("a package operation must not be IR-projected")
	}
}

// TestDeliveryRuntimeConfigRendersOnGitHub covers the authored runtime image,
// services, variables and setup on the supported hosted engine.
func TestDeliveryRuntimeConfigRendersOnGitHub(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	plan.Environment.Runtime.RuntimeImage = "docker:27@sha256:" + strings.Repeat("a", 64)
	plan.Environment.Runtime.Services = []string{"docker:dind@sha256:" + strings.Repeat("b", 64)}
	plan.Environment.Runtime.Variables = map[string]string{"DOCKER_HOST": "tcp://docker:2375"}
	plan.Environment.Runtime.Setup = []string{"docker login -u user -p pass registry.example"}
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest

	gh, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	ghwf := string(gh.Files[GitHubWorkflowPath])
	for _, want := range []string{"container: 'docker:27@sha256:", "image: 'docker:dind@sha256:",
		"DOCKER_HOST: 'tcp://docker:2375'", "- name: Configure build environment", "docker login -u user -p pass registry.example"} {
		if !strings.Contains(ghwf, want) {
			t.Fatalf("github runtime config missing %q", want)
		}
	}
}

// TestEnvironmentSelectsExecutionRunner proves the runner is environment config: a
// delivery runsOn overrides the definition's default on every emitted node.
func TestEnvironmentSelectsExecutionRunner(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	if len(plan.Graph.Nodes) == 0 {
		t.Fatal("expected a non-empty graph")
	}
	for _, n := range plan.Graph.Nodes {
		n, err := plan.ExecutionFor(n)
		if err != nil {
			t.Fatal(err)
		}
		if n.RunsOn != "ubuntu-24.04" {
			t.Fatalf("delivery runsOn must override the definition default; node %q RunsOn=%q", n.ID, n.RunsOn)
		}
	}
}
