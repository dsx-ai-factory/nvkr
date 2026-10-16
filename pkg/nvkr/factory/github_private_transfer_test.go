// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubPrivateTransfersExcludePayloadsFromActionsStorage(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"production", githubTestPlan},
		{"development", githubDevelopmentPublicationTestPlan},
		{"disk", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/amd64") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			transfers := 0
			for name, raw := range bundle.Files {
				if name != bundle.WorkflowPath && !strings.HasSuffix(name, "/action.yml") {
					continue
				}
				var document yamlv3.Node
				if err := yamlv3.Unmarshal(raw, &document); err != nil {
					t.Fatal(err)
				}
				var inspect func(*yamlv3.Node)
				inspect = func(node *yamlv3.Node) {
					uses := githubYAMLValue(node, "uses")
					if strings.HasPrefix(uses, "actions/download-artifact@") {
						t.Errorf("%s retains public artifact downloads", name)
					}
					if strings.HasPrefix(uses, "actions/upload-artifact@") {
						path := githubYAMLValue(githubYAMLField(node, "with"), "path")
						if name != bundle.WorkflowPath || (path != ".nvkr/run.json" && path != githubQualificationHandoffPath) {
							t.Errorf("%s can upload payload %q to public run storage", name, path)
						}
					}
					if uses == "./"+githubTransferDirectory(plan) {
						transfers++
						with := githubYAMLField(node, "with")
						if githubYAMLValue(with, "mode") == "download" && githubYAMLValue(with, "expected-index-file") == "" {
							t.Errorf("%s downloads without provider-passed digests", name)
						}
					}
					for _, child := range node.Content {
						inspect(child)
					}
				}
				inspect(&document)
			}
			if transfers == 0 {
				t.Fatal("no private transfers")
			}
			for id, job := range decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Jobs {
				if job.Outputs[githubTransferIndexID] != "${{ steps."+githubTransferIndexID+".outputs.index }}" {
					t.Errorf("job %s omits immutable transfer index", id)
				}
				for key := range job.Env {
					if strings.HasPrefix(key, "NVKR_TRANSFER_") {
						t.Errorf("job %s scopes transfer credential to whole job", id)
					}
				}
			}
		})
	}
}

func TestGitHubPrivateDependencyContextNeverEntersEnvironment(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for id, job := range decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Jobs {
		capture, checkout := -1, -1
		for position, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") {
				checkout = position
			}
			if step.ID == githubTransferContextID {
				capture = position
				if step.Run != githubTransferContextScript || checkout < 0 || checkout >= capture {
					t.Errorf("job %s captures dependency context outside its admitted post-checkout script", id)
				}
			}
			for _, value := range step.Env {
				if strings.Contains(value, "toJSON(needs)") {
					t.Errorf("job %s places complete dependency context in an environment variable", id)
				}
			}
			for key, value := range step.With {
				if strings.Contains(value, "toJSON(needs)") {
					t.Errorf("job %s places complete dependency context in action input %s", id, key)
				}
				if key == "transfer-needs" && (value != githubTransferContextPath || capture < 0 || capture >= position) {
					t.Errorf("job %s consumes dependency indices before staging their file", id)
				}
				if key == "nvkr-job-results" && value != githubTransferResults {
					t.Errorf("job %s recovery does not use compact producer outcomes", id)
				}
			}
		}
	}
}

func TestGitHubPrivateDependencyContextStagesLargeHostileJSON(t *testing.T) {
	root := t.TempDir()
	output, marker := filepath.Join(root, "github output"), filepath.Join(root, "must-not-exist")
	if err := os.WriteFile(output, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	needs := map[string]any{}
	for i := range 600 {
		index, err := json.Marshal(map[string]any{"version": 1, "scope": map[string]string{
			"repository": "example/project", "run": "12345678901", "attempt": "1",
			"workflow": "example/project/.github/workflows/build.yml@refs/heads/main",
		}, "entries": map[string]string{fmt.Sprintf("carrier-%d", i): "sha256:" + strings.Repeat("a", 64)}})
		if err != nil {
			t.Fatal(err)
		}
		needs[fmt.Sprintf("producer-%d", i)] = map[string]any{"result": "success", "outputs": map[string]string{"nvkr-transfer-index": string(index)}}
	}
	needs["hostile"] = map[string]any{"result": "success", "outputs": map[string]string{
		"attack": "\nNVKR_PRIVATE_NEEDS_JSON_END\ntouch '" + marker + "'\n$(touch '" + marker + "') `touch '" + marker + "'` ${HOME}",
	}}
	raw, err := json.MarshalIndent(needs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 128*1024 {
		t.Fatalf("fixture must exceed the Linux environment-string limit, got %d", len(raw))
	}
	script := strings.Replace(githubTransferContextScript, "${{ toJSON(needs) }}", string(raw), 1)
	scriptPath := filepath.Join(root, "capture.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "bash", scriptPath)
	command.Env = append(os.Environ(), "RUNNER_TEMP="+root, "GITHUB_OUTPUT="+output)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("stage large context: %v: %s", err, result)
	}
	line, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	staged := strings.TrimSuffix(strings.TrimPrefix(string(line), "path="), "\n")
	actual, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, append(raw, '\n')) {
		t.Fatal("dependency context changed while staging")
	}
	info, err := os.Stat(staged)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("dependency file must be private: %v, %v", info, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("dependency output escaped the quoted JSON heredoc")
	}
}

func TestGitHubPrivateTransportTamperingFailsClosed(t *testing.T) {
	for _, test := range []struct{ name, from, to string }{
		{"credential", "${{ secrets.NVKR_TRANSFER_WRITE }}", "${{ github.token }}"},
		{"index", "${{ steps.nvkr-transfer-index.outputs.index }}", "sha256:untrusted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			raw := bundle.Files[bundle.WorkflowPath]
			if !bytes.Contains(raw, []byte(test.from)) {
				t.Fatal("fixture missing mutation target")
			}
			changed := mutateGitHubBundleFile(t, bundle, bundle.WorkflowPath, func(raw []byte) []byte {
				return bytes.Replace(raw, []byte(test.from), []byte(test.to), 1)
			})
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}
