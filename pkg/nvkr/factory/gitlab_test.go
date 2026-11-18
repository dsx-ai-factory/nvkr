// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	yaml "go.yaml.in/yaml/v3"
)

func gitlabFixture(t *testing.T, outputs int) (string, *Plan) {
	return gitlabFixtureAt(t, outputs, "https://gitlab.example.com/api/v4")
}

func gitlabFixtureAt(t *testing.T, outputs int, apiURL string) (string, *Plan) {
	t.Helper()
	root, definition, environment := snapshotFixture(t)
	definition.Outputs = definition.Outputs[:outputs]
	environment.RunsOn = ""
	environment.GitLab = &authored.GitLab{APIURL: apiURL, ProjectID: 123, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: map[string]authored.GitLabRunner{"linux/amd64": {Tags: []string{"runner: native"}, BootstrapImage: "registry.example.com/go@sha256:" + strings.Repeat("a", 64)}}}
	writeSnapshotFixtureDocument(t, root, snapshotDefinition, definition)
	writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
	plan, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	return root, plan
}

func TestGitLabCompleteGraphProjectionDifferentAuthoredSelections(t *testing.T) {
	sizes := []int{}
	for _, outputs := range []int{1, 2} {
		t.Run(string(rune('0'+outputs)), func(t *testing.T) {
			_, plan := gitlabFixture(t, outputs)
			bundle, err := Render(t.Context(), plan, " GitLab ")
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := yaml.Unmarshal(bundle.Files[GitLabWorkflowPath], &document); err != nil {
				t.Fatal(err)
			}
			workflow := document["workflow"].(map[string]any)
			rules := workflow["rules"].([]any)
			if rules[0].(map[string]any)["when"] != "never" || !strings.Contains(rules[0].(map[string]any)["if"].(string), `$CI_DEBUG_TRACE != "false"`) {
				t.Fatal("native pre-start debug guard missing")
			}
			graph, err := projectGitLabGraph(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, len(graph.Operations))
			_, artifacts := planIndex(plan)
			for _, op := range graph.Operations {
				if _, ok := document[op.Job]; !ok {
					t.Fatalf("missing operation %s", op.Node.ID)
				}
				if len(op.Inputs) != len(op.Node.Inputs) || len(op.Outputs) != len(op.Node.Outputs) {
					t.Fatal("transport dropped full graph membership")
				}
				for i, input := range op.Inputs {
					if input != artifacts[op.Node.Inputs[i]] {
						t.Fatal("wrong source-owned input association")
					}
				}
				job := document[op.Job].(map[string]any)
				tags := job["tags"].([]any)
				if tags[0] != "runner: native" {
					t.Fatal("YAML encoder changed literal runner tag")
				}
				script := job["script"].([]any)[0].(string)
				if strings.Contains(script, "run generate") {
					t.Fatal("source compilation precedes native role admission")
				}
			}
			names, err := GitLabJobNames(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != len(plan.Graph.Nodes)+2 {
				t.Fatalf("native inventory count %d", len(names))
			}
			again, err := RenderGitLab(t.Context(), plan)
			if err != nil || !reflect.DeepEqual(bundle, again) {
				t.Fatalf("generation nondeterminism: %v", err)
			}
		})
	}
	if len(sizes) != 2 || sizes[0] == sizes[1] {
		t.Fatal("different authored topology was not exercised")
	}
}

func TestGitLabBundleCustodyAndTampering(t *testing.T) {
	root, plan := gitlabFixture(t, 1)
	if _, err := Generate(t.Context(), plan, EngineGitLab, root, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(t.Context(), plan, " GitLab ", root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, GitLabWorkflowPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte("# tampered\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(t.Context(), plan, EngineGitLab, root); err == nil {
		t.Fatal("changed transport accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitlab-ci.yml")); !os.IsNotExist(err) {
		t.Fatal("generator wrote operator root")
	}
	assertFactoryErrorCode(t, func() error { _, err := RenderGitHub(t.Context(), plan); return err }(), e.CodeEngineCapability)
}

func TestGitLabStrictRootProfile(t *testing.T) {
	valid := "include:\n  - local: .gitlab/nvkr-run/pipeline.yml\n"
	if err := gitlabRootProfile([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{valid + "default: {}\n", valid + "---\n[\n", valid + "---\n{}\n", "include:\n - local: other.yml\n", valid + "include: []\n"} {
		if err := gitlabRootProfile([]byte(raw)); err == nil {
			t.Fatalf("unsupported effective config accepted: %q", raw)
		}
	}
}

func TestGitLabFinalizerCloseCancelsAndJoinsSharedHandle(t *testing.T) {
	lifetime, cancel := context.WithCancel(t.Context())
	owner := &GitLabFinalizer{state: &gitlabAdmission{lifetime: lifetime, cancel: cancel}}
	copy := *owner
	owner.state.mu.Lock()
	finished := make(chan struct{})
	go func() { defer close(finished); _ = copy.Close() }()
	select {
	case <-lifetime.Done():
	case <-time.After(time.Second):
		t.Fatal("Close failed to cancel active acquisition")
	}
	select {
	case <-finished:
		t.Fatal("Close returned before active work joined")
	default:
	}
	owner.state.mu.Unlock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Close did not join")
	}
	if !owner.state.closed {
		t.Fatal("shallow handle did not revoke shared admission")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.state.current(t.Context()); err == nil {
		t.Fatal("closed admission accepted")
	}
}

func TestGitLabNativeImageBoundChartProjection(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	chart := base.Components["containerd"]
	chart.OutputType = "tarball"
	chart.ArtifactPath = "bin/chart.tgz"
	chart.WriteDir = "bin"
	chart.BuildCommand = []string{"tar", "-czf", "/src/bin/chart.tgz", "chart"}
	chart.Helm = &catalog.HelmPackaging{Chart: ".", Images: map[string]catalog.HelmImage{"controller": {Values: catalog.HelmImageValues{Digest: "image.digest"}}}}
	base.Components["chart-source"] = chart
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("gitlab-image-chart", "cli", "")
	definition.Outputs = []authored.DefinitionOutput{
		{Name: "image", Platform: "linux/amd64", Format: authored.OutputOCI, Components: []string{"coredns"}},
		{Name: "chart", Platform: "linux/amd64", Format: authored.OutputHelm, Components: []string{"chart-source"}, Helm: &authored.HelmOutput{Images: map[string]string{"controller": "image"}}},
		{Name: "cli", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"containerd"}},
	}
	slices.SortFunc(definition.Outputs, func(a, b authored.DefinitionOutput) int { return strings.Compare(a.Name, b.Name) })
	environment.GitLab = &authored.GitLab{APIURL: "https://gitlab.example.com/api/v4", ProjectID: 123, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: map[string]authored.GitLabRunner{"linux/amd64": {Tags: []string{"native"}, BootstrapImage: "registry.example.com/go@sha256:" + strings.Repeat("a", 64)}}}
	plan, err := compileDocumentsWithLoader(t.Context(), "config/definition.json", definition, "config/environment.json", environment, "v0.0.0 (test)", func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectGitLabGraph(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, operation := range graph.Operations {
		if operation.Node.ID != "package/chart" {
			continue
		}
		for _, artifact := range operation.Inputs {
			if artifact.ID == "package/image" {
				found = true
			}
		}
		if !slices.Contains(operation.Needs, gitlabNodeJob("package/image")) {
			t.Fatal("image-bound chart lost actual image scheduling prerequisite")
		}
	}
	if !found {
		t.Fatal("chart lost exact native image transport edge")
	}
	if _, err := RenderGitLab(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	// The native publication Plan must retain the complete release and publish
	// route; the adapter may not manufacture a local-only graph or skip a node.
	environment.Publication = &authored.Publication{RepositoryPrefix: "registry.example.com/release", Anonymous: true}
	published, err := compileDocumentsWithLoader(t.Context(), "config/definition.json", definition, "config/environment.json", environment, "v0.0.0 (test)", func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
	if err != nil {
		t.Fatal(err)
	}
	root := planNode(published, "package/release")
	if root.Kind != NodePackage || root.PackageKind != PackageOCI {
		t.Fatal("published Plan did not derive the native OCI release package")
	}
	for _, input := range []string{"package/image", "package/chart", "package/cli"} {
		if !slices.Contains(root.Inputs, input) {
			t.Fatalf("release package lost required input %q", input)
		}
	}
	publisher := planNode(published, "publish/release")
	if publisher.Kind != NodePublish || !slices.Contains(publisher.Inputs, "package/release") {
		t.Fatal("published Plan lost its native release publication route")
	}
	if _, err := RenderGitLab(t.Context(), published); err != nil {
		t.Fatalf("unsigned native release graph is not admitted: %v", err)
	}
	t.Run("same-project job token", func(t *testing.T) {
		env := environment
		env.Publication = &authored.Publication{RepositoryPrefix: "registry.example.com/release", CredentialRef: "gitlab:CI_JOB_TOKEN"}
		candidate, err := compileDocumentsWithLoader(t.Context(), "config/definition.json", definition, "config/environment.json", env, "v0.0.0 (test)", func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RenderGitLab(t.Context(), candidate); err != nil {
			t.Fatalf("native current-job registry route refused: %v", err)
		}
	})
	for _, tc := range []struct {
		name        string
		publication authored.Publication
	}{
		{"private credential", authored.Publication{RepositoryPrefix: "registry.example.com/release", CredentialRef: "github:REGISTRY_TOKEN"}},
		{"explicit insecure", authored.Publication{RepositoryPrefix: "registry.example.com/release", Anonymous: true, PlainHTTP: true}},
		{"implicit insecure", authored.Publication{RepositoryPrefix: "localhost:5000/release", Anonymous: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := environment
			env.Publication = &tc.publication
			candidate, err := compileDocumentsWithLoader(t.Context(), "config/definition.json", definition, "config/environment.json", env, "v0.0.0 (test)", func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
			if err != nil {
				t.Fatalf("fixture did not compile: %v", err)
			}
			assertFactoryErrorCode(t, ValidateGitLabPlan(t.Context(), candidate), e.CodeEngineCapability)
		})
	}
}

func TestGitLabNativeCheckoutSourceBinding(t *testing.T) {
	t.Chdir(t.TempDir())
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", args...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git: %v", err)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "--quiet")
	if err := os.WriteFile("authored.json", []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "--", "authored.json")
	git("-c", "user.name=Native Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "fixture")
	commit := git("rev-parse", "HEAD")
	if err := gitlabCheckout(t.Context(), commit, []string{"authored.json"}); err != nil {
		t.Fatal(err)
	}
	if err := gitlabCheckout(t.Context(), strings.Repeat("f", 40), []string{"authored.json"}); err == nil {
		t.Fatal("wrong authenticated source accepted")
	}
	if err := os.WriteFile("untracked.json", []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := gitlabCheckout(t.Context(), commit, []string{"untracked.json"}); err == nil {
		t.Fatal("untracked authored input accepted")
	}
	if err := os.WriteFile("authored.json", []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := gitlabCheckout(t.Context(), commit, []string{"authored.json"}); err == nil {
		t.Fatal("modified authenticated source accepted")
	}
	git("update-index", "--assume-unchanged", "authored.json")
	if err := gitlabCheckout(t.Context(), commit, []string{"authored.json"}); err == nil {
		t.Fatal("hidden worktree modifications accepted")
	}
}

// gitlabResolvedExecutionPlan compiles one real ARM64 executable selection
// through the ordinary catalog/compiler owners, with optional catalog-owned
// build requirements and an explicit native runner route set.
func gitlabResolvedExecutionPlan(t *testing.T, requirements *execution.Requirements, routes []string) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	if requirements != nil {
		component := base.Components["containerd"]
		component.BuildRequirements = execution.Clone(requirements)
		base.Components["containerd"] = component
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("gitlab-resolved-execution", "cli", "")
	definition.Outputs = []authored.DefinitionOutput{
		{Name: "cli", Platform: "linux/arm64", Format: authored.OutputExecutable, Components: []string{"containerd"}},
	}
	tags := map[string]string{"linux/amd64": "nvkr-utility-amd64", "linux/arm64": "nvkr-producer-arm64"}
	runners := map[string]authored.GitLabRunner{}
	for _, platform := range routes {
		runners[platform] = authored.GitLabRunner{Tags: []string{tags[platform]}, BootstrapImage: "registry.example.com/go@sha256:" + strings.Repeat("a", 64)}
	}
	environment.GitLab = &authored.GitLab{APIURL: "https://gitlab.example.com/api/v4", ProjectID: 123, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: runners}
	if requirements != nil {
		// The fixture class must admit the requirement it is paired with, including
		// an over-limit producer timeout, so the adapter's own native budget cap
		// stays the asserted refusal boundary. This stays within the execution
		// package's generic duration limit and never changes production behavior.
		limit := uint64(86400)
		if requirements.TimeoutSeconds > limit {
			limit = requirements.TimeoutSeconds
		}
		environment.ExecutionClasses = []execution.Class{{Name: "fixture-class", RunsOn: "fixture-route", HostPlatform: execution.HostPlatformLinuxARM64, Resources: execution.ResourceCapacity{CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 20480}, MaxTimeoutSeconds: limit}}
	}
	plan, err := compileDocumentsWithLoader(t.Context(), "config/definition.json", definition, "config/environment.json", environment, "v0.0.0 (test)",
		func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestGitLabResolvedProducerExecution proves the GitLab adapter consumes the
// Plan's resolved execution view for native producer work while utility
// placement, logical identity and requirement admission stay unchanged.
func TestGitLabResolvedProducerExecution(t *testing.T) {
	routes := []string{"linux/amd64", "linux/arm64"}
	resolve := func(t *testing.T, plan *Plan, node Node) string {
		t.Helper()
		platform, err := gitlabNodePlatform(plan, node)
		if err != nil {
			t.Fatal(err)
		}
		return platform
	}

	t.Run("resolved native producer routes keep utility placement", func(t *testing.T) {
		plan := gitlabResolvedExecutionPlan(t, nil, routes)
		bundle, err := RenderGitLab(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		used, err := gitlabUsedPlatforms(plan)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(used, []string{"linux/amd64", "linux/arm64"}) {
			t.Fatalf("used-platform inventory lost a resolved native route: %v", used)
		}
		names, err := GitLabJobNames(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		for _, platform := range routes {
			if !slices.Contains(names, gitlabBootstrapJob(platform)) {
				t.Fatalf("native inventory lost the %s bootstrap", platform)
			}
		}
		var document map[string]any
		if err := yaml.Unmarshal(bundle.Files[GitLabWorkflowPath], &document); err != nil {
			t.Fatal(err)
		}
		for _, platform := range routes {
			bootstrap := document[gitlabBootstrapJob(platform)].(map[string]any)
			script := bootstrap["script"].([]any)[0].(string)
			if !strings.Contains(script, "GOARCH="+strings.TrimPrefix(platform, "linux/")) {
				t.Fatalf("%s bootstrap did not build its own native helper", platform)
			}
		}
		producers, utilities := 0, 0
		for _, node := range plan.Graph.Nodes {
			job := document[gitlabNodeJob(node.ID)].(map[string]any)
			want := plan.Environment.GitLab.Runners[resolve(t, plan, node)]
			if got := fmt.Sprint(job["tags"].([]any)[0]); got != want.Tags[0] {
				t.Fatalf("operation %q selected tags %q for platform %q", node.ID, got, resolve(t, plan, node))
			}
			needs := job["needs"].([]any)
			if bootstrap := needs[0].(map[string]any)["job"]; bootstrap != gitlabBootstrapJob(resolve(t, plan, node)) {
				t.Fatalf("operation %q lost its resolved bootstrap transport entry", node.ID)
			}
			if node.IRNode != "" {
				producers++
				if resolve(t, plan, node) != "linux/arm64" {
					t.Fatalf("producer %q did not resolve its native platform", node.ID)
				}
				continue
			}
			utilities++
			if resolve(t, plan, node) != "linux/amd64" {
				t.Fatalf("utility operation %q changed its own placement", node.ID)
			}
		}
		if producers < 2 || utilities < 2 {
			t.Fatalf("fixture lost its mixed selection: producers=%d utilities=%d", producers, utilities)
		}
	})

	t.Run("absent producer route refuses before generated output", func(t *testing.T) {
		plan := gitlabResolvedExecutionPlan(t, nil, []string{"linux/amd64"})
		bundle, err := RenderGitLab(t.Context(), plan)
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
		if bundle != nil {
			t.Fatal("absent native route produced a generated bundle")
		}
	})

	t.Run("source-owned unsupported requirement refuses before generated output", func(t *testing.T) {
		plan := gitlabResolvedExecutionPlan(t, &execution.Requirements{Resources: &execution.ResourceRequest{CPUMillis: 4000, MemoryMiB: 8192}}, routes)
		assertFactoryErrorCode(t, ValidateGitLabPlan(t.Context(), plan), e.CodeEngineCapability)
		bundle, err := RenderGitLab(t.Context(), plan)
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
		if bundle != nil {
			t.Fatal("unsupported execution requirements produced a generated bundle")
		}
	})

	t.Run("resolved budgets and unchanged logical fields", func(t *testing.T) {
		plan := gitlabResolvedExecutionPlan(t, nil, routes)
		before, err := plan.Digest()
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := RenderGitLab(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		after, err := plan.Digest()
		if err != nil || before != after {
			t.Fatalf("generation changed logical Plan identity: %v", err)
		}
		for _, node := range plan.Graph.Nodes {
			if node.IRNode == "" {
				continue
			}
			if node.Platform != "" || node.Container != "" || node.Requirements != nil || node.ExecutionBinding != nil {
				t.Fatalf("generation wrote resolved execution fields into producer %q", node.ID)
			}
			budget, err := gitlabOperationBudget(plan, node)
			if err != nil {
				t.Fatal(err)
			}
			if budget != time.Hour {
				t.Fatalf("operation %q budget changed its default: %v", node.ID, budget)
			}
		}
		var document map[string]any
		if err := yaml.Unmarshal(bundle.Files[GitLabWorkflowPath], &document); err != nil {
			t.Fatal(err)
		}
		for _, node := range plan.Graph.Nodes {
			job := document[gitlabNodeJob(node.ID)].(map[string]any)
			if job["timeout"] != "4500 seconds" {
				t.Fatalf("operation %q rendered timeout %v", node.ID, job["timeout"])
			}
		}
	})

	// gitlabFixtureProducer returns the fixture's compiled containerd build
	// producer for the cli output. planNode returns a zero node for a missing
	// ID, so the helper asserts the exact compiled producer exists and is the
	// intended producer before any budget resolution observes it.
	gitlabFixtureProducer := func(t *testing.T, plan *Plan) Node {
		t.Helper()
		node := planNode(plan, "cli/build-containerd")
		if node.ID != "cli/build-containerd" || node.IRNode == "" || node.Kind != NodeBuild {
			t.Fatalf("fixture lost its intended compiled producer: %+v", node)
		}
		return node
	}

	t.Run("source-owned timeout is not silently defaulted", func(t *testing.T) {
		plan := gitlabResolvedExecutionPlan(t, &execution.Requirements{TimeoutSeconds: 7200}, routes)
		budget, err := gitlabOperationBudget(plan, gitlabFixtureProducer(t, plan))
		if err != nil {
			t.Fatal(err)
		}
		if budget != 7200*time.Second {
			t.Fatalf("source-owned timeout was read as the empty scheduler default: %v", budget)
		}
		overdue := gitlabResolvedExecutionPlan(t, &execution.Requirements{TimeoutSeconds: 100000}, routes)
		_, err = gitlabOperationBudget(overdue, gitlabFixtureProducer(t, overdue))
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	})
}

func TestGitLabRejectsSelectiveBootBeforeRendering(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Plan)
		code   e.Code
	}{
		{name: "compiled boot plan", code: e.CodeEngineCapability},
		{name: "missing compiled bounds", mutate: func(plan *Plan) { plan.BootPublication = nil }, code: e.CodeInvalidCatalog},
		{name: "substituted bounds", mutate: func(plan *Plan) { plan.BootPublication.Destinations[0].FinalReadBytes++ }, code: e.CodePipelineDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, _ := bootPlanFixture(t)
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(plan)
				assertFactoryErrorCode(t, plan.Validate(), test.code)
			}
			assertFactoryErrorCode(t, ValidateGitLabPlan(t.Context(), plan), test.code)
			bundle, err := RenderGitLab(t.Context(), plan)
			assertFactoryErrorCode(t, err, test.code)
			if bundle != nil {
				t.Fatal("unsupported or changed boot plan returned generated output")
			}
		})
	}
}

func TestGitLabRejectsUnsupportedExecutionConfigurationBeforeRendering(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*authored.Environment)
	}{
		{"missing exact route", func(e *authored.Environment) { delete(e.GitLab.Runners, "linux/amd64") }},
		{"arbitrary variables", func(e *authored.Environment) { e.Runtime.Variables = map[string]string{"CI_JOB_TOKEN": "forged"} }},
		{"setup hook", func(e *authored.Environment) { e.Runtime.Setup = []string{"arbitrary setup"} }},
		{"runtime startup override", func(e *authored.Environment) { e.Runtime.RuntimeImage = fixtureUtilityContainer }},
		{"helper override", func(e *authored.Environment) { e.Runtime.HelperImage = fixtureUtilityContainer }},
		{"undeclared execution root", func(e *authored.Environment) { e.ToolchainAccess.PublicRoots = []string{"example.test/unrelated"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, plan := gitlabFixture(t, 1)
			environment := plan.Environment
			test.change(&environment)
			writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
			changed, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "v0.0.0 (test)")
			if err != nil {
				return
			}
			bundle, err := RenderGitLab(t.Context(), changed)
			if err == nil || bundle != nil {
				t.Fatal("unsupported selected profile produced output")
			}
		})
	}
}
