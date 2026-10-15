// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

// The fixture deliberately combines caller-only contexts, an output-referenced
// execution step and all provider controls that cannot move across action scope.
func githubCompositeContractFixture(actions githubActionRefs, container bool) []byte {
	raw := `name: Composite contract
'on':
  workflow_dispatch: {}
permissions:
  contents: read
jobs:
  source:
    name: Fetch and inspect source
    runs-on: ubuntu-24.04
    timeout-minutes: 60
    needs: [bootstrap]
    if: ${{ always() }}
    environment: admitted-environment
    permissions:
      contents: read
    steps:
      - name: Check native runner platform
        run: test "$(uname -s)/$(uname -m)" = Linux/x86_64
      - name: Check out workflow source
        uses: CHECKOUT
        with:
          persist-credentials: 'false'
      - name: Download NVKR runner
        uses: DOWNLOAD
        with:
          name: nvkr-exec-linux-amd64
          path: '.'
      - name: Prepare helper
        run: chmod +x ./nvkr-exec
      - name: Fetch source
        id: fetch-source
        env:
          NVKR_SOURCE_TOKEN: ${{ secrets.SOURCE_TOKEN }}
          GIT_TERMINAL_PROMPT: '0'
          GIT_CONFIG_NOSYSTEM: '1'
          GIT_CONFIG_GLOBAL: /dev/null
          GIT_CONFIG_COUNT: '4'
          GIT_CONFIG_KEY_0: url.https://mirror.example/source.insteadOf
          GIT_CONFIG_VALUE_0: https://source.example/repository
          GIT_CONFIG_KEY_1: credential.helper
          GIT_CONFIG_VALUE_1: ''
          GIT_CONFIG_KEY_2: credential.https://mirror.example/source.helper
          GIT_CONFIG_VALUE_2: SOURCE_HELPER
          GIT_CONFIG_KEY_3: credential.useHttpPath
          GIT_CONFIG_VALUE_3: 'true'
        run: ./nvkr-exec factory github run-node --plan .github/nvkr-run/plan.json --node fetch
      - name: Pack source
        run: ./nvkr-exec factory github pack-outputs --plan .github/nvkr-run/plan.json --node fetch
      - name: Upload source carrier
        uses: UPLOAD
        with:
          name: source-carrier
          path: .nvkr/carriers/source.tar
      - name: Run disk
        id: run-disk
        timeout-minutes: 45
        run: ./nvkr-exec factory github run-disk --plan .github/nvkr-run/plan.json --output disk
      - name: Upload failed disk diagnostics
        if: ${{ failure() && steps.run-disk.outputs.packer_diagnostics != '' }}
        timeout-minutes: 5
        uses: UPLOAD
        with:
          path: ${{ steps.run-disk.outputs.packer_diagnostics }}
      - name: Observe output
        id: observation
        run: printf 'observed=true\n' >> "$GITHUB_OUTPUT"
      - name: Consume output
        run: echo '${{ steps.observation.outputs.observed }}'
      - name: Always upload diagnostic
        if: ${{ always() }}
        uses: UPLOAD
        with:
          path: diagnostic.json
      - name: Tolerate missing diagnostic
        continue-on-error: true
        uses: DOWNLOAD
        with:
          pattern: optional-diagnostic
      - name: Recover planned carriers
        id: recover-artifacts
        env:
          NVKR_JOB_RESULTS: ${{ toJSON(needs) }}
        run: ./nvkr-exec factory github recover --plan .github/nvkr-run/plan.json
  consumer:
    name: Process source
    runs-on: ubuntu-24.04
    timeout-minutes: 60
    needs: [source]
    steps:
      - name: Check native runner platform
        run: test "$(uname -s)/$(uname -m)" = Linux/x86_64
      - name: Check out workflow source
        uses: CHECKOUT
        with:
          persist-credentials: 'false'
      - name: Download NVKR runner
        uses: DOWNLOAD
        with:
          name: nvkr-exec-linux-arm64
          path: '.'
      - name: Prepare helper
        run: chmod +x ./nvkr-exec
      - name: Configure build environment
        run: false | true
      - name: Execute consumer
        run: ./nvkr-exec factory github run-node --plan .github/nvkr-run/plan.json --node consumer
`
	if container {
		raw = strings.ReplaceAll(raw, "    runs-on: ubuntu-24.04\n", "    runs-on: ubuntu-24.04\n    container: ubuntu:24.04\n")
	}
	return []byte(strings.NewReplacer("CHECKOUT", actions.Checkout, "DOWNLOAD", actions.Download, "UPLOAD", actions.Upload, "SOURCE_HELPER", yamlSingle(sourceCredentialHelper)).Replace(raw))
}

func TestGitHubCompositesPreserveExpandedSemanticsAndProviderControls(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	for _, test := range []struct {
		name      string
		container bool
		shell     string
	}{
		{name: "host defaults", shell: "bash -e {0}"},
		{name: "container defaults", container: true, shell: "sh -e {0}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := githubCompositeContractFixture(actions, test.container)
			workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
			if err != nil {
				t.Fatal(err)
			}
			workflow, err = shareGitHubActions(&Plan{}, workflow, files, actions)
			if err != nil {
				t.Fatal(err)
			}
			expanded, err := expandGitHubActions(&Plan{}, workflow, files)
			if err != nil {
				t.Fatal(err)
			}
			want, err := yaml.YAMLToJSONStrict(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := yaml.YAMLToJSONStrict(expanded)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("expanded workflow semantics changed: %v\nwant: %s\ngot: %s", err, want, got)
			}
			second, secondFiles, err := lowerGitHubActions(&Plan{}, raw, actions)
			if err != nil {
				t.Fatal(err)
			}
			second, err = shareGitHubActions(&Plan{}, second, secondFiles, actions)
			if err != nil || !bytes.Equal(workflow, second) || !reflect.DeepEqual(files, secondFiles) {
				t.Fatalf("action lowering is not deterministic: %v", err)
			}
			var entry githubWorkflowDocument
			if err := yaml.UnmarshalStrict(workflow, &entry); err != nil {
				t.Fatal(err)
			}
			steps := entry.Jobs["source"].Steps
			if steps[0].Name != githubPlatformCheckName || steps[1].Uses != actions.Checkout {
				t.Fatal("preflight or checkout moved behind a local action")
			}
			for _, name := range []string{"Run disk", "Upload failed disk diagnostics", "Observe output", "Consume output", "Always upload diagnostic", "Tolerate missing diagnostic"} {
				found := false
				for _, step := range steps {
					found = found || step.Name == name
				}
				if !found {
					t.Errorf("provider-controlled/output-scoped step %q was hidden in an action", name)
				}
			}
			helperFiles, shellSteps := 0, 0
			for name, body := range files {
				if strings.Contains(name, "/prepare-runner") {
					helperFiles++
				}
				if bytes.Contains(body, []byte("secrets.")) || bytes.Contains(body, []byte("toJSON(needs)")) {
					t.Errorf("caller-only context leaked into %s", name)
				}
				if name == GitHubActionDataPath {
					continue
				}
				if strings.Contains(name, "/jobs/") {
					t.Fatalf("job-specific action bypassed semantic sharing: %s", name)
				}
				var action struct {
					Runs struct {
						Steps []struct {
							Run   string `json:"run"`
							Shell string `json:"shell"`
						} `json:"steps"`
					} `json:"runs"`
				}
				if err := yaml.Unmarshal(body, &action); err != nil {
					t.Fatal(err)
				}
				for _, step := range action.Runs.Steps {
					if step.Run != "" {
						shellSteps++
						if step.Shell != test.shell {
							t.Errorf("%s uses shell %q, want original default %q", name, step.Shell, test.shell)
						}
					}
				}
				assertGitHubCompositeReadableYAML(t, name, body, &Plan{})
			}
			if helperFiles != 1 || shellSteps == 0 {
				t.Fatalf("expected one shared helper action and explicit shell steps; got %d and %d", helperFiles, shellSteps)
			}
			assertGitHubCompositeReadableYAML(t, "workflow", workflow, &Plan{})
		})
	}
}

func assertGitHubCompositeReadableYAML(t *testing.T, name string, raw []byte, plan *Plan) {
	t.Helper()
	sharedPrefix := "./" + githubActionsDirectoryFor(plan) + "shared/"
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var visit func(*yamlv3.Node)
	visit = func(node *yamlv3.Node) {
		if node.Kind == yamlv3.MappingNode && len(node.Content) > 0 && node.Style&yamlv3.FlowStyle != 0 {
			t.Errorf("%s contains a nonempty flow mapping", name)
		}
		if node.Anchor != "" || node.Kind == yamlv3.AliasNode {
			anchor, target := node.Anchor, node
			if node.Kind == yamlv3.AliasNode {
				anchor, target = node.Value, node.Alias
			}
			if name == "workflow" && target != nil && target.Kind == yamlv3.SequenceNode && strings.HasPrefix(anchor, "steps-") {
				resolved, err := githubResolveAliases(target, map[*yamlv3.Node]bool{})
				if err != nil || len(resolved.Content) < 4 || len(resolved.Content) > 5 {
					t.Fatalf("invalid complete step sequence: %v", err)
				}
				if githubYAMLValue(resolved.Content[0], "name") != githubPlatformCheckName ||
					!strings.HasPrefix(githubYAMLValue(resolved.Content[1], "uses"), "actions/checkout@") ||
					githubYAMLValue(resolved.Content[2], "id") != githubContextStepID {
					t.Fatal("step sequence does not retain native preflight, checkout and preparation")
				}
				for _, call := range resolved.Content[3:] {
					if githubYAMLValue(call, "uses") != sharedPrefix+"execute-node" || githubYAMLLength(call) != 6 ||
						githubYAMLLength(githubYAMLField(call, "with")) != 2 {
						t.Fatal("step sequence includes a controlled or credential-bearing operation")
					}
				}
				for _, child := range node.Content {
					visit(child)
				}
				return
			}
			if name != "workflow" || target == nil || target.Kind != yamlv3.MappingNode {
				t.Fatalf("%s aliases something other than an approved complete step", name)
			}
			switch anchor {
			case "checkout-workflow-source":
				if !strings.HasPrefix(githubYAMLValue(target, "uses"), "actions/checkout@") || githubYAMLValue(githubYAMLField(target, "with"), "persist-credentials") != "false" {
					t.Fatal("checkout alias does not preserve its pinned credential-free checkout")
				}
			case "check-native-linux-amd64", "check-native-linux-arm64":
				platform := "Linux/x86_64"
				if anchor == "check-native-linux-arm64" {
					platform = "Linux/aarch64"
				}
				if githubYAMLValue(target, "name") != githubPlatformCheckName || !strings.Contains(githubYAMLValue(target, "run"), platform) {
					t.Fatal("native preflight alias names a different platform or operation")
				}
			default:
				if strings.HasPrefix(anchor, "operation-") {
					uses := githubYAMLValue(target, "uses")
					prefix := sharedPrefix
					family := strings.TrimPrefix(uses, prefix)
					binding := githubYAMLValue(githubYAMLField(target, "with"), "parameters")
					selectorPrefix := "${{ fromJSON(steps." + githubContextStepID + ".outputs.data)['"
					part := strings.TrimSuffix(strings.TrimPrefix(binding, selectorPrefix), "'] }}")
					if !strings.HasPrefix(uses, prefix) || binding != githubParametersExpression(part) ||
						!githubActionPart(part) || anchor != "operation-"+family+"-"+part ||
						githubYAMLValue(target, "name") != strings.ReplaceAll(family, "-", " ")+" "+part {
						t.Fatal("operation alias changes its semantic interface or current-job parameter selector")
					}
					break
				}
				if !strings.HasPrefix(anchor, "prepare-stage-") {
					t.Errorf("%s contains non-semantic or unapproved alias %q", name, anchor)
					break
				}
				with := githubYAMLField(target, "with")
				uses, artifact := githubYAMLValue(target, "uses"), githubYAMLValue(with, "runner-artifact")
				wantAnchor := "prepare-stage-" + shortStableName(artifact)
				if uses == sharedPrefix+"prepare-stage-container" {
					wantAnchor += "-container"
				} else if uses != sharedPrefix+"prepare-stage" {
					t.Fatal("preparation alias names a different action")
				}
				if anchor != wantAnchor || len(target.Content) != 8 || githubYAMLLength(with) != 4 ||
					githubYAMLValue(target, "name") != "Prepare NVKR runner and job parameters" ||
					githubYAMLValue(target, "id") != githubContextStepID || githubYAMLValue(with, "action") != "${{ github.job }}" ||
					!strings.HasPrefix(artifact, "nvkr-exec-") {
					t.Fatal("preparation alias changes its fixed helper delivery or current-job binding")
				}
			}
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&document)
}

func TestGitHubCompositesKeepCallerInputsOnExactInnerSteps(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	workflow, files, err := lowerGitHubActions(&Plan{}, githubCompositeContractFixture(actions, false), actions)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = shareGitHubActions(&Plan{}, workflow, files, actions)
	if err != nil {
		t.Fatal(err)
	}
	var entry githubWorkflowDocument
	if err := yaml.UnmarshalStrict(workflow, &entry); err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{}
	for _, step := range entry.Jobs["source"].Steps {
		if !strings.HasPrefix(step.Uses, "./") {
			continue
		}
		if len(step.Env) != 0 {
			t.Fatal("composite call has a broad credential environment")
		}
		for name, value := range step.With {
			switch name {
			case "parameters", "action", "runner-artifact":
				continue
			case "with-path":
				if value == "" {
					continue
				}
			}
			if prior, exists := inputs[name]; exists && prior != value {
				t.Fatalf("caller input %q changed binding between operations", name)
			}
			inputs[name] = value
		}
	}
	want := map[string]string{"nvkr-source-token": "${{ secrets.SOURCE_TOKEN }}", "nvkr-job-results": "${{ toJSON(needs) }}",
		"with-path": "${{ steps.run-disk.outputs.packer_diagnostics }}"}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("caller bindings %v, want %v", inputs, want)
	}
	for _, expected := range []githubStep{
		{Name: "Upload failed disk diagnostics", Uses: actions.Upload, If: "${{ failure() && steps.run-disk.outputs.packer_diagnostics != '' }}", TimeoutMinutes: 5},
		{Name: "Always upload diagnostic", Uses: actions.Upload, If: "${{ always() }}"},
		{Name: "Tolerate missing diagnostic", Uses: actions.Download, ContinueOnError: true},
	} {
		found := false
		for _, call := range entry.Jobs["source"].Steps {
			if call.Name != expected.Name {
				continue
			}
			found = true
			if !strings.HasPrefix(call.Uses, "./"+githubActionsDirectory) || call.If != expected.If || call.TimeoutMinutes != expected.TimeoutMinutes || call.ContinueOnError != expected.ContinueOnError {
				t.Fatalf("controlled transfer %q changed its caller condition, timeout, or failure handling: %+v", call.Name, call)
			}
			raw := files[strings.TrimPrefix(call.Uses, "./")+"/action.yml"]
			var action struct {
				Runs struct {
					Steps []githubStep `json:"steps"`
				} `json:"runs"`
			}
			if err := yaml.Unmarshal(raw, &action); err != nil {
				t.Fatal(err)
			}
			if len(action.Runs.Steps) != 1 {
				t.Fatalf("controlled transfer %q must wrap exactly one transport", call.Name)
			}
			inner := action.Runs.Steps[0]
			wantInnerCondition := ""
			if expected.If == "${{ always() }}" {
				wantInnerCondition = expected.If
			}
			if inner.Uses != expected.Uses || inner.If != wantInnerCondition || inner.TimeoutMinutes != 0 || inner.ContinueOnError || bytes.Contains(raw, []byte("steps.")) {
				t.Fatalf("controlled transfer %q changed its inner cancellation guard or moved caller controls or step-output context into its action", call.Name)
			}
			if call.Name == "Upload failed disk diagnostics" &&
				(call.With["with-path"] != want["with-path"] || inner.With["path"] != "${{ inputs.with-path || fromJSON(inputs.parameters).upload_with_path }}") {
				t.Fatal("diagnostic path did not cross the action boundary as an explicit caller input")
			}
			if wantInnerCondition != "" {
				var changedAction yamlv3.Node
				if err := yamlv3.Unmarshal(raw, &changedAction); err != nil {
					t.Fatal(err)
				}
				steps := githubYAMLField(githubYAMLField(changedAction.Content[0], "runs"), "steps")
				githubYAMLRemove(steps.Content[0], "if")
				changedRaw, err := encodeGitHubYAML(&changedAction)
				if err != nil {
					t.Fatal(err)
				}
				changedFiles := maps.Clone(files)
				changedFiles[strings.TrimPrefix(call.Uses, "./")+"/action.yml"] = changedRaw
				_, err = expandGitHubActions(&Plan{}, workflow, changedFiles)
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			}
		}
		if !found {
			t.Fatalf("controlled transfer %q is missing from the caller", expected.Name)
		}
	}
	foundSource, foundRecovery := false, false
	for name, raw := range files {
		if name == GitHubActionDataPath {
			continue
		}
		var action struct {
			Runs struct {
				Steps []githubStep `json:"steps"`
			} `json:"runs"`
		}
		if err := yaml.Unmarshal(raw, &action); err != nil {
			t.Fatal(err)
		}
		for _, step := range action.Runs.Steps {
			if token := step.Env["NVKR_SOURCE_TOKEN"]; token != "" {
				if name != githubSharedDirectory+"execute-source/action.yml" || step.ID != "operation_execute" || token != "${{ inputs.nvkr-source-token }}" {
					t.Fatalf("source credential moved to %s / %s", name, step.Name)
				}
				foundSource = true
			}
			if results := step.Env["NVKR_JOB_RESULTS"]; results != "" {
				if name != githubSharedDirectory+"recover-inputs/action.yml" || step.ID != "operation_recover" || results != "${{ inputs.nvkr-job-results }}" {
					t.Fatalf("provider results moved to %s / %s", name, step.Name)
				}
				foundRecovery = true
			}
		}
	}
	if !foundSource || !foundRecovery {
		t.Fatal("source credential or provider-result binding is absent")
	}
}

func TestGitHubCompositesKeepReferencedTransferOutputsInCaller(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	for _, test := range []struct{ name, uses, output string }{
		{"upload artifact ID", actions.Upload, "artifact-id"},
		{"download path", actions.Download, "download-path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(strings.NewReplacer("CHECKOUT", actions.Checkout, "TRANSFER", test.uses, "OUTPUT", test.output).Replace(`jobs:
  transfer:
    runs-on: ubuntu-24.04
    steps:
      - name: Check out workflow source
        uses: CHECKOUT
      - name: Transfer artifact
        id: transfer
        if: ${{ always() }}
        timeout-minutes: 5
        continue-on-error: true
        uses: TRANSFER
        with:
          name: fixture-artifact
          path: '.'
      - name: Consume transfer output
        run: printf '%s\n' '${{ steps.transfer.outputs.OUTPUT }}'
`))
			workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
			if err != nil {
				t.Fatal(err)
			}
			before := decodeGitHubWorkflow(t, raw).Jobs["transfer"].Steps
			after := decodeGitHubWorkflow(t, workflow).Jobs["transfer"].Steps
			if len(after) != len(before) || !reflect.DeepEqual(after[1], before[1]) || !reflect.DeepEqual(after[2], before[2]) {
				t.Fatal("referenced transfer output or consumer moved out of the caller step scope")
			}
			if after[1].Uses != test.uses || after[1].ID != "transfer" {
				t.Fatal("transfer output producer was hidden behind a composite without declared outputs")
			}
			expanded, err := expandGitHubActions(&Plan{}, workflow, files)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeGitHubWorkflow(t, expanded), decodeGitHubWorkflow(t, raw)) {
				t.Fatal("transfer output scope changed during action expansion")
			}
		})
	}
}

func TestGitHubCompositesRejectConflictingCredentialInputs(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	raw := githubCompositeContractFixture(actions, false)
	raw = bytes.Replace(raw, []byte("      - name: Pack source\n"), []byte("      - name: Pack source\n        env:\n          NVKR_SOURCE_TOKEN: ${{ secrets.OTHER_SOURCE_TOKEN }}\n"), 1)
	_, _, err := lowerGitHubActions(&Plan{}, raw, actions)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestGitHubCompositesKeepAdmittedOperatorSetupInCaller(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup string
	}{
		{name: "private registry secret", setup: "printf '%s' '${{ secrets.REGISTRY_PASSWORD }}' | docker login registry.example --username user --password-stdin"},
		{name: "provider dependency context", setup: "printf '%s\\n' '${{ toJSON(needs) }}'"},
		{name: "original action path environment", setup: "printf '%s\\n' \"${GITHUB_ACTION_PATH:-}\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := buildOnlyPlan(t, "ubuntu-24.04")
			plan.Environment.Runtime.Setup = []string{test.setup}
			digest, err := plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			plan.EnvironmentDigest = digest
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			var workflow githubWorkflowDocument
			if err := yaml.UnmarshalStrict(bundle.Files[GitHubWorkflowPath], &workflow); err != nil {
				t.Fatal(err)
			}
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range stages {
				found := 0
				for _, step := range workflow.Jobs[stage.ID].Steps {
					if step.Name == "Configure build environment" {
						found++
						if strings.TrimSuffix(step.Run, "\n") != test.setup || step.Uses != "" || len(step.Env) != 0 {
							t.Fatalf("job %s changed admitted operator setup or its caller scope", stage.ID)
						}
					}
				}
				if found != 1 {
					t.Fatalf("job %s has %d inline setup steps, want one", stage.ID, found)
				}
			}
			for name, raw := range bundle.Files {
				if strings.HasPrefix(name, githubActionsDirectory) && bytes.Contains(raw, []byte("Configure build environment")) {
					t.Fatalf("operator setup moved into %s", name)
				}
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitHubCompositesRejectChangedBundleMembers(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	actionPath := githubActionsDirectory + "prepare-runner/action.yml"
	if len(bundle.Files[actionPath]) == 0 {
		t.Fatal("fixture has no shared helper action")
	}
	for _, test := range []struct {
		name   string
		code   e.Code
		mutate func(*GeneratedBundle)
	}{
		{name: "missing action", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) { delete(changed.Files, actionPath) }},
		{name: "extra action", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			changed.Files[githubActionsDirectory+"unplanned/action.yml"] = changed.Files[actionPath]
		}},
		{name: "renamed action", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			changed.Files[githubActionsDirectory+"renamed/action.yml"] = changed.Files[actionPath]
			delete(changed.Files, actionPath)
		}},
		{name: "description changed", code: e.CodeEngineCapability, mutate: func(changed *GeneratedBundle) {
			changed.Files[actionPath] = bytes.Replace(changed.Files[actionPath], []byte("Generated NVKR steps"), []byte("Changed NVKR steps"), 1)
		}},
		{name: "helper command changed", code: e.CodeEngineCapability, mutate: func(changed *GeneratedBundle) {
			changed.Files[actionPath] = bytes.Replace(changed.Files[actionPath], []byte("chmod +x ./nvkr-exec"), []byte("chmod +x ./nvkr-exec && printf injected"), 1)
		}},
		{name: "shell changed", code: e.CodeEngineCapability, mutate: func(changed *GeneratedBundle) {
			changed.Files[actionPath] = bytes.Replace(changed.Files[actionPath], []byte("bash -e {0}"), []byte("bash {0}"), 1)
		}},
		{name: "empty action", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) { changed.Files[actionPath] = nil }},
		{name: "comment-only action", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			changed.Files[actionPath] = []byte("# No action body\n")
		}},
		{name: "empty workflow", code: e.CodeInvalidCatalog, mutate: func(changed *GeneratedBundle) {
			changed.Files[GitHubWorkflowPath] = nil
			changed.WorkflowDigest = pipeline.FileDigest(nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *bundle
			changed.Files = maps.Clone(bundle.Files)
			test.mutate(&changed)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), test.code)
		})
	}
}

func TestGitHubCompositesManifestOwnershipAndRetirement(t *testing.T) {
	bundle, err := RenderGitHub(t.Context(), githubTestPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := ManifestFor(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range bundle.Files {
		if !strings.HasPrefix(name, githubActionsDirectory) {
			continue
		}
		found := false
		for _, member := range manifest.Members {
			if member.Path == name {
				found = member.Mode == 0o644 && member.SHA256 == pipeline.FileDigest(raw)
			}
		}
		if !found {
			t.Fatalf("action %s is missing its exact manifest digest/mode", name)
		}
	}
	for _, test := range []struct {
		name    string
		changed bool
	}{
		{name: "retire unchanged action"},
		{name: "preserve edited action", changed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			obsolete := githubActionsDirectory + "obsolete/action.yml"
			prior := *bundle
			prior.Files = maps.Clone(bundle.Files)
			prior.Files[obsolete] = []byte("name: obsolete generated action\n")
			if err := WriteGeneratedBundle(t.Context(), root, &prior); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedBundle(t.Context(), root, &prior); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, obsolete)
			if test.changed {
				if err := os.WriteFile(path, []byte("operator edit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, &prior), e.CodePipelineDrift)
				assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
				retained, err := os.ReadFile(path)
				if err != nil || string(retained) != "operator edit\n" {
					t.Fatalf("modified obsolete action was not preserved: %v", err)
				}
				return
			}
			if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("obsolete action directory was not retired: %v", err)
			}
			if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
		})
	}
}
