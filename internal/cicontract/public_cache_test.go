// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type storageWorkflowStep struct {
	Name            string            `json:"name"`
	Run             string            `json:"run"`
	Uses            string            `json:"uses"`
	If              string            `json:"if"`
	ContinueOnError any               `json:"continue-on-error"`
	With            map[string]any    `json:"with"`
	Env             map[string]string `json:"env"`
}

func TestWorkflowsDoNotRetainCompiledCaches(t *testing.T) {
	const directory = "../../.github/workflows"
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	codeQLInitializers, codeQLAnalyzers := 0, 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".yml") && !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		t.Run(file.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(directory, file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Jobs map[string]struct {
					Steps []storageWorkflowStep `json:"steps"`
				} `json:"jobs"`
			}
			if err := yaml.Unmarshal(raw, &workflow); err != nil {
				t.Fatal(err)
			}
			for jobID, job := range workflow.Jobs {
				codeQLAdmitted := false
				for _, step := range job.Steps {
					if step.Name == "Admit CodeQL without database artifact uploads" {
						if step.If != "" || step.ContinueOnError != nil && step.ContinueOnError != false {
							t.Fatalf("%s can bypass CodeQL debug admission", jobID)
						}
						assertCodeQLDebugAdmission(t, step.Run)
						codeQLAdmitted = true
					}
					switch {
					case strings.HasPrefix(step.Uses, "actions/cache"):
						t.Fatalf("%s retains repository-accessible Actions caches", jobID)
					case strings.HasPrefix(step.Uses, "actions/setup-go@"):
						if step.With["cache"] != false && step.With["cache"] != "false" {
							t.Fatalf("%s permits setup-go compiled caches", jobID)
						}
					case strings.HasPrefix(step.Uses, "actions/setup-node@"):
						if step.With["package-manager-cache"] != false && step.With["package-manager-cache"] != "false" {
							t.Fatalf("%s leaves implicit package manager caches enabled", jobID)
						}
					case strings.HasPrefix(step.Uses, "golangci/golangci-lint-action@"):
						if step.With["skip-cache"] != true && step.With["skip-cache"] != "true" {
							t.Fatalf("%s permits compiled analysis caches", jobID)
						}
					case strings.HasPrefix(step.Uses, "github/codeql-action/init@"):
						codeQLInitializers++
						if !codeQLAdmitted || step.If != "" || step.Env["CODEQL_OVERLAY_DATABASE_MODE"] != "none" {
							t.Fatalf("%s permits CodeQL debug artifacts or overlay database caches", jobID)
						}
						for _, input := range []string{"debug", "trap-caching", "dependency-caching"} {
							if step.With[input] != false && step.With[input] != "false" {
								t.Fatalf("%s does not disable CodeQL %s", jobID, input)
							}
						}
					case strings.HasPrefix(step.Uses, "github/codeql-action/analyze@"):
						codeQLAnalyzers++
						if step.With["upload-database"] != false && step.With["upload-database"] != "false" {
							t.Fatalf("%s uploads compiled CodeQL databases", jobID)
						}
					}
				}
			}
		})
	}
	if codeQLInitializers == 0 || codeQLAnalyzers != codeQLInitializers {
		t.Fatal("private storage protections must preserve complete CodeQL analysis")
	}
}

func assertCodeQLDebugAdmission(t *testing.T, script string) {
	t.Helper()
	for _, debug := range []string{"0", "1"} {
		command := exec.CommandContext(t.Context(), "bash", "-eu", "-c", script)
		command.Env = append(os.Environ(), "RUNNER_DEBUG="+debug)
		output, err := command.CombinedOutput()
		if (err != nil) != (debug == "1") || debug == "1" && !strings.Contains(string(output), "NVKR-E-ENGINE-CAPABILITY") {
			t.Fatalf("CodeQL debug admission (%s): err=%v output=%s", debug, err, output)
		}
	}
}
