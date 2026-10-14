// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"sigs.k8s.io/yaml"
)

type githubInterfaceFixture struct {
	name        string
	nodes       int
	carriers    int
	inputs      bool
	images      bool
	setup       bool
	collectOnly bool
}

func githubActionInterfaceFixture(actions githubActionRefs, fixtures []githubInterfaceFixture) []byte {
	var source bytes.Buffer
	source.WriteString("jobs:\n")
	for _, fixture := range fixtures {
		fmt.Fprintf(&source, "  %s:\n    runs-on: ubuntu-24.04\n    steps:\n", fixture.name)
		actions.writeCheckoutStep(&source)
		actions.writeDownloadStep(&source, "NVKR runner", "nvkr-exec-linux-amd64", ".")
		writePrepareHelperStep(&source)
		if fixture.inputs {
			fmt.Fprintf(&source, "      - name: Download required inputs\n        uses: %s\n        with:\n          pattern: fixture-input\n          path: .nvkr/carriers\n          merge-multiple: 'true'\n", actions.Download)
			writeGitHubShellStep(&source, "Restore required inputs", []string{githubRunnerCommand(&Plan{}, "restore-inputs", "stage", fixture.name)})
		}
		if fixture.setup {
			writeGitHubShellStep(&source, "Configure build environment", []string{"printf setup"})
		}
		if fixture.images {
			fmt.Fprintln(&source, "      - name: Prepare private native toolchains\n        env:")
			for _, key := range sortedKeys(githubImageEnvironment()) {
				fmt.Fprintf(&source, "          %s: %s\n", key, yamlSingle(githubImageEnvironment()[key]))
			}
			fmt.Fprintf(&source, "        run: %s\n", githubRunnerCommand(&Plan{}, "prepare-images", "stage", fixture.name))
		}
		for index := 0; index < fixture.nodes; index++ {
			selector := fmt.Sprintf("%s/node-%d", fixture.name, index)
			if !fixture.collectOnly {
				fmt.Fprintf(&source, "      - name: Execute node %d\n        id: execute-%d\n        run: %s\n", index, index, githubRunnerCommand(&Plan{}, "run-node", "node", selector))
			}
			if fixture.carriers == 0 {
				continue
			}
			writeGitHubShellStep(&source, fmt.Sprintf("Collect node %d", index), []string{githubRunnerCommand(&Plan{}, "pack-outputs", "node", selector)})
			for carrier := 0; carrier < fixture.carriers; carrier++ {
				label := fmt.Sprintf("%s-node-%d-carrier-%d", fixture.name, index, carrier)
				actions.writeUploadStep(&source, &Plan{}, label, label, ".nvkr/carriers/"+label+".tar")
			}
		}
	}
	return source.Bytes()
}

func TestGitHubSemanticInterfacesReuseAcrossNodeAndCarrierCounts(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	fixtures := []githubInterfaceFixture{
		{name: "no-carriers", nodes: 1},
		{name: "one-carrier", nodes: 1, carriers: 1},
		{name: "two-carriers", nodes: 1, carriers: 2},
		{name: "grouped-nodes", nodes: 4, carriers: 2, inputs: true},
		{name: "private-images", nodes: 1, carriers: 2, inputs: true, images: true},
		{name: "authored-setup-boundary", nodes: 2, carriers: 2, inputs: true, images: true, setup: true},
		{name: "additional-carriers", nodes: 3, carriers: 6},
		{name: "collect-group", nodes: 4, carriers: 2, collectOnly: true},
		{name: "collect-additional-carriers", nodes: 3, carriers: 6, collectOnly: true},
	}
	raw := githubActionInterfaceFixture(actions, fixtures)
	workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = shareGitHubActions(&Plan{}, workflow, files, actions)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, workflow)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			operation := "execute-node"
			if fixture.collectOnly {
				operation = "collect-node-outputs"
			}
			var calls []string
			for _, step := range document.Jobs[fixture.name].Steps {
				if step.With["parameters"] != "" {
					calls = append(calls, strings.TrimPrefix(step.Uses, "./"+githubSharedDirectory))
				}
			}
			var want []string
			if fixture.inputs && fixture.setup {
				want = append(want, "prepare-inputs")
			}
			for index := 0; index < fixture.nodes; index++ {
				want = append(want, operation)
				for carrier := 2; carrier < fixture.carriers; carrier++ {
					want = append(want, "upload-carrier")
				}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("operation interfaces changed or reordered with cardinality: got %v, want %v", calls, want)
			}
		})
	}
	for path := range files {
		if !strings.HasPrefix(path, githubSharedDirectory) {
			continue
		}
		family := strings.TrimSuffix(strings.TrimPrefix(path, githubSharedDirectory), "/action.yml")
		switch family {
		case "prepare-stage", "prepare-inputs", "execute-node", "collect-node-outputs", "upload-carrier":
		default:
			t.Fatalf("equivalent operations created a cardinality or preparation variant: %s", family)
		}
	}
	for _, family := range []string{"execute-node", "collect-node-outputs", "upload-carrier"} {
		if len(files[githubSharedDirectory+family+"/action.yml"]) == 0 {
			t.Fatalf("fixture did not exercise %s", family)
		}
	}
	expanded, err := expandGitHubActions(&Plan{}, workflow, files)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeGitHubWorkflow(t, expanded), decodeGitHubWorkflow(t, raw)) {
		t.Fatal("semantic interface lowering changed execution, collection, or carrier order")
	}
}

func githubInterfaceParameters(t *testing.T, data githubActionDataDocument, job string, step githubStep) map[string]string {
	t.Helper()
	binding := step.With["parameters"]
	prefix := "${{ fromJSON(steps." + githubContextStepID + ".outputs.data)['"
	part := strings.TrimSuffix(strings.TrimPrefix(binding, prefix), "'] }}")
	if binding != githubParametersExpression(part) {
		t.Fatalf("job %q uses an unsupported parameter binding", job)
	}
	var parameters map[string]string
	if err := canonical.UnmarshalExact([]byte(data.Entries[job].Values[part]), &parameters); err != nil {
		t.Fatal(err)
	}
	return parameters
}

func TestGitHubExecutionInterfaceHasSemanticOptionalRoles(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	fixtures := []githubInterfaceFixture{
		{name: "no-outputs", nodes: 1},
		{name: "plain", nodes: 1, carriers: 1},
		{name: "inputs", nodes: 1, carriers: 2, inputs: true},
		{name: "images", nodes: 1, carriers: 2, images: true},
		{name: "inputs-and-images", nodes: 1, carriers: 2, inputs: true, images: true},
		{name: "authored-setup-boundary", nodes: 1, carriers: 2, inputs: true, images: true, setup: true},
	}
	workflow, files, err := lowerGitHubActions(&Plan{}, githubActionInterfaceFixture(actions, fixtures), actions)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = shareGitHubActions(&Plan{}, workflow, files, actions)
	if err != nil {
		t.Fatal(err)
	}
	data := sharedGitHubActionData(t, &GeneratedBundle{Files: files})
	document := decodeGitHubWorkflow(t, workflow)
	for _, fixture := range fixtures {
		found, preparations := 0, 0
		for _, step := range document.Jobs[fixture.name].Steps {
			if step.With["parameters"] == "" {
				continue
			}
			parameters := githubInterfaceParameters(t, data, fixture.name, step)
			if step.Uses == "./"+githubSharedDirectory+"prepare-inputs" {
				preparations++
				if !fixture.inputs || !fixture.setup || parameters["download_present"] != "true" || parameters["restore_present"] != "true" || parameters["restore_selector"] != fixture.name {
					t.Fatalf("job %q has no exact shared input preparation", fixture.name)
				}
				continue
			}
			if step.Uses != "./"+githubSharedDirectory+"execute-node" {
				t.Fatalf("job %q has an unexpected operation interface: %s", fixture.name, step.Uses)
			}
			found++
			for role, present := range map[string]bool{
				"download": fixture.inputs && !fixture.setup, "restore": fixture.inputs && !fixture.setup,
				"images": fixture.images, "execute": true, "pack": fixture.carriers > 0,
				"upload": fixture.carriers > 0, "extra_upload": fixture.carriers == 2,
			} {
				if parameters[role+"_present"] != strconv.FormatBool(present) {
					t.Fatalf("job %q role %q presence=%q, want %t", fixture.name, role, parameters[role+"_present"], present)
				}
			}
			wantPack := ""
			if fixture.carriers > 0 {
				wantPack = fixture.name + "/node-0"
			}
			if parameters["execute_selector"] != fixture.name+"/node-0" || parameters["pack_selector"] != wantPack {
				t.Fatalf("job %q has no exact semantic execution/collection selectors", fixture.name)
			}
		}
		wantPreparations := 0
		if fixture.inputs && fixture.setup {
			wantPreparations = 1
		}
		if found != 1 || preparations != wantPreparations {
			t.Fatalf("job %q has %d execution and %d input preparation calls, want 1 and %d", fixture.name, found, preparations, wantPreparations)
		}
	}
	var action struct {
		Runs struct {
			Steps []githubStep `json:"steps"`
		} `json:"runs"`
	}
	if err := yaml.Unmarshal(files[githubSharedDirectory+"execute-node/action.yml"], &action); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, step := range action.Runs.Steps {
		role := strings.TrimPrefix(step.ID, "operation_")
		roles = append(roles, role)
		switch role {
		case "execute":
			if step.If != "" {
				t.Fatalf("required execution acquired an optional condition: %q", step.If)
			}
		case "download", "restore", "images", "pack", "upload", "extra_upload":
			if step.If != "${{ fromJSON(inputs.parameters)."+role+"_present == 'true' }}" {
				t.Fatalf("optional role %q does not use its exact presence condition: %q", role, step.If)
			}
		}
	}
	wantRoles := []string{"download", "restore", "images", "execute", "pack", "upload", "extra_upload"}
	if !reflect.DeepEqual(roles, wantRoles) {
		t.Fatalf("execution interface roles=%v, want %v", roles, wantRoles)
	}
}

func TestGitHubExecutionInterfaceRespectsAuthoredSetupBoundary(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	fixture := githubInterfaceFixture{name: "with-setup", nodes: 1, carriers: 2, inputs: true, images: true, setup: true}
	workflow, files, err := lowerGitHubActions(&Plan{}, githubActionInterfaceFixture(actions, []githubInterfaceFixture{fixture}), actions)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = shareGitHubActions(&Plan{}, workflow, files, actions)
	if err != nil {
		t.Fatal(err)
	}
	job := decodeGitHubWorkflow(t, workflow).Jobs[fixture.name]
	var order []string
	for _, step := range job.Steps {
		if step.Name == "Configure build environment" {
			if step.Uses != "" || strings.TrimSpace(step.Run) != "printf setup" {
				t.Fatal("authored setup crossed the action boundary")
			}
			order = append(order, "setup")
		} else if step.With["parameters"] != "" {
			order = append(order, strings.TrimPrefix(step.Uses, "./"+githubSharedDirectory))
		}
	}
	want := []string{"prepare-inputs", "setup", "execute-node"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("authored setup boundary order=%v, want %v", order, want)
	}
}

func TestGitHubInputAndImagePreparationWithoutFollowingNode(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	fixture := githubInterfaceFixture{name: "preparation-only", inputs: true, images: true}
	raw := githubActionInterfaceFixture(actions, []githubInterfaceFixture{fixture})
	workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = shareGitHubActions(&Plan{}, workflow, files, actions)
	if err != nil {
		t.Fatal(err)
	}
	data := sharedGitHubActionData(t, &GeneratedBundle{Files: files})
	var order []string
	for _, step := range decodeGitHubWorkflow(t, workflow).Jobs[fixture.name].Steps {
		if step.With["parameters"] == "" {
			continue
		}
		family := strings.TrimPrefix(step.Uses, "./"+githubSharedDirectory)
		order = append(order, family)
		parameters := githubInterfaceParameters(t, data, fixture.name, step)
		switch family {
		case "prepare-inputs":
			if parameters["download_present"] != "true" || parameters["restore_present"] != "true" || parameters["restore_selector"] != fixture.name {
				t.Fatal("standalone input preparation lost its download or exact restore selector")
			}
		case "prepare-images":
			if parameters["images_present"] != "true" || parameters["images_selector"] != fixture.name {
				t.Fatal("standalone image preparation lost its exact selector")
			}
		default:
			t.Fatalf("preparation-only job acquired an execution interface: %s", family)
		}
	}
	if want := []string{"prepare-inputs", "prepare-images"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("preparation order=%v, want %v", order, want)
	}
	expanded, err := expandGitHubActions(&Plan{}, workflow, files)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeGitHubWorkflow(t, expanded), decodeGitHubWorkflow(t, raw)) {
		t.Fatal("standalone preparation changed download, restore or credentialed image preparation semantics")
	}
}

func TestGitHubSourceInterfaceOwnsOnlyItsExecutionCredential(t *testing.T) {
	plan, fetch, _ := authenticatedSourcePlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	_, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	wantJob := jobs[fetch.ID]
	workflow := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	sourceCalls := 0
	for id, job := range workflow.Jobs {
		for _, step := range job.Steps {
			credential := step.With["nvkr-source-token"]
			if credential == "" {
				continue
			}
			sourceCalls++
			if id != wantJob || step.Uses != "./"+githubSharedDirectory+"execute-source" || credential != "${{ secrets.NKE_SOURCE_TOKEN }}" || len(step.Env) != 0 {
				t.Fatalf("source credential escaped its explicit execution interface: %s/%s", id, step.Name)
			}
		}
	}
	if sourceCalls != 1 {
		t.Fatalf("source credential reached %d action calls, want one", sourceCalls)
	}
	var action struct {
		Runs struct {
			Steps []githubStep `json:"steps"`
		} `json:"runs"`
	}
	if err := yaml.Unmarshal(bundle.Files[githubSharedDirectory+"execute-source/action.yml"], &action); err != nil {
		t.Fatal(err)
	}
	credentialSteps := 0
	for _, step := range action.Runs.Steps {
		for key, value := range step.Env {
			if key != sourceTokenEnv && !strings.HasPrefix(key, "GIT_") {
				continue
			}
			if step.ID != "operation_execute" {
				t.Fatalf("source environment %q escaped the execution role", key)
			}
			if key == sourceTokenEnv {
				credentialSteps++
				if value != "${{ inputs.nvkr-source-token }}" {
					t.Fatal("source credential was embedded in descriptive parameter data")
				}
			}
		}
	}
	if credentialSteps != 1 {
		t.Fatalf("source credential reached %d inner steps, want one", credentialSteps)
	}
	if bytes.Contains(bundle.Files[githubSharedDirectory+"execute-node/action.yml"], []byte(sourceTokenEnv)) {
		t.Fatal("ordinary execution interface acquired source credentials")
	}
}
