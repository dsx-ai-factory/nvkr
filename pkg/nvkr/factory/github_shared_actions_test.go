// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func sharedGitHubActionData(t *testing.T, bundle *GeneratedBundle) githubActionDataDocument {
	t.Helper()
	var data githubActionDataDocument
	dataPath := GitHubActionDataPath
	if bundle.PlanPath != "" {
		dataPath = filepath.ToSlash(filepath.Join(filepath.Dir(bundle.PlanPath), "action-data.json"))
	}
	if err := canonical.UnmarshalExact(bundle.Files[dataPath], &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Entries) == 0 {
		t.Fatal("fixture has no shared action data")
	}
	return data
}

func TestGitHubSharedActionCountDoesNotGrowWithEquivalentJobs(t *testing.T) {
	plan := githubTestPlan(t)
	actions := testActionRefs(t, plan)
	var firstTemplates map[string][]byte
	var executionTemplate string
	for _, count := range []int{1, 3, 8} {
		t.Run(fmt.Sprintf("%d jobs", count), func(t *testing.T) {
			var source bytes.Buffer
			source.WriteString("jobs:\n")
			for index := 0; index < count; index++ {
				fmt.Fprintf(&source, "  operation-%d:\n    runs-on: ubuntu-24.04\n    steps:\n", index)
				fmt.Fprintf(&source, "      - name: Check out workflow source\n        uses: %s\n        with:\n          persist-credentials: 'false'\n", actions.Checkout)
				fmt.Fprintf(&source, "      - name: Download NVKR runner\n        uses: %s\n        with:\n          name: nvkr-exec-linux-amd64\n          path: '.'\n", actions.Download)
				fmt.Fprintf(&source, "      - name: %s\n        run: %s\n", prepareHelperStepName, prepareHelperRun)
				selector := fmt.Sprintf("producer-node-%d", index)
				fmt.Fprintf(&source, "      - name: Execute producer %d\n        id: producer-%d\n        run: %s\n", index, index, githubRunnerCommand(plan, "run-node", "node", selector))
				fmt.Fprintf(&source, "      - name: Collect producer %d\n        run: %s\n", index, githubRunnerCommand(plan, "pack-outputs", "node", selector))
				actions.writeUploadStep(&source, plan, fmt.Sprintf("producer %d", index), fmt.Sprintf("producer-carrier-%d", index), fmt.Sprintf("producer-%d.tar", index))
			}
			workflow, files, err := lowerGitHubActions(plan, source.Bytes(), actions)
			if err != nil {
				t.Fatal(err)
			}
			workflow, err = shareGitHubActions(plan, workflow, files, actions)
			if err != nil {
				t.Fatal(err)
			}
			templates := maps.Clone(files)
			delete(templates, GitHubActionDataPath)
			if firstTemplates == nil {
				firstTemplates = templates
			} else if !reflect.DeepEqual(templates, firstTemplates) {
				t.Fatal("adding equivalent jobs changed the reusable action set or its implementations")
			}
			data := sharedGitHubActionData(t, &GeneratedBundle{Files: files})
			if len(data.Entries) != count {
				t.Fatalf("got %d parameter entries for %d jobs", len(data.Entries), count)
			}
			entry := decodeGitHubWorkflow(t, workflow)
			calls := 0
			for jobID, job := range entry.Jobs {
				prepared, invoked := false, false
				for _, step := range job.Steps {
					if step.ID == githubContextStepID {
						prepared = step.Uses == "./"+githubSharedDirectory+"prepare-stage" && step.With["action"] == "${{ github.job }}"
						continue
					}
					if parameters := step.With["parameters"]; parameters != "" {
						if !prepared || parameters != githubParametersExpression("part-1") {
							t.Fatalf("job %q does not consume its own prepared parameter entry", jobID)
						}
						if executionTemplate == "" {
							executionTemplate = step.Uses
						}
						if step.Uses != executionTemplate || step.Uses != "./"+githubSharedDirectory+"execute-node" {
							t.Fatalf("equivalent job %q uses a different implementation: %s", jobID, step.Uses)
						}
						invoked = true
						calls++
					}
				}
				if !prepared || !invoked {
					t.Fatalf("job %q omits its shared preparation or execution", jobID)
				}
			}
			if calls != count {
				t.Fatalf("one shared implementation has %d calls for %d jobs", calls, count)
			}
			for name, raw := range templates {
				assertGitHubCompositeReadableYAML(t, name, raw)
				if strings.Contains(name, "/jobs/") || bytes.Contains(raw, []byte("producer-node-")) {
					t.Fatalf("job-specific implementation remains in %s", name)
				}
			}
			assertGitHubCompositeReadableYAML(t, "workflow", workflow)
			expanded, err := expandGitHubActions(plan, workflow, files)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeGitHubWorkflow(t, expanded), decodeGitHubWorkflow(t, source.Bytes())) {
				t.Fatal("shared templates changed the jobs' concrete execution semantics")
			}
		})
	}
}

func TestGitHubSharedActionParametersContainDataWithoutPrograms(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) *Plan
	}{
		{name: "public source", fixture: githubTestPlan},
		{name: "authenticated source", fixture: func(t *testing.T) *Plan {
			plan, _, _ := authenticatedSourcePlan(t)
			return plan
		}},
		{name: "strict ARC inspection", fixture: func(t *testing.T) *Plan { return arcTestPlan(t, "linux/amd64") }},
		{name: "qualification handoff", fixture: referenceTestPlan},
		{name: "development", fixture: githubDevelopmentTestPlan},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertGitHubSharedActionParameterData(t, test.fixture(t))
		})
	}
}

func assertGitHubSharedActionParameterData(t *testing.T, plan *Plan) {
	t.Helper()
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	data := sharedGitHubActionData(t, bundle)
	selectors := 0
	for job, entry := range data.Entries {
		if entry.Template != "stage-context" || len(entry.Values) == 0 {
			t.Fatalf("job %q has no fixed stage context", job)
		}
		for part, encoded := range entry.Values {
			var parameters map[string]string
			if err := canonical.UnmarshalExact([]byte(encoded), &parameters); err != nil {
				t.Fatal(err)
			}
			for key, value := range parameters {
				role, _, _ := strings.Cut(key, "_")
				if strings.HasPrefix(role, "s") {
					if _, err := strconv.Atoi(strings.TrimPrefix(role, "s")); err == nil {
						t.Fatalf("job %s part %s parameter %s uses a positional step interface", job, part, key)
					}
				}
				if strings.HasSuffix(key, "_run") || strings.HasSuffix(key, "_script") || strings.HasSuffix(key, "_command") ||
					strings.Contains(value, "./nvkr-exec ") || strings.Contains(value, "${{") || strings.HasPrefix(value, "!") {
					t.Fatalf("job %s part %s parameter %s contains a program or provider expression", job, part, key)
				}
				if strings.HasSuffix(key, "_selector") {
					selectors++
				}
			}
		}
	}
	if selectors == 0 {
		t.Fatal("fixture does not exercise operation selector parameters")
	}
	selectorPrograms := 0
	for name, raw := range bundle.Files {
		if !strings.HasPrefix(name, githubActionsDirectoryFor(plan)+"shared/") {
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
			program := strings.NewReplacer("${{ github.run_id }}", "", "${{ github.run_attempt }}", "").Replace(step.Run)
			if strings.Contains(program, "${{") || strings.Contains(step.Run, "eval ") {
				t.Fatalf("shared implementation %s interpolates parameter data as shell source", name)
			}
			if selector := step.Env[githubSelectorEnvironment]; selector != "" {
				selectorPrograms++
				if !strings.HasPrefix(selector, "${{ fromJSON(inputs.parameters).") || !strings.Contains(step.Run, `"$NVKR_ACTION_SELECTOR"`) {
					t.Fatalf("shared implementation %s does not pass its selector as one quoted environment value", name)
				}
			}
		}
	}
	if selectorPrograms == 0 {
		t.Fatal("fixture does not exercise a shared selector-consuming implementation")
	}
}

func TestGitHubSharedAttemptUploadNamesPreserveProviderContext(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) *Plan
		want    func(*Plan, githubActionRefs) githubStep
	}{
		{"inspection amd64", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/amd64") }, func(plan *Plan, actions githubActionRefs) githubStep {
			return githubInspectionDiagnosticStep(actions, plan.LifecycleOutputs[0].Name)
		}},
		{"inspection arm64", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/arm64") }, func(plan *Plan, actions githubActionRefs) githubStep {
			return githubInspectionDiagnosticStep(actions, plan.LifecycleOutputs[0].Name)
		}},
		{"qualification handoff", referenceTestPlan, func(_ *Plan, actions githubActionRefs) githubStep { return githubQualificationHandoffStep(actions) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.fixture(t)
			want := test.want(plan, testActionRefs(t, plan))
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			concrete := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			found := 0
			for _, job := range concrete.Jobs {
				for _, step := range job.Steps {
					if step.Name == want.Name {
						found++
						if !reflect.DeepEqual(step, want) {
							t.Fatalf("attempt-named upload changed name, controls or inputs: %+v", step)
						}
					}
				}
			}
			if found != 1 {
				t.Fatalf("expected one exact %q upload, got %d", want.Name, found)
			}
			data := sharedGitHubActionData(t, bundle)
			found = 0
			for _, entry := range data.Entries {
				for _, encoded := range entry.Values {
					var values map[string]string
					if err := canonical.UnmarshalExact([]byte(encoded), &values); err != nil {
						t.Fatal(err)
					}
					if values["upload_name"] != want.Name {
						continue
					}
					found++
					if values["upload_name_attempt"] != "true" || values["upload_with_name"]+githubAttemptUploadNameSuffix != want.With["name"] {
						t.Fatalf("attempt name was not separated into literal data: %v", values)
					}
					for key, value := range values {
						if strings.Contains(value, "${{") {
							t.Fatalf("provider expression escaped into invocation data %s", key)
						}
					}
				}
			}
			if found != 1 {
				t.Fatalf("expected one shared invocation for %q, got %d", want.Name, found)
			}
			workflow := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			found = 0
			for _, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if step.Name != want.Name {
						continue
					}
					found++
					var action yamlv3.Node
					path := strings.TrimPrefix(step.Uses, "./") + "/action.yml"
					if err := yamlv3.Unmarshal(bundle.Files[path], &action); err != nil || len(action.Content) != 1 {
						t.Fatalf("attempt upload does not use a shared action: %s / %v", step.Uses, err)
					}
					steps := githubYAMLField(githubYAMLField(action.Content[0], "runs"), "steps")
					if steps == nil || len(steps.Content) != 1 || githubYAMLValue(githubYAMLField(steps.Content[0], "with"), "name") != githubStandaloneUploadNameExpression {
						t.Fatal("provider run ID and attempt are not evaluated in the fixed action template")
					}
				}
			}
			if found != 1 {
				t.Fatalf("expected one shared action call for %q, got %d", want.Name, found)
			}
		})
	}
}

func TestGitHubSharedUploadRejectsOtherProviderNames(t *testing.T) {
	plan := githubTestPlan(t)
	actions := testActionRefs(t, plan)
	for _, test := range []struct{ name, artifact, path string }{
		{"different provider context", "nvkr-inspection-disk-${{ github.sha }}", githubInspectionLogPath("disk")},
		{"reversed attempt fields", "nvkr-inspection-disk-${{ github.run_attempt }}-${{ github.run_id }}", githubInspectionLogPath("disk")},
		{"unowned name", "arbitrary" + githubAttemptUploadNameSuffix, githubInspectionLogPath("disk")},
		{"mismatched diagnostic path", "nvkr-inspection-disk" + githubAttemptUploadNameSuffix, githubInspectionLogPath("other")},
		{"mismatched handoff path", githubQualificationHandoffName, ".nvkr/run.json"},
		{"secret expression", "${{ secrets.NKE_SOURCE_TOKEN }}", githubInspectionLogPath("disk")},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic := githubInspectionDiagnosticStep(actions, "disk")
			diagnostic.With["name"], diagnostic.With["path"] = test.artifact, test.path
			document := githubWorkflowDocument{Jobs: map[string]githubJob{"diagnostic": {RunsOn: "ubuntu-24.04", Steps: []githubStep{
				{Uses: actions.Checkout, With: map[string]string{"persist-credentials": "false"}},
				{Name: "Download NVKR runner", Uses: actions.Download, With: map[string]string{"name": "nvkr-exec-linux-amd64", "path": "."}},
				{Name: prepareHelperStepName, Run: prepareHelperRun},
				diagnostic,
			}}}}
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			workflow, files, err := lowerGitHubActions(plan, raw, actions)
			if err != nil {
				t.Fatal(err)
			}
			_, err = shareGitHubActions(plan, workflow, files, actions)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
		})
	}
}

func TestGitHubSharedActionsRejectDescriptorAndInterfaceTampering(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	rewriteData := func(changed *GeneratedBundle, mutate func(*githubActionDataDocument)) {
		data := sharedGitHubActionData(t, changed)
		mutate(&data)
		raw, err := canonical.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		changed.Files[GitHubActionDataPath] = raw
	}
	for _, test := range []struct {
		name       string
		code       e.Code
		diagnostic string
		mutate     func(*GeneratedBundle)
	}{
		{name: "missing data member", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			delete(changed.Files, GitHubActionDataPath)
		}},
		{name: "noncanonical data", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			changed.Files[GitHubActionDataPath] = append(bytes.Clone(changed.Files[GitHubActionDataPath]), '\n')
		}},
		{name: "missing job entry", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) { delete(data.Entries, sortedKeys(data.Entries)[0]) })
		}},
		{name: "unknown context template", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) {
				job := sortedKeys(data.Entries)[0]
				entry := data.Entries[job]
				entry.Template = "unplanned-template"
				data.Entries[job] = entry
			})
		}},
		{name: "missing invocation", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) {
				entry := data.Entries[sortedKeys(data.Entries)[0]]
				delete(entry.Values, sortedKeys(entry.Values)[0])
			})
		}},
		{name: "invalid invocation encoding", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) {
				entry := data.Entries[sortedKeys(data.Entries)[0]]
				entry.Values[sortedKeys(entry.Values)[0]] = "{}\n"
			})
		}},
		{name: "missing parameter", code: e.CodePipelineDrift, mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) {
				for _, job := range sortedKeys(data.Entries) {
					entry := data.Entries[job]
					for _, part := range sortedKeys(entry.Values) {
						var values map[string]string
						if err := canonical.UnmarshalExact([]byte(entry.Values[part]), &values); err != nil {
							t.Fatal(err)
						}
						if _, ok := values["execute_name"]; !ok || values["execute_present"] != "true" {
							continue
						}
						delete(values, "execute_name")
						raw, err := canonical.Marshal(values)
						if err != nil {
							t.Fatal(err)
						}
						entry.Values[part] = string(raw)
						return
					}
				}
				t.Fatal("fixture has no active execution name parameter")
			})
		}},
		{name: "unplanned selector", code: e.CodeEngineCapability, diagnostic: "must execute exact node", mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) {
				for _, job := range sortedKeys(data.Entries) {
					entry := data.Entries[job]
					for _, part := range sortedKeys(entry.Values) {
						var values map[string]string
						if err := canonical.UnmarshalExact([]byte(entry.Values[part]), &values); err != nil {
							t.Fatal(err)
						}
						if values["execute_present"] != "true" || values["execute_selector"] == "" {
							continue
						}
						values["execute_selector"] = "not-an-admitted-operation"
						raw, err := canonical.Marshal(values)
						if err != nil {
							t.Fatal(err)
						}
						entry.Values[part] = string(raw)
						return
					}
				}
				t.Fatal("fixture has no active execution selector to mutate")
			})
		}},
		{name: "extra invocation", code: e.CodeEngineCapability, mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) {
				entry := data.Entries[sortedKeys(data.Entries)[0]]
				entry.Values["part-999"] = "{}"
			})
		}},
		{name: "missing action input interface", code: e.CodeEngineCapability, mutate: func(changed *GeneratedBundle) {
			for _, name := range sortedFileNames(changed.Files) {
				if !strings.HasPrefix(name, githubSharedDirectory+"execute-") {
					continue
				}
				var action yamlv3.Node
				if err := yamlv3.Unmarshal(changed.Files[name], &action); err != nil {
					t.Fatal(err)
				}
				if githubYAMLField(action.Content[0], "inputs") == nil {
					t.Fatal("fixture execution action has no parameter interface")
				}
				githubYAMLRemove(action.Content[0], "inputs")
				raw, err := encodeGitHubYAML(&action)
				if err != nil {
					t.Fatal(err)
				}
				changed.Files[name] = raw
				return
			}
			t.Fatal("fixture has no shared execution action")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *bundle
			changed.Files = maps.Clone(bundle.Files)
			test.mutate(&changed)
			err := ValidateGitHubBundle(t.Context(), plan, &changed)
			assertFactoryErrorCode(t, err, test.code)
			if test.diagnostic != "" && !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("mutation reached the wrong admission guard: %v", err)
			}
		})
	}
}

func TestWriteGitHubActionDataDerivesBoundedOutputFromPlan(t *testing.T) {
	for _, name := range []string{"", "project-a", "project-b"} {
		t.Run(name, func(t *testing.T) {
			plan := namedGitHubTestPlan(t, name)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			data := sharedGitHubActionData(t, bundle)
			selector := sortedKeys(data.Entries)[0]
			wantData, err := canonical.Marshal(data.Entries[selector].Values)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			t.Chdir(root)
			if err := os.MkdirAll(filepath.Dir(githubActionDataPathFor(plan)), 0o755); err != nil {
				t.Fatal(err)
			}
			untrusted := []byte(`{"entries":{"injected":{"template":"stage-context","values":{"part-1":"malicious command"}}}}`)
			if err := os.WriteFile(githubActionDataPathFor(plan), untrusted, 0o644); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "github-output")
			if err := os.WriteFile(output, []byte("existing=value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := WriteGitHubActionData(t.Context(), plan, selector, output); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			want := "existing=value\ndata=" + string(wantData) + "\n"
			if string(raw) != want || bytes.Count(raw, []byte("\n")) != 2 || len(raw)-len("existing=value\n") > maxGitHubActionDataOutputBytes {
				t.Fatal("provider output differs from bounded, single-line canonical Plan-derived data")
			}
			retained, err := os.ReadFile(githubActionDataPathFor(plan))
			if err != nil || !bytes.Equal(retained, untrusted) {
				t.Fatalf("action description modified the checked-in data file: %v", err)
			}
		})
	}
}

func TestWriteGitHubActionDataRejectsBeforeOutput(t *testing.T) {
	plan := githubTestPlan(t)
	for _, test := range []struct {
		name     string
		selector string
		canceled bool
		code     e.Code
	}{
		{name: "missing selector", code: e.CodeUsage},
		{name: "unknown selector", selector: "not-an-admitted-job", code: e.CodePipelineDrift},
		{name: "canceled", selector: "not-an-admitted-job", canceled: true, code: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "must-not-exist")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			assertFactoryErrorCode(t, WriteGitHubActionData(ctx, plan, test.selector, output), test.code)
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("rejected action description wrote provider output: %v", err)
			}
		})
	}
}
