// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

// The join rule is decided on the graph alone: an operation joins a job only
// when it shares its authority, runner and subject and adds no prerequisite.
func TestGitHubAuthorityStagesAddNoPrerequisite(t *testing.T) {
	node := func(id, kind, target string, inputs ...string) Node {
		return Node{ID: id, Kind: kind, BuildTarget: target, RunsOn: "ubuntu-24.04", Inputs: inputs, Outputs: []string{id + "-out"}}
	}
	for _, test := range []struct {
		name  string
		nodes []Node
		want  [][]string
	}{
		{"one target shares its source, producer and utilities", []Node{
			node("a-fetch", NodeFetch, "x"), node("b-build", NodeBuild, "x", "a-fetch-out"),
			node("c-observe", NodeVerify, "x", "b-build-out"), node("d-package", NodePackage, "", "b-build-out", "c-observe-out"),
		}, [][]string{{"a-fetch", "b-build", "c-observe", "d-package"}}},
		{"sibling source fetches of one target share a job", []Node{
			node("a-fetch", NodeFetch, "x"), node("b-fetch", NodeFetch, "x"), node("c-build", NodeBuild, "x", "a-fetch-out", "b-fetch-out"),
		}, [][]string{{"a-fetch", "b-fetch", "c-build"}}},
		{"two targets never share a job", []Node{
			node("a-build", NodeBuild, "x"), node("b-build", NodeBuild, "y"),
		}, [][]string{{"a-build"}, {"b-build"}}},
		{"an input from an unrelated job starts a new job", []Node{
			node("a-build", NodeBuild, "x"), node("b-build", NodeBuild, "y"),
			node("c-package", NodePackage, "", "a-build-out", "b-build-out"),
		}, [][]string{{"a-build"}, {"b-build"}, {"c-package"}}},
		{"an input from a job already awaited adds no prerequisite", []Node{
			node("a-build", NodeBuild, "x"), node("b-build", NodeBuild, "y", "a-build-out"),
			node("c-package", NodePackage, "", "a-build-out", "b-build-out"),
		}, [][]string{{"a-build"}, {"b-build", "c-package"}}},
		{"a root tool is awaited and never gains members", []Node{
			node("a-tool", NodeAcquire, ""), node("b-build", NodeBuild, "x"),
			node("c-sbom", NodeSBOM, "x", "a-tool-out", "b-build-out"), node("d-scan", NodeSBOM, "", "a-tool-out"),
		}, [][]string{{"a-tool"}, {"b-build", "c-sbom"}, {"d-scan"}}},
		{"a job stops at the provider's duration ceiling", []Node{
			node("a-build", NodeBuild, "x"), node("b-step", NodeVerify, "x", "a-build-out"), node("c-step", NodeSBOM, "x", "b-step-out"),
			node("d-step", NodePackage, "", "c-step-out"), node("e-step", NodePackage, "", "d-step-out"),
			node("f-step", NodePackage, "", "e-step-out"), node("g-step", NodePackage, "", "f-step-out"),
		}, [][]string{{"a-build", "b-step", "c-step", "d-step", "e-step", "f-step"}, {"g-step"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := &Plan{}
			for _, member := range test.nodes {
				plan.Graph.Nodes = append(plan.Graph.Nodes, member)
				plan.Graph.Artifacts = append(plan.Graph.Artifacts, Artifact{ID: member.ID + "-out", Producer: member.ID})
			}
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			var got [][]string
			for _, stage := range stages {
				var members []string
				for _, member := range stage.Nodes {
					members = append(members, member.ID)
				}
				got = append(got, members)
			}
			slices.SortFunc(got, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
			if !slices.EqualFunc(got, test.want, func(a, b []string) bool { return slices.Equal(a, b) }) {
				t.Fatalf("jobs = %v, want %v", got, test.want)
			}
		})
	}
}

// Jobs that split one subject keep distinct display names.
func TestGitHubAuthorityStagesNumberRepeatedNames(t *testing.T) {
	plan := &Plan{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: id, Kind: NodeFetch, BuildTarget: "disk", RunsOn: "ubuntu-24.04", Outputs: []string{id + "-out"}})
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, Artifact{ID: id + "-out", Producer: id})
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, stage := range stages {
		names = append(names, stage.Name)
	}
	want := []string{"Fetch pinned sources for disk", "Fetch pinned sources for disk (part 2)"}
	if !slices.Equal(names, want) {
		t.Fatalf("job names = %q, want %q", names, want)
	}
}

// A real signed per-output Plan yields one producer job per Build target and
// one release job per publication route, and they never share authority.
func TestGitHubAuthorityStagesSeparateProducersFromRelease(t *testing.T) {
	plan := referenceTestPlan(t)
	stages, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) >= len(plan.Graph.Nodes)/2 {
		t.Fatalf("%d jobs for %d operations: authority grouping did not contract the graph", len(stages), len(plan.Graph.Nodes))
	}
	routes := map[string][]string{}
	for _, stage := range stages {
		authority := stageAuthorityFor(plan, stage.Nodes[0])
		for _, member := range stage.Nodes {
			if member.RunsOn != stage.Nodes[0].RunsOn || stageAuthorityFor(plan, member) != authority {
				t.Fatalf("job %s mixes runners or authority classes", stage.ID)
			}
			if authority == authorityRelease {
				routes[member.Target] = append(routes[member.Target], member.Kind)
			}
		}
		if rootToolAcquisition(stage.Nodes[0]) && len(stage.Nodes) != 1 {
			t.Fatalf("root tool job %s gained members", stage.ID)
		}
	}
	for _, output := range plan.LifecycleOutputs {
		route := "output-" + output.Name
		if !slices.Equal(routes[route], []string{NodeAttestEvidence, NodePublish, NodeSign, NodeVerifyPublication}) {
			t.Fatalf("route %s release operations = %v", route, routes[route])
		}
		if jobs[NodeAttestEvidence+"/"+route] != jobs[NodeVerifyPublication+"/"+route] {
			t.Fatalf("route %s release operations do not share one job", route)
		}
		if jobs["package/"+outputPublicationPackage(output.Name)] == jobs[NodePublish+"/"+route] {
			t.Fatalf("output %s packaging shares a job with its publication", output.Name)
		}
	}
	if !slices.Equal(routes["release"], []string{NodeAssembleRelease, NodePublish, NodeSign, NodeVerifyPublication}) {
		t.Fatalf("aggregate route release operations = %v", routes["release"])
	}
}

// The rendered job holds the union of its operations' token scopes, while each
// registry credential stays on the step of the operation that uses it.
func TestGitHubReleaseJobScopesCredentialsToSteps(t *testing.T) {
	plan := referenceTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	release := 0
	for _, stage := range stages {
		job := document.Jobs[stage.ID]
		for _, key := range []string{"NVKR_CREDENTIAL", "NVKR_REGISTRY_USER", githubRepositoryTokenEnv} {
			if _, present := job.Env[key]; present {
				t.Fatalf("job %s binds %s at job scope", stage.ID, key)
			}
		}
		if stageAuthorityFor(plan, stage.Nodes[0]) != authorityRelease {
			if job.Environment != "" || job.Permissions["id-token"] != "" || job.Permissions["packages"] != "" {
				t.Fatalf("producer job %s holds release authority: %+v", stage.ID, job)
			}
			continue
		}
		release++
		if job.Environment != githubReleaseEnvironment ||
			!maps.Equal(job.Permissions, map[string]string{"contents": "read", "id-token": "write", "packages": "write"}) {
			t.Fatalf("release job %s scope is not the union of its operations: %+v", stage.ID, job)
		}
		for _, member := range stage.Nodes {
			want := map[string]string{}
			if member.Kind != NodeAttestEvidence {
				want["NVKR_CREDENTIAL"], want["NVKR_REGISTRY_USER"] = "${{ secrets.GITHUB_TOKEN }}", "${{ github.actor }}"
			}
			if member.Kind == NodePublish {
				want[githubRepositoryTokenEnv] = "${{ github.token }}"
			}
			if step := githubNodeStep(t, &job, member.ID); len(step.Env) != len(want) || (len(want) > 0 && !maps.Equal(step.Env, want)) {
				t.Fatalf("%s step credentials = %v, want %v", member.ID, step.Env, want)
			}
		}
	}
	if release != len(plan.LifecycleOutputs)+1 {
		t.Fatalf("%d release jobs for %d outputs and one aggregate route", release, len(plan.LifecycleOutputs))
	}
}

// A release step's exact credential keys select its execution interface. Any
// other set has none, so lowering refuses it.
func TestGitHubReleaseFamilySelectsExactCredentialKeys(t *testing.T) {
	for _, test := range []struct {
		keys []string
		want string
	}{
		{[]string{"NVKR_CREDENTIAL", "NVKR_REGISTRY_USER"}, "execute-registry"},
		{[]string{"NVKR_CREDENTIAL", "NVKR_REGISTRY_USER", githubRepositoryTokenEnv}, "execute-publication"},
		{[]string{githubRepositoryTokenEnv}, "execute-repository"},
		{[]string{"NVKR_CREDENTIAL"}, ""},
		{[]string{"NVKR_CREDENTIAL", "NVKR_REGISTRY_USER", "NVKR_OTHER"}, ""},
		{nil, ""},
	} {
		env := &yamlv3.Node{Kind: yamlv3.MappingNode}
		for _, key := range test.keys {
			githubYAMLSet(env, key, githubYAMLString("x"))
		}
		if got := githubReleaseFamily(env); got != test.want {
			t.Errorf("credential keys %v select %q, want %q", test.keys, got, test.want)
		}
	}
}

// Bundle validation, not only generation, owns the authority boundary.
func TestGitHubValidationRejectsAuthorityTampering(t *testing.T) {
	plan := referenceTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	_, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	route := "output-" + plan.LifecycleOutputs[0].Name
	releaseJob, producerJob := jobs[NodePublish+"/"+route], jobs["package/"+outputPublicationPackage(plan.LifecycleOutputs[0].Name)]
	for _, test := range []struct {
		name, job, want string
		change          func(*githubJob)
	}{
		{"registry credential at job scope", releaseJob, "outside its operation step", func(job *githubJob) {
			job.Env = map[string]string{"NVKR_CREDENTIAL": "${{ secrets.GITHUB_TOKEN }}"}
		}},
		{"token scope wider than the operations", releaseJob, "exact scopes", func(job *githubJob) { job.Permissions["actions"] = "write" }},
		{"identity token dropped from the signer", releaseJob, "exact scopes", func(job *githubJob) { delete(job.Permissions, "id-token") }},
		{"identity token on a producer", producerJob, "without an admitted workload", func(job *githubJob) {
			job.Permissions = map[string]string{"contents": "read", "id-token": "write"}
		}},
		{"release environment on a producer", producerJob, "deployment environment", func(job *githubJob) { job.Environment = githubReleaseEnvironment }},
		{"attestation borrows the publisher credential", releaseJob, "credential scope differs", func(job *githubJob) {
			githubNodeStep(t, job, NodeAttestEvidence+"/"+route).Env = map[string]string{
				"NVKR_CREDENTIAL": "${{ secrets.GITHUB_TOKEN }}", "NVKR_REGISTRY_USER": "${{ github.actor }}"}
		}},
		{"verification loses its credential", releaseJob, "credential scope differs", func(job *githubJob) {
			githubNodeStep(t, job, NodeVerifyPublication+"/"+route).Env = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			job := document.Jobs[test.job]
			test.change(&job)
			document.Jobs[test.job] = job
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateGitHubBundle(t.Context(), plan, unsharedGitHubTestBundle(t, plan, bundle, raw))
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("tampering reached %q instead of the authority check %q", err, test.want)
			}
		})
	}
}

// The Environment permits partitions; the generator engages them only when the
// flat workflow fails provider size admission.
func TestGitHubPartitionsEngageOnlyOnOverflow(t *testing.T) {
	plan := referenceTestPlan(t)
	plan.Environment.GitHubExecutionPartitions = true
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	workers := func(bundle *GeneratedBundle) int {
		count := 0
		for name := range bundle.Files {
			if githubPartitionWorkflowSuffix.MatchString(name) {
				count++
			}
		}
		return count
	}
	flat, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if workers(flat) != 0 {
		t.Fatalf("a workflow inside every provider limit emitted %d partition workers", workers(flat))
	}
	engageGitHubPartitions(t)
	partitioned, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if workers(partitioned) == 0 {
		t.Fatal("an oversized flat workflow did not engage the permitted partition fallback")
	}
	if err := ValidateGitHubBundle(t.Context(), plan, partitioned); err != nil {
		t.Fatal(err)
	}
	// An Environment that does not permit the fallback is never partitioned.
	plan.Environment.GitHubExecutionPartitions = false
	if plan.EnvironmentDigest, err = plan.Environment.Digest(); err != nil {
		t.Fatal(err)
	}
	unpermitted, err := RenderGitHub(t.Context(), plan)
	if err != nil || workers(unpermitted) != 0 {
		t.Fatalf("an Environment without the setting emitted %d workers: %v", workers(unpermitted), err)
	}
}

// Only the two per-file limits engage the fallback. An oversized expression
// fails admission, but splitting the file would not fix it.
func TestGitHubWorkflowOverflowsOnlyOnPerFileLimits(t *testing.T) {
	expression := "run: ${{ '" + strings.Repeat("x", maxGitHubExpressionBytes+1) + "' }}\n"
	for _, test := range []struct {
		name     string
		raw      string
		overflow bool
		admitted bool
	}{
		{"within every limit", "jobs: {}\n", false, true},
		{"file size ceiling", "a: " + strings.Repeat("x", maxGitHubWorkflowBytes) + "\n", true, false},
		{"parser event cap", strings.Repeat("- x\n", maxGitHubWorkflowYAMLNodes), true, false},
		{"alias expansion past the event cap", githubAliasExpansionFixture(), true, false},
		{"oversized expression", expression, false, false},
		{"malformed YAML", "jobs: [\n", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubWorkflowOverflows([]byte(test.raw)); got != test.overflow {
				t.Fatalf("overflow = %t, want %t", got, test.overflow)
			}
			// The production predicate alone drives engagement, and only where
			// the Environment permits the fallback.
			permitted := &Plan{Environment: definition.Environment{GitHubExecutionPartitions: true}}
			if got := githubPartitionsEngaged(permitted, []byte(test.raw)); got != test.overflow {
				t.Fatalf("permitted fallback engaged = %t, want %t", got, test.overflow)
			}
			if githubPartitionsEngaged(&Plan{}, []byte(test.raw)) {
				t.Fatal("an Environment without the setting engaged the fallback")
			}
			if err := validateGitHubWorkflowSize([]byte(test.raw)); (err == nil) != test.admitted {
				t.Fatalf("admission error = %v, want admitted %t", err, test.admitted)
			}
		})
	}
}

// engageGitHubPartitions forces the permitted fallback for the rest of one
// test, so small partition-permitted fixtures exercise it. The force only adds
// engagement; the production limits are never replaced. Plans that do not
// permit partitions are unaffected. The switch is package-wide until cleanup,
// so a caller must not run in parallel with another test that renders.
func engageGitHubPartitions(t testing.TB) {
	t.Helper()
	previous := githubForcePartitionFallback
	githubForcePartitionFallback = true
	t.Cleanup(func() { githubForcePartitionFallback = previous })
}
