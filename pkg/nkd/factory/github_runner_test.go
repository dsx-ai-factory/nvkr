// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRunGitHubNodeAdmitsCompletePlanBeforeEffects(t *testing.T) {
	for _, name := range []string{"nil-context", "canceled", "nil-plan", "unknown-node", "changed-sibling", "missing-input-path"} {
		t.Run(name, func(t *testing.T) {
			plan := githubTestPlan(t)
			ctx := t.Context()
			id := plan.Graph.Nodes[0].ID
			want := e.CodeInvalidCatalog
			switch name {
			case "nil-context":
				ctx = nil
				want = e.CodeUsage
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = e.CodeTimeout
			case "nil-plan":
				plan = nil
				want = e.CodeUsage
			case "unknown-node":
				id = "absent"
			case "changed-sibling":
				plan.Graph.Nodes[len(plan.Graph.Nodes)-1].Outputs = []string{"invented"}
			case "missing-input-path":
				plan.DefinitionPath = ""
				want = e.CodeInvalidCatalog
			}
			var log bytes.Buffer
			summary := filepath.Join(t.TempDir(), "summary")
			ran := false
			err := runGitHubNode(ctx, plan, id, GitHubStepOptions{Stdout: &log, Stderr: &log, SummaryPath: summary}, func(context.Context, *Plan, Node, GitHubStepOptions) error { ran = true; return nil })
			assertFactoryErrorCode(t, err, want)
			if ran || log.Len() != 0 {
				t.Fatalf("invalid admission caused effects: ran=%t log=%s", ran, &log)
			}
			if _, err := os.Stat(summary); !os.IsNotExist(err) {
				t.Fatal("invalid admission wrote summary")
			}
		})
	}
}

func TestRunGitHubNodeSourceCredentialScope(t *testing.T) {
	for _, token := range []string{"", "do-not-log-this"} {
		t.Run(fmt.Sprintf("present=%t", token != ""), func(t *testing.T) {
			plan, fetch, _ := authenticatedSourcePlan(t)
			t.Setenv(sourceTokenEnv, token)
			var log bytes.Buffer
			calls := 0
			execute := func(_ context.Context, _ *Plan, node Node, _ GitHubStepOptions) error {
				calls++
				if node.ID != fetch.ID {
					t.Fatal("wrong operation")
				}
				return nil
			}
			err := runGitHubNode(t.Context(), plan, fetch.ID, GitHubStepOptions{Stdout: &log, Stderr: &log}, execute)
			if token == "" {
				assertFactoryErrorCode(t, err, e.CodeFetchFailed)
				if calls != 0 || strings.Contains(log.String(), "Produced:") || !strings.Contains(log.String(), "check source credential (exit 20") {
					t.Fatalf("credential preflight lost: %s", &log)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("valid fetch failed: %v", err)
			}
			if strings.Contains(log.String(), "do-not-log-this") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestRunGitHubNodeLeavesPackingSeparate(t *testing.T) {
	plan := githubTestPlan(t)
	var selected Node
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage {
			selected = node
			break
		}
	}
	if selected.ID == "" {
		t.Fatal("missing package fixture")
	}
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(sourceTokenEnv, "")
	var log bytes.Buffer
	calls := 0
	err := runGitHubNode(t.Context(), plan, selected.ID, GitHubStepOptions{Stdout: &log, Stderr: &log}, func(_ context.Context, _ *Plan, node Node, _ GitHubStepOptions) error {
		calls++
		if node.ID != selected.ID {
			t.Fatal("wrong selected node")
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("execution failed: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("run-node prepared carriers or wrote other files: %v %v", entries, err)
	}
	if !strings.Contains(log.String(), "Produced: "+selected.Outputs[0]) {
		t.Fatalf("missing selected output: %s", &log)
	}
}

func githubRunnerRuntimeFixture(t *testing.T, script string) (*Plan, Node, string) {
	return githubRunnerRuntimeFixtureFor(t, "", script)
}

func githubRunnerRuntimeFixtureFor(t *testing.T, runtimeOverride, script string) (*Plan, Node, string) {
	t.Helper()
	plan := githubTestPlan(t)
	if runtimeOverride != "" {
		plan.Environment.Runtime.Runtime = runtimeOverride
		digest, err := plan.Environment.Digest()
		if err != nil {
			t.Fatal(err)
		}
		plan.EnvironmentDigest = digest
	}
	var node Node
	for _, candidate := range plan.Graph.Nodes {
		if candidate.Kind == NodePackage && candidate.Container != "" {
			node = candidate
			break
		}
	}
	if node.ID == "" {
		t.Fatal("fixture lacks utility node")
	}
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := filepath.Join(root, "runtime-calls")
	t.Setenv("NKD_TEST_RUNTIME_LOG", calls)
	runtime := filepath.Join(root, plan.Environment.RuntimeBinary())
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf '%s\\n' CALL \"$@\" >> \"$NKD_TEST_RUNTIME_LOG\"\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(FactoryPlanPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FactoryPlanPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return plan, node, calls
}

func TestRunGitHubNodeUtilityStatusAndExactLaunch(t *testing.T) {
	for _, status := range []int{0, 22, 23, 125, 137} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			plan, node, calls := githubRunnerRuntimeFixture(t, fmt.Sprintf("[ \"$1\" = rm ] && exit 0\nprintf 'child diagnostic\\n'\nexit %d\n", status))
			var log bytes.Buffer
			err := RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{Stdout: &log, Stderr: &log})
			if got := GitHubStepExitCode(err); got != status {
				t.Fatalf("exit=%d want%d: %v", got, status, err)
			}
			if status != 0 {
				want := e.CodeEngineCapability
				if status == 22 {
					want = e.CodeVerifyFailed
				}
				assertFactoryErrorCode(t, err, want)
				if strings.Contains(log.String(), "Produced:") || strings.Contains(log.String(), "Completed:") {
					t.Fatalf("failed utility reported success: %s", &log)
				}
			}
			raw, readErr := os.ReadFile(calls)
			if readErr != nil {
				t.Fatal(readErr)
			}
			digest, digestErr := plan.Digest()
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			for _, want := range []string{"CALL\nrun\n--rm\n--name\n", "--network\nnone\n--platform\n" + node.Platform, "/workspace/nkd-exec\nfactory\nexecute-node\n", "--plan\n" + FactoryPlanPath, "--node\n" + node.ID, "--plan-digest\n" + digest, "CALL\nrm\n-f\n"} {
				if !strings.Contains(string(raw), want) {
					t.Fatalf("missing%q: %s", want, raw)
				}
			}
			if strings.Contains(string(raw), "--privileged") || strings.Contains(string(raw), sourceTokenEnv) {
				t.Fatalf("unadmitted privilege/credential: %s", raw)
			}
		})
	}
}

func TestRunGitHubNodeRejectsReplacedUtilityPlanBeforeLaunch(t *testing.T) {
	plan, node, calls := githubRunnerRuntimeFixture(t, "exit 0\n")
	other := *plan
	other.GeneratorVersion = "different (source)"
	raw, err := canonical.Marshal(&other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FactoryPlanPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	err = RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{})
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("changed Plan reached utility runtime")
	}
}

func TestRunGitHubNodeCancelsAndJoinsUtility(t *testing.T) {
	plan, node, calls := githubRunnerRuntimeFixture(t, "[ \"$1\" = rm ] && exit 0\nprintf started > started\nwhile :; do sleep 1; done\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunGitHubNode(ctx, plan, node.ID, GitHubStepOptions{}) }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	started := false
	for !started {
		select {
		case err := <-done:
			t.Fatalf("utility ended before cancellation: %v", err)
		case <-deadline.C:
			t.Fatal("utility did not start")
		case <-ticker.C:
			_, err := os.Stat("started")
			started = err == nil
		}
	}
	cancel()
	select {
	case err := <-done:
		assertFactoryErrorCode(t, err, e.CodeTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("utility did not join after cancellation")
	}
	raw, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(raw), "CALL\nrm\n-f\n") {
		t.Fatalf("container cleanup missing: %s %v", raw, err)
	}
}

func TestVerifyPlanDigestRejectsWrongSelection(t *testing.T) {
	plan := githubTestPlan(t)
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{digest, "", "sha256:" + strings.Repeat("0", 64)} {
		err := VerifyPlanDigest(plan, expected)
		if expected == digest {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		}
	}
	assertFactoryErrorCode(t, VerifyPlanDigest(nil, digest), e.CodeUsage)
}

func TestRunGitHubNodeUtilityIgnoresClosedDiagnosticStreams(t *testing.T) {
	for _, status := range []int{0, 22} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			plan, node, _ := githubRunnerRuntimeFixture(t, fmt.Sprintf("[ \"$1\" = rm ] && exit 0\nprintf 'stdout\\n'\nprintf 'stderr\\n' >&2\nexit %d\n", status))
			err := RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{Stdout: githubBrokenLog{}, Stderr: githubBrokenLog{}})
			if got := GitHubStepExitCode(err); got != status {
				t.Fatalf("closed logs changed exit: got%d want%d: %v", got, status, err)
			}
		})
	}
}

func TestGitHubStepExitCodeKeepsPublicMapping(t *testing.T) {
	if GitHubStepExitCode(nil) != 0 {
		t.Fatal("nil error did not succeed")
	}
	for _, code := range e.AllCodes {
		err := e.New(code, "ordinary coded failure")
		if GitHubStepExitCode(err) != e.ExitCode(err) {
			t.Fatalf("public exit changed for %s", code)
		}
	}
	if GitHubStepExitCode(fmt.Errorf("ordinary failure")) != 1 {
		t.Fatal("generic exit fallback changed")
	}
}

func TestGitHubUtilityContextUsesStageBoundAndEarlierDeadline(t *testing.T) {
	plan := githubTestPlan(t)
	var node Node
	for _, candidate := range plan.Graph.Nodes {
		if candidate.Kind == NodePackage && candidate.Container != "" {
			node = candidate
			break
		}
	}
	for _, earlier := range []bool{false, true} {
		t.Run(fmt.Sprint(earlier), func(t *testing.T) {
			parent := t.Context()
			if earlier {
				var cancel context.CancelFunc
				parent, cancel = context.WithTimeout(parent, 100*time.Millisecond)
				defer cancel()
			}
			start := time.Now()
			child, cancel, err := githubUtilityContext(parent, plan, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			deadline, ok := child.Deadline()
			if !ok {
				t.Fatal("hosted utility has no deadline")
			}
			if earlier {
				want, _ := parent.Deadline()
				if !deadline.Equal(want) {
					t.Fatal("stage bound extended caller deadline")
				}
				<-child.Done()
				assertFactoryErrorCode(t, githubUtilityError(child, child.Err()), e.CodeTimeout)
			} else if delta := deadline.Sub(start); delta < 59*time.Minute || delta > 60*time.Minute+time.Second {
				t.Fatalf("unexpected stage bound: %s", delta)
			}
		})
	}
}

func TestPublicationUtilityCarriesOnlyPublicRunIdentity(t *testing.T) {
	plan, node, calls := githubRunnerRuntimeFixture(t, "exit 0\n")
	location := utilityRunnerLocation{runtime: plan.Environment.RuntimeBinary(), mountSource: "/fixture", mountTarget: "/workspace", helper: "/workspace/nkd-exec", plan: FactoryPlanPath, publicationRunContext: true}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_TOKEN", "must-not-cross")
	if err := executeUtilityNode(t.Context(), node, location, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GITHUB_ACTIONS", "GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_WORKFLOW_REF", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"} {
		if !strings.Contains(string(raw), "-e\n"+key+"\n") {
			t.Fatalf("missing run identity %s", key)
		}
	}
	for _, forbidden := range []string{"GITHUB_TOKEN", "ACTIONS_ID_TOKEN", sourceTokenEnv, "NKD_CREDENTIAL", "must-not-cross"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("credential crossed utility boundary: %s", forbidden)
		}
	}
}
