// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestChildWorkDeadlineBoundsCommandContext(t *testing.T) {
	for _, test := range []struct {
		name       string
		value      string
		fromEnv    bool
		parentWins bool
		canceled   bool
		actionFail bool
		wait       bool
		want       e.Code
	}{
		{name: "unchanged without handoff"},
		{name: "absolute deadline", value: "future"},
		{name: "bootstrap environment", value: "future", fromEnv: true},
		{name: "earlier parent wins", value: "future", parentWins: true},
		{name: "malformed", value: "tomorrow", want: e.CodeUsage},
		{name: "expired", value: "past", want: e.CodeTimeout},
		{name: "canceled parent", value: "future", canceled: true, want: e.CodeTimeout},
		{name: "failed command cancels timer", value: "future", actionFail: true, want: e.CodeBuildFailed},
		{name: "deadline stops command", value: "future", wait: true, want: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NVKR_WORK_DEADLINE", "")
			deadline := time.Now().Add(5 * time.Minute).UTC()
			if test.wait {
				deadline = time.Now().Add(100 * time.Millisecond).UTC()
			}
			value := test.value
			switch value {
			case "future":
				value = deadline.Format(time.RFC3339Nano)
			case "past":
				value = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
			}
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.parentWins {
				deadline = time.Now().Add(time.Minute)
				var stop context.CancelFunc
				parent, stop = context.WithDeadline(parent, deadline)
				defer stop()
			}
			if test.canceled {
				cancel()
			}
			var observed context.Context
			app := newApp()
			app.Commands = []*cli.Command{{Name: "probe", Subcommands: []*cli.Command{{Name: "child", Action: func(c *cli.Context) error {
				observed = c.Context
				if test.wait {
					<-c.Done()
					return e.Wrap(e.CodeTimeout, c.Err(), "synthetic work stopped")
				}
				if test.actionFail {
					return e.New(e.CodeBuildFailed, "synthetic command failure")
				}
				return nil
			}}}}}
			args := []string{"nvkr"}
			if test.fromEnv {
				t.Setenv("NVKR_WORK_DEADLINE", value)
			} else if value != "" {
				args = append(args, "--work-deadline", value)
			}
			err := app.RunContext(parent, append(args, "probe", "child"))
			if test.want != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.want {
					t.Fatalf("error=%v, want %s", err, test.want)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if observed == nil {
				if test.want == "" || test.actionFail || test.wait {
					t.Fatal("command did not run")
				}
				return
			}
			if value == "" {
				if observed != parent || observed.Err() != nil {
					t.Fatal("missing handoff changed caller context")
				}
				return
			}
			got, bounded := observed.Deadline()
			cause := context.Canceled
			if test.wait {
				cause = context.DeadlineExceeded
			}
			if !bounded || !got.Equal(deadline) || !errors.Is(observed.Err(), cause) {
				t.Fatalf("deadline=%s, bounded=%v, error=%v", got, bounded, observed.Err())
			}
		})
	}
}

func lifecycleCommandRepo(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

func invokeLifecycleCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	raw, err := runCLIWithCapturedStdout(t, newApp(), append([]string{"nvkr"}, args...)...)
	return string(raw), err
}

func TestPublicDefinitionCommandsPreserveIdentity(t *testing.T) {
	lifecycleCommandRepo(t)
	const path = "docs/examples/nvke-tutorial/definition.yaml"
	want, err := lifecycle.LoadDefinition(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := want.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"validate", "inspect"} {
		t.Run(command, func(t *testing.T) {
			output, err := invokeLifecycleCLI(t, "definition", command, "--definition", path)
			if err != nil {
				t.Fatal(err)
			}
			if command == "validate" {
				if !strings.Contains(output, digest) || !strings.Contains(output, "outputs: 3") {
					t.Fatalf("definition summary lost identity or required outputs: %s", output)
				}
				return
			}
			var got lifecycle.Definition
			if err := json.Unmarshal([]byte(output), &got); err != nil {
				t.Fatal(err)
			}
			gotDigest, err := got.Digest()
			if err != nil || gotDigest != digest {
				t.Fatalf("inspected definition changed identity: %s %v", gotDigest, err)
			}
		})
	}
}

func TestRunCommandsPlanGenerateAndCheckDrift(t *testing.T) {
	lifecycleCommandRepo(t)
	inputs := []string{"--definition", "docs/examples/nvke-tutorial/definition.yaml", "--environment", "docs/examples/nvke-tutorial/environment-github.yaml"}
	for _, asJSON := range []bool{false, true} {
		args := append([]string{"run", "plan"}, inputs...)
		if asJSON {
			args = append(args, "--json")
		}
		output, err := invokeLifecycleCLI(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if asJSON {
			var plan factory.Plan
			if err := json.Unmarshal([]byte(output), &plan); err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 3 || plan.Qualification != "pipeline-attested" {
				t.Fatalf("unexpected CLI plan: %+v", plan)
			}
		} else if !strings.Contains(output, "expected outputs: 3") {
			t.Fatalf("missing frozen output count: %s", output)
		}
	}
	root := t.TempDir()
	args := append(append([]string{"run", "generate"}, inputs...), "--root", root)
	if _, err := invokeLifecycleCLI(t, args...); err != nil {
		t.Fatal(err)
	}
	checkArgs := append(args, "--check")
	if output, err := invokeLifecycleCLI(t, checkArgs...); err != nil || !strings.Contains(output, "generated run is current") {
		t.Fatalf("fresh generation did not check cleanly: %s %v", output, err)
	}
	workflow := filepath.Join(root, ".github", "workflows", "nvkr-run.yml")
	if err := os.WriteFile(workflow, []byte("unexpected workflow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := invokeLifecycleCLI(t, checkArgs...)
	assertLifecycleCommandCode(t, err, e.CodePipelineDrift)
	raw, err := os.ReadFile(workflow)
	if err != nil || string(raw) != "unexpected workflow\n" {
		t.Fatalf("check mode rewrote drifted input: %q %v", raw, err)
	}
}

func TestLocalRunDryRunDoesNotCreateAttempt(t *testing.T) {
	lifecycleCommandRepo(t)
	root := filepath.Join(".nvkr", filepath.Base(filepath.Dir(t.TempDir())))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	output, err := invokeLifecycleCLI(t, "run", "local", "--definition", "docs/examples/nvke-tutorial/definition.yaml",
		"--environment", "docs/examples/nvke-tutorial/environment-local.yaml", "--attempt", "test-dry-run", "--attempts-root", root, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "attempt: test-dry-run") || !strings.Contains(output, "lane: static-unqualified") {
		t.Fatalf("dry-run presentation: %s", output)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created attempt storage: %v", err)
	}
}

func TestLifecycleCommandsRejectInvalidInputs(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"missing definition", []string{"definition", "validate", "--definition", "missing.yaml"}, e.CodeInvalidCatalog},
		{"missing inspection", []string{"definition", "inspect", "--definition", "missing.yaml"}, e.CodeInvalidCatalog},
		{"missing plan", []string{"run", "plan", "--definition", "missing.yaml", "--environment", "missing.yaml"}, e.CodeInvalidCatalog},
		{"missing environment", []string{"run", "plan", "--definition", "docs/examples/nvke-tutorial/definition.yaml", "--environment", "missing.yaml"}, e.CodeInvalidCatalog},
		{"unsupported engine", []string{"run", "generate", "--definition", "missing.yaml", "--environment", "missing.yaml", "--engine", "tekton"}, e.CodeEngineCapability},
		{"invalid attempt", []string{"run", "local", "--definition", "docs/examples/nvke-tutorial/definition.yaml", "--environment", "docs/examples/nvke-tutorial/environment-local.yaml", "--attempt", "../escape", "--dry-run"}, e.CodeUsage},
		{"missing finalizer inputs", []string{"run", "finalize", "--definition", "missing.yaml", "--environment", "missing.yaml", "--plan", "missing.json", "--attempt", "test-run"}, e.CodeInvalidCatalog},
		{"wrong factory workspace", []string{"factory", "execute-node", "--plan", "missing.json", "--node", "missing", "--workspace", "wrong"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := invokeLifecycleCLI(t, tc.args...)
			assertLifecycleCommandCode(t, err, tc.code)
		})
	}
}

func assertLifecycleCommandCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
