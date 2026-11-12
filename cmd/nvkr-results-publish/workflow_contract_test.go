// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
	yaml "go.yaml.in/yaml/v3"
)

// committedQualifier is the generated release qualifier whose publish job runs this helper.
const committedQualifier = "../../.github/workflows/nvkr-qualify.yml"

// committedCommand returns the helper invocation in the named step of the committed
// publish job, as argv after the program name, expanding the job's variables.
func committedCommand(t *testing.T, step string, vars map[string]string) []string {
	t.Helper()
	raw, err := os.ReadFile(committedQualifier)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range workflow.Jobs["publish"].Steps {
		if candidate.Name != step {
			continue
		}
		for line := range strings.SplitSeq(candidate.Run, "\n") {
			if command, ok := strings.CutPrefix(strings.TrimSpace(line), "./nvkr-results-publish "); ok {
				command, _, _ = strings.Cut(command, " >> ")
				return shellWords(t, command, vars)
			}
		}
	}
	t.Fatalf("the committed publish job has no %q step running the helper", step)
	return nil
}

// shellWords splits the rendered command as bash would for the forms the
// generator emits: bare words, single-quoted literals and double-quoted $NAME
// expansions. The generator quotes every expansion.
func shellWords(t *testing.T, command string, vars map[string]string) []string {
	t.Helper()
	expand := func(s string) string {
		return os.Expand(s, func(name string) string {
			value, ok := vars[name]
			if !ok {
				t.Fatalf("the committed command uses $%s, which the job does not set", name)
			}
			return value
		})
	}
	var words []string
	var word strings.Builder
	inWord := false
	for i := 0; i < len(command); i++ {
		switch c := command[i]; c {
		case ' ':
			if inWord {
				words, inWord = append(words, word.String()), false
				word.Reset()
			}
		case '\'', '"':
			end := strings.IndexByte(command[i+1:], c)
			if end < 0 {
				t.Fatalf("unterminated quote in %q", command)
			}
			quoted := command[i+1 : i+1+end]
			if c == '"' {
				quoted = expand(quoted)
			}
			word.WriteString(quoted)
			inWord, i = true, i+1+end
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// The committed publish job's select and publish commands must parse and run
// against this helper: a renamed flag or a mismatched path fails here, not on a
// hosted Run, where continue-on-error would hide it.
func TestResultsPublishCommittedWorkflowCommands(t *testing.T) {
	outcomes := filepath.Join(t.TempDir(), "nvkr-qualification-outcomes")
	if err := os.Mkdir(outcomes, 0o700); err != nil {
		t.Fatal(err)
	}
	// A failed producer: the qualify job was skipped, so its report is empty.
	vars := map[string]string{"OUTCOMES": outcomes, "GITHUB_EVENT_PATH": producerEvent(t, "failure", "push"), "GITHUB_RUN_ATTEMPT": "1",
		"QUALIFY_RESULT": "skipped", "QUALIFY_ATTEMPT": "", "OUTCOME_WRITTEN": ""}
	selected := runCLI(t, jobEnvironment(nil), nil, committedCommand(t, "Select the outcomes to publish", vars)...)
	if selected.status != 0 || selected.stdout != "built=true\n" {
		t.Fatalf("committed select: %+v", selected)
	}
	if _, err := os.Stat(filepath.Join(outcomes, "built-1.json")); err != nil {
		t.Fatalf("select did not write the outcome where the committed upload step reads it: %v", err)
	}
	store := t.TempDir()
	open := func(context.Context, string) (resultspublish.ObjectStore, func() error, error) {
		return localStore{root: store}, func() error { return nil }, nil
	}
	published := runCLI(t, jobEnvironment(nil), open, committedCommand(t, "Publish the outcomes", vars)...)
	if published.status != 0 || !strings.Contains(published.stderr, "published 1 outcome(s)") {
		t.Fatalf("committed publish: %+v", published)
	}
	if _, err := os.Stat(filepath.Join(store, "results", "nvke", "nvke-m2", "101-1")); err != nil {
		t.Fatalf("committed publish did not write the canonical record: %v", err)
	}
}
