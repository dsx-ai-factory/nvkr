// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The ordinary-operation contract harness drives every maintained entry that
// executes complete-Plan operations from one fixture: the execute-node helper,
// hosted GitHub run-node steps and local scheduling. A recording container
// runtime first on PATH makes utility launches observable without Docker. The
// tables freeze each entry's admission codes, route, emitted bytes and exact
// output membership, error and cancellation behavior, and the no-op dry-run;
// every case also requires an unchanged caller Plan identity. Committed
// generated bundle bytes remain frozen by `make workflows-check`.

type operationEntry struct {
	name string
	run  func(context.Context, *Plan, string, io.Writer) error
}

var operationEntries = []operationEntry{
	{name: "execute-node", run: func(ctx context.Context, plan *Plan, id string, log io.Writer) error {
		return ExecuteNode(ctx, plan, id, log, log)
	}},
	{name: "github run-node", run: func(ctx context.Context, plan *Plan, id string, log io.Writer) error {
		return RunGitHubNode(ctx, plan, id, GitHubStepOptions{Stdout: log, Stderr: log})
	}},
	{name: "local", run: func(ctx context.Context, plan *Plan, id string, log io.Writer) error {
		options := withLocalDefaults(LocalRunOptions{Stdout: log, Stderr: log})
		return executeLocalFactoryNode(ctx, plan, planNode(plan, id), options, map[string]string{"linux/amd64": "runner"})
	}},
}

// newOperationHarness isolates the working directory and installs a recording
// runtime. The script tail runs after argv is recorded. No worker case may
// reach the generation seam.
func newOperationHarness(t *testing.T, script string) (*Plan, string) {
	t.Helper()
	forbidGenerationSeam(t)
	plan := githubTestPlan(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "runtime-calls")
	// Shadow both maintained runtimes: hosted steps use the Environment's, local
	// scheduling defaults to docker. No case may reach a real daemon.
	for _, name := range []string{plan.Environment.RuntimeBinary(), "docker"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s\\n' CALL \"$@\" >> \"$NVKR_TEST_RUNTIME_LOG\"\n"+preparedUtilityRuntimeProtocol(planNode(plan, "package/worker"), name)+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NVKR_TEST_RUNTIME_LOG", calls)
	t.Setenv(sourceTokenEnv, "")
	t.Chdir(t.TempDir())
	return plan, calls
}

// writeOperationFixture supplies the hosted Plan carrier and exact producer
// bytes and records for the worker terminal observation.
func writeOperationFixture(t *testing.T, plan *Plan) {
	t.Helper()
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), FactoryPlanPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	terminal := planNode(plan, "worker/terminal-observe")
	for _, id := range terminal.Inputs {
		artifact := artifacts[id]
		if artifact.Kind == "producer-result" {
			continue
		}
		if err := writeFactoryFile(t.Context(), artifact.Path, []byte("fixture for "+id), 0o755); err != nil {
			t.Fatal(err)
		}
		writeProducerRecordFixture(t, plan, artifact, artifacts["result/worker/"+artifact.Component].Path)
	}
}

func operationFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		files[filepath.ToSlash(path)] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func operationPlanDigest(t *testing.T, plan *Plan) string {
	t.Helper()
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestOrdinaryOperationEntriesAdmitCompletePlanBeforeEffects(t *testing.T) {
	entries := append([]operationEntry{}, operationEntries[:2]...)
	// Local scheduling admits the complete Run; it has no node selector.
	entries = append(entries, operationEntry{name: "local run", run: func(ctx context.Context, plan *Plan, _ string, log io.Writer) error {
		_, err := RunLocal(ctx, plan, LocalRunOptions{Stdout: log, Stderr: log})
		return err
	}})
	for _, entry := range entries {
		for _, test := range []struct {
			name string
			want e.Code
		}{
			{"nil-context", e.CodeUsage},
			{"canceled", e.CodeTimeout},
			{"nil-plan", e.CodeUsage},
			{"unknown-node", e.CodeInvalidCatalog},
			{"changed-sibling", e.CodeInvalidCatalog},
		} {
			if entry.name == "local run" && test.name == "unknown-node" {
				continue
			}
			t.Run(entry.name+"/"+test.name, func(t *testing.T) {
				plan, calls := newOperationHarness(t, "exit 0\n")
				ctx, id := t.Context(), "worker/terminal-observe"
				switch test.name {
				case "nil-context":
					ctx = nil
				case "canceled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				case "nil-plan":
					plan = nil
				case "unknown-node":
					id = "absent"
				case "changed-sibling":
					plan.Graph.Nodes[len(plan.Graph.Nodes)-1].Outputs = []string{"invented"}
				}
				var log bytes.Buffer
				assertFactoryErrorCode(t, entry.run(ctx, plan, id, &log), test.want)
				if log.Len() != 0 || len(operationFiles(t)) != 0 {
					t.Fatalf("refused entry caused effects: log=%q files=%v", &log, operationFiles(t))
				}
				if _, err := os.Stat(calls); !os.IsNotExist(err) {
					t.Fatal("refused entry launched a utility container")
				}
			})
		}
	}
}

var producedOutput = regexp.MustCompile(`Produced: (\S+)`)

func TestOrdinaryOperationEntriesShareRoutesBytesAndMembership(t *testing.T) {
	// Route inventory: an in-process operation writes its outputs here; a
	// contained one launches the same execute-node helper in its pinned image.
	routes := map[string]map[string]string{
		"worker/terminal-observe": {"execute-node": "in-process", "github run-node": "contained", "local": "in-process"},
		"package/worker":          {"execute-node": "in-process", "github run-node": "contained", "local": "contained"},
	}
	for _, id := range sortedKeys(routes) {
		observed := map[string]string{}
		for _, entry := range operationEntries {
			t.Run(id+"/"+entry.name, func(t *testing.T) {
				plan, calls := newOperationHarness(t, "exit 0\n")
				writeOperationFixture(t, plan)
				if id == "package/worker" {
					if err := ExecuteNode(t.Context(), plan, "worker/terminal-observe", nil, nil); err != nil {
						t.Fatal(err)
					}
				}
				node, digest := planNode(plan, id), operationPlanDigest(t, plan)
				_, artifacts := planIndex(plan)
				before := operationFiles(t)
				var log bytes.Buffer
				if err := entry.run(t.Context(), plan, id, &log); err != nil {
					t.Fatalf("%v\n%s", err, &log)
				}
				if operationPlanDigest(t, plan) != digest || plan.Qualification != TierStaticUnqualified {
					t.Fatal("entry changed the caller Plan identity or lane")
				}
				after := operationFiles(t)
				var written []string
				for path, content := range after {
					if before[path] != content {
						written = append(written, path)
					}
				}
				sort.Strings(written)
				launched, err := os.ReadFile(calls)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				route := "in-process"
				if len(launched) != 0 {
					route = "contained"
				}
				if route != routes[id][entry.name] {
					t.Fatalf("route = %s, want %s", route, routes[id][entry.name])
				}
				if route == "contained" {
					for _, want := range []string{"factory\nexecute-node\n", "--node\n" + id + "\n", "--workspace\n.nvkr/factory\n", "CALL\nrm\n-f\n"} {
						if !strings.Contains(string(launched), want) {
							t.Fatalf("launch omits %q: %s", want, launched)
						}
					}
					if hosted := strings.Contains(string(launched), "--plan-digest\n"+digest+"\n"); hosted != (entry.name == "github run-node") {
						t.Fatalf("plan digest binding = %t for %s: %s", hosted, entry.name, launched)
					}
					if len(written) != 0 {
						t.Fatalf("contained route wrote host files %v", written)
					}
				} else {
					// Exact membership: every write lies inside one of this node's
					// outputs, and every output was written.
					covered := map[string]bool{}
					for _, path := range written {
						owner := ""
						for _, output := range node.Outputs {
							if root := artifacts[output].Path; path == root || strings.HasPrefix(path, root+"/") {
								owner = output
							}
						}
						if owner == "" {
							t.Fatalf("operation wrote %s outside its outputs %v", path, node.Outputs)
						}
						covered[owner] = true
					}
					if len(covered) != len(node.Outputs) {
						t.Fatalf("operation wrote %v, want every output of %v", written, node.Outputs)
					}
					observed[entry.name] = after[artifacts[node.Outputs[0]].Path]
				}
				if entry.name == "github run-node" {
					var produced []string
					for _, match := range producedOutput.FindAllStringSubmatch(log.String(), -1) {
						produced = append(produced, match[1])
					}
					if strings.Join(produced, ",") != strings.Join(node.Outputs, ",") {
						t.Fatalf("produced = %v, want %v", produced, node.Outputs)
					}
				}
			})
		}
		if id == "worker/terminal-observe" && observed["execute-node"] != observed["local"] {
			t.Fatal("helper and local terminal observations differ")
		}
	}
}

func TestOrdinaryOperationEntriesPreservePrimaryError(t *testing.T) {
	// In-process routes fail on a missing producer payload; contained routes
	// fail through a child exiting 22. Local scheduling later classifies the
	// uncoded local child status as build-failed in runLocalNode.
	for _, test := range []struct {
		entry, id string
		contained bool
		want      e.Code // empty: the raw child status is the uncoded cause
	}{
		{entry: "execute-node", id: "worker/terminal-observe", want: e.CodeVerifyFailed},
		{entry: "local", id: "worker/terminal-observe", want: e.CodeVerifyFailed},
		{entry: "github run-node", id: "package/worker", contained: true, want: e.CodeVerifyFailed},
		{entry: "local", id: "package/worker", contained: true},
	} {
		t.Run(test.entry+"/"+test.id, func(t *testing.T) {
			plan, _ := newOperationHarness(t, "[ \"$1\" = rm ] && exit 0\nexit 22\n")
			writeOperationFixture(t, plan)
			_, artifacts := planIndex(plan)
			terminal := planNode(plan, "worker/terminal-observe")
			if err := os.Remove(artifacts[terminal.Inputs[0]].Path); err != nil {
				t.Fatal(err)
			}
			var entry operationEntry
			for _, candidate := range operationEntries {
				if candidate.name == test.entry {
					entry = candidate
				}
			}
			err := entry.run(t.Context(), plan, test.id, io.Discard)
			if test.want != "" {
				assertFactoryErrorCode(t, err, test.want)
			} else if factoryErrorCode(err) != "" {
				t.Fatalf("raw child failure gained a code before scheduling: %v", err)
			}
			if test.contained {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 22 {
					t.Fatalf("child status not preserved: %v", err)
				}
				if test.entry == "github run-node" && GitHubStepExitCode(err) != 22 {
					t.Fatalf("hosted exit status = %d", GitHubStepExitCode(err))
				}
			} else if _, err := os.Stat(artifacts[terminal.Outputs[0]].Path); !os.IsNotExist(err) {
				t.Fatal("failed observation persisted an output")
			}
		})
	}
}

func TestOrdinaryOperationContainedRoutesCancelAndJoin(t *testing.T) {
	for _, entry := range operationEntries[1:] {
		t.Run(entry.name, func(t *testing.T) {
			plan, calls := newOperationHarness(t, "[ \"$1\" = rm ] && exit 0\nprintf started > \"$NVKR_TEST_RUNTIME_LOG.started\"\nwhile :; do sleep 1; done\n")
			writeOperationFixture(t, plan)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- entry.run(ctx, plan, "package/worker", io.Discard) }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(calls + ".started"); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("utility did not start")
				}
				time.Sleep(5 * time.Millisecond)
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
		})
	}
}

func TestOrdinaryOperationLocalDryRunIsCompleteAndInert(t *testing.T) {
	plan, calls := newOperationHarness(t, "exit 0\n")
	digest := operationPlanDigest(t, plan)
	result, err := RunLocal(t.Context(), plan, LocalRunOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	if result.Qualification != TierLocalExecution || result.PlanDigest != digest || operationPlanDigest(t, plan) != digest {
		t.Fatalf("dry-run lane or identity changed: %s %s", result.Qualification, result.PlanDigest)
	}
	seen := map[string]int{}
	for _, node := range result.Nodes {
		seen[node.ID]++
		if want := map[bool]string{true: "skipped", false: "dry-run"}[requiresPublication(node.Kind)]; node.Status != want {
			t.Fatalf("%s status = %s, want %s", node.ID, node.Status, want)
		}
	}
	for _, node := range plan.Graph.Nodes {
		if seen[node.ID] != 1 {
			t.Fatalf("%s listed %d times", node.ID, seen[node.ID])
		}
	}
	if len(seen) != len(plan.Graph.Nodes) || len(operationFiles(t)) != 0 {
		t.Fatal("dry-run listed foreign nodes or wrote files")
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("dry-run launched a utility container")
	}
}
