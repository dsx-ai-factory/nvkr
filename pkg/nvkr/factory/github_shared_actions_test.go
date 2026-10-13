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
	if err := canonical.UnmarshalExact(bundle.Files[GitHubActionDataPath], &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Entries) == 0 {
		t.Fatal("fixture has no shared action data")
	}
	return data
}

func TestGitHubSharedActionCountDoesNotGrowWithEquivalentJobs(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
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
				fmt.Fprintf(&source, "      - name: Execute producer %d\n        id: producer-%d\n        run: %s\n", index, index, githubRunnerCommand("run-node", "node", selector))
				fmt.Fprintf(&source, "      - name: Collect producer %d\n        run: %s\n", index, githubRunnerCommand("pack-outputs", "node", selector))
				fmt.Fprintf(&source, "      - name: Upload producer %d\n        uses: %s\n        with:\n          name: producer-carrier-%d\n          path: producer-%d.tar\n", index, actions.Upload, index, index)
			}
			workflow, files, err := lowerGitHubActions(source.Bytes(), actions)
			if err != nil {
				t.Fatal(err)
			}
			workflow, err = shareGitHubActions(workflow, files, actions)
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
						if step.Uses != executionTemplate || !strings.HasPrefix(step.Uses, "./"+githubSharedDirectory+"execute-node-") {
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
			expanded, err := expandGitHubActions(workflow, files)
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
	bundle, err := RenderGitHub(t.Context(), githubTestPlan(t))
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
		if !strings.HasPrefix(name, githubSharedDirectory) {
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
		name   string
		code   e.Code
		mutate func(*GeneratedBundle)
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
				entry := data.Entries[sortedKeys(data.Entries)[0]]
				part := sortedKeys(entry.Values)[0]
				var values map[string]string
				if err := canonical.UnmarshalExact([]byte(entry.Values[part]), &values); err != nil {
					t.Fatal(err)
				}
				removed := false
				for _, key := range sortedKeys(values) {
					if strings.HasSuffix(key, "_name") {
						delete(values, key)
						removed = true
						break
					}
				}
				if !removed {
					t.Fatal("fixture has no required step name parameter")
				}
				raw, err := canonical.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				entry.Values[part] = string(raw)
			})
		}},
		{name: "unplanned selector", code: e.CodeEngineCapability, mutate: func(changed *GeneratedBundle) {
			rewriteData(changed, func(data *githubActionDataDocument) {
				for _, job := range sortedKeys(data.Entries) {
					entry := data.Entries[job]
					for _, part := range sortedKeys(entry.Values) {
						var values map[string]string
						if err := canonical.UnmarshalExact([]byte(entry.Values[part]), &values); err != nil {
							t.Fatal(err)
						}
						for _, key := range sortedKeys(values) {
							if strings.HasSuffix(key, "_selector") {
								values[key] = "not-an-admitted-operation"
								raw, err := canonical.Marshal(values)
								if err != nil {
									t.Fatal(err)
								}
								entry.Values[part] = string(raw)
								return
							}
						}
					}
				}
				t.Fatal("fixture has no operation selector to mutate")
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
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), test.code)
		})
	}
}

func TestWriteGitHubActionDataDerivesBoundedOutputFromPlan(t *testing.T) {
	plan := githubTestPlan(t)
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
	if err := os.MkdirAll(filepath.Dir(GitHubActionDataPath), 0o755); err != nil {
		t.Fatal(err)
	}
	untrusted := []byte(`{"entries":{"injected":{"template":"stage-context","values":{"part-1":"malicious command"}}}}`)
	if err := os.WriteFile(GitHubActionDataPath, untrusted, 0o644); err != nil {
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
	retained, err := os.ReadFile(GitHubActionDataPath)
	if err != nil || !bytes.Equal(retained, untrusted) {
		t.Fatalf("action description modified the checked-in data file: %v", err)
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
