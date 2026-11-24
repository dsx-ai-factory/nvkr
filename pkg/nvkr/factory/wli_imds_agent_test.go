// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const wliDefinitionPath = "inputs/definitions/nvidia-workload-identity/imds-agent.yaml"
const wliEnvironmentPath = "inputs/environments/github-wli-imds-agent.yaml"

func wliAgentPlan(t *testing.T) *Plan {
	t.Helper()
	t.Chdir("../../..")
	doc, err := definition.LoadDefinition(t.Context(), wliDefinitionPath)
	if err != nil {
		t.Fatal(err)
	}
	env, err := definition.LoadEnvironment(t.Context(), wliEnvironmentPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := CompileDocuments(t.Context(), wliDefinitionPath, doc, wliEnvironmentPath, env, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	return plan
}

// The independent selection compiles exactly two single-platform executable outputs
// from one pinned root-module source. Compilation needs no source access and no
// execution; a Plan remains static-unqualified work description.
func TestWLIIMDSAgentPlanOutputs(t *testing.T) {
	plan := wliAgentPlan(t)
	if len(plan.LifecycleOutputs) != 2 {
		t.Fatalf("plan carries %d outputs, want the two agent executables", len(plan.LifecycleOutputs))
	}
	wantNames := []string{"dsx-imds-node-attestor-agent-linux-amd64", "dsx-imds-node-attestor-agent-linux-arm64"}
	nodes, artifacts := planIndex(plan)
	for _, output := range plan.LifecycleOutputs {
		payload := artifacts["artifact/"+output.Name+"/dsx-imds-node-attestor-agent"]
		if payload.Kind != "host-package" || payload.MediaType != delivery.MediaOctetStream || nodes[payload.Producer].Kind != NodeBuild {
			t.Fatalf("output %q raw executable metadata: %+v", output.Name, payload)
		}
		if nodes[output.ArtifactID].PackageKind != PackageBinaryBundle || artifacts[output.ArtifactID].MediaType != "application/vnd.nvkr.binary-bundle.v1" {
			t.Fatalf("output %q changed its binary-bundle packaging", output.Name)
		}
		if !slices.Contains(wantNames, output.Name) {
			t.Fatalf("unexpected output %q", output.Name)
		}
		if len(output.Components) != 1 || output.Components[0] != "dsx-imds-node-attestor-agent" {
			t.Fatalf("output %q selects %v, want exactly the agent producer", output.Name, output.Components)
		}
	}
	amd64, arm64 := plan.LifecycleOutputs[0], plan.LifecycleOutputs[1]
	if amd64.BuildDigest == arm64.BuildDigest {
		t.Fatal("both architectures share one build identity; each output must bind its own")
	}
	if len(plan.Builds) != 2 {
		t.Fatalf("plan compiles %d builds, want one per architecture", len(plan.Builds))
	}
	if len(plan.InternalTools) != 0 {
		t.Fatal("executable selection acquired an internal tool producer")
	}
	lock, err := catalog.LoadLockContext(t.Context(), "inputs/catalogs/nvidia-workload-identity")
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := lock.Pins["nvidia-workload-identity"]
	if !ok {
		t.Fatal("agent source pin is missing")
	}
	for _, build := range plan.Builds {
		if len(build.Components) != 1 || build.Components[0] != "dsx-imds-node-attestor-agent" {
			t.Fatalf("build %q selects %v", build.Name, build.Components)
		}
		for _, node := range build.Graph.Nodes {
			switch node.Kind {
			case ir.KindFetch:
				if node.Commit != pin.Commit || node.TreeHash != pin.TreeHash {
					t.Fatalf("build %q fetch lost the exact v1.15.0 source identity", build.Name)
				}
			case ir.KindBuild:
				command := strings.Join(node.Command, "\n")
				if len(node.Command) == 0 || node.OutputType != "host-package" || !strings.Contains(command, "./cmd/dsx-imds-node-attestor-agent") || strings.Contains(command, "attestor-server") {
					t.Fatalf("build %q lost its agent host-package contract", build.Name)
				}
			case ir.KindVerify:
			default:
				t.Fatalf("build %q emitted unexpected %q node for a plain executable selection", build.Name, node.Kind)
			}
		}
	}
}

// The generated bundle is the independent named production workflow: isolated
// record paths, manual dispatch only, generic hosted runners, and no publication,
// signing or qualification lane.
func TestWLIIMDSAgentGeneratedBundle(t *testing.T) {
	plan := wliAgentPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.WorkflowPath != ".github/workflows/nvkr-run-wli-imds-agent.yml" ||
		bundle.PlanPath != ".github/nvkr-run-wli-imds-agent/plan.json" ||
		bundle.ManifestPath != ".github/nvkr-run-wli-imds-agent/manifest.json" {
		t.Fatalf("bundle escaped its isolated namespace: %s / %s / %s", bundle.WorkflowPath, bundle.PlanPath, bundle.ManifestPath)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	if _, manual := document.On["workflow_dispatch"]; !manual || len(document.On) != 1 {
		t.Fatalf("workflow triggers %v, want manual dispatch only", keysOf(document.On))
	}
	if document.Name != "nvkr-project-wli-imds-agent" {
		t.Fatalf("workflow name %q", document.Name)
	}
	for name, job := range document.Jobs {
		if strings.Contains(strings.ToLower(name), "publish") || strings.Contains(strings.ToLower(name), "sign") {
			t.Fatalf("build-only bundle carries job %q", name)
		}
		if job.RunsOn != "ubuntu-24.04" && job.RunsOn != "ubuntu-24.04-arm" {
			t.Fatalf("job %q runs on %q, want a generic hosted runner", name, job.RunsOn)
		}
		if job.Environment != "" {
			t.Fatalf("job %q selects protected environment %q", name, job.Environment)
		}
	}
	for path, raw := range bundle.Files {
		text := string(raw)
		for _, credential := range []string{"NVKR_REGISTRY_WRITE", "NVKR_REGISTRY_READ", "cosign"} {
			if strings.Contains(text, credential) {
				t.Fatalf("bundle member %s references publication or signing material %q", path, credential)
			}
		}
	}
}

// The compiled plan delegates the hosted source fetch to the pipeline executor
// with the held SourceWorkspace and the source owner's exact vendor declaration.
// This is delegation evidence only: it substitutes the pipeline callback and does
// not prove Git acquisition, receipts, archive handoff or materialization. The
// pipeline package's WLIIMDS transport tests cover that boundary directly.
func TestWLIIMDSAgentSourceFetchDelegatesHeldWorkspace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issued held source workspace requires Linux")
	}
	plan := wliAgentPlan(t)
	lock, err := catalog.LoadLockContext(t.Context(), "inputs/catalogs/nvidia-workload-identity")
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := lock.Pins["nvidia-workload-identity"]
	if !ok {
		t.Fatal("agent source pin is missing")
	}
	var fetch *Node
	for i := range plan.Graph.Nodes {
		if plan.Graph.Nodes[i].Kind == NodeFetch {
			fetch = &plan.Graph.Nodes[i]
			break
		}
	}
	if fetch == nil {
		t.Fatal("plan has no source fetch node")
	}
	t.Chdir(t.TempDir())
	called := false
	err = executeSourceFetch(t.Context(), plan, fetch.ID, nil, nil, nil,
		func(instruction ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called = true
			if options.SourceWorkspace == nil || !options.Launch {
				t.Fatal("held source workspace was not issued to the fetch operation")
			}
			if instruction.Repo != "https://github.com/dsx-ai-factory/nvidia-workload-identity" ||
				instruction.Commit != pin.Commit {
				t.Fatalf("fetch instruction lost the pinned root source: %+v", instruction)
			}
			vendor := strings.Join(instruction.VendorCmd, "\n")
			for _, required := range []string{"go mod download", "go mod verify", "go mod vendor", `test "$before" = "$after"`} {
				if !strings.Contains(vendor, required) {
					t.Fatalf("held vendor route lost %q", required)
				}
			}
			return nil, nil
		})
	if !called || err != nil {
		t.Fatalf("held source fetch delegated=%t error=%v", called, err)
	}
}

// Incomplete or mismatched declarations fail with coded errors before any
// generated output, build or source work.
func TestWLIIMDSAgentRejectsIncompleteDeclarations(t *testing.T) {
	t.Chdir("../../..")
	doc, err := definition.LoadDefinition(t.Context(), wliDefinitionPath)
	if err != nil {
		t.Fatal(err)
	}
	env, err := definition.LoadEnvironment(t.Context(), wliEnvironmentPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("undeclared output platform", func(t *testing.T) {
		mutated := doc
		mutated.Outputs = append([]definition.DefinitionOutput(nil), doc.Outputs...)
		mutated.Outputs[0].Platform = "linux/riscv64"
		_, err := CompileDocuments(t.Context(), wliDefinitionPath, mutated, wliEnvironmentPath, env, "dev (unknown)")
		if err == nil || factoryErrorCode(err) != e.CodeInvalidCatalog {
			t.Fatalf("undeclared platform compiled: %v", err)
		}
	})
	t.Run("undeclared toolchain access", func(t *testing.T) {
		mutatedEnv := env
		access := *mutatedEnv.ToolchainAccess
		access.PublicRoots = nil
		mutatedEnv.ToolchainAccess = &access
		plan, err := CompileDocuments(t.Context(), wliDefinitionPath, doc, wliEnvironmentPath, mutatedEnv, "dev (unknown)")
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := RenderGitHub(t.Context(), plan)
		if bundle != nil || factoryErrorCode(err) != e.CodeEngineCapability {
			t.Fatalf("missing toolchain access rendered: %v, %v", bundle, err)
		}
	})
}

func keysOf(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// Historical Plans retain their own bytes and identity. Compatibility admits
// only either complete raw-executable encoding, never per-artifact exceptions.
func TestWLIIMDSAgentCompiledGraphMediaCompatibility(t *testing.T) {
	plan := wliAgentPlan(t)
	var raw []int
	for i, artifact := range plan.Graph.Artifacts {
		if artifact.Kind == "host-package" && artifact.MediaType == delivery.MediaOctetStream {
			raw = append(raw, i)
		}
	}
	if len(raw) < 2 {
		t.Fatal("fixture needs multiple raw executable artifacts")
	}
	legacy := artifactMediaType("host-package", "")
	for _, tc := range []struct {
		name      string
		media     []string
		unrelated bool
		want      e.Code
	}{
		{name: "current", media: []string{delivery.MediaOctetStream, delivery.MediaOctetStream}},
		{name: "legacy", media: []string{legacy, legacy}},
		{name: "mixed legacy first", media: []string{legacy, delivery.MediaOctetStream}, want: e.CodePipelineDrift},
		{name: "mixed current first", media: []string{delivery.MediaOctetStream, legacy}, want: e.CodePipelineDrift},
		{name: "unknown", media: []string{"application/x-unrelated", "application/x-unrelated"}, want: e.CodePipelineDrift},
		{name: "legacy unrelated edit", media: []string{legacy, legacy}, unrelated: true, want: e.CodePipelineDrift},
		{name: "current unrelated edit", media: []string{delivery.MediaOctetStream, delivery.MediaOctetStream}, unrelated: true, want: e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *plan
			candidate.Graph.Artifacts = slices.Clone(plan.Graph.Artifacts)
			for i, index := range raw {
				candidate.Graph.Artifacts[index].MediaType = tc.media[i%len(tc.media)]
			}
			if tc.unrelated {
				candidate.Graph.Artifacts[raw[0]].Name += "-unrelated"
			}
			before, err := canonical.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			err = candidate.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, tc.want)
			}
			after, err := canonical.Marshal(candidate)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("validation rewrote frozen Plan bytes")
			}
		})
	}
}
