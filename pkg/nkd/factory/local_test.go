// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type recordingLocalSessionCloser struct {
	order *[]string
}

func (closer recordingLocalSessionCloser) Close() {
	*closer.order = append(*closer.order, "session")
}

func TestFinishLocalRunOrdersFailureCleanupAndRetainsSuccessfulPayload(t *testing.T) {
	for _, test := range []struct {
		name      string
		completed bool
		wantOrder []string
		closed    bool
	}{
		{name: "failure closes payload before session", wantOrder: []string{"payload", "session"}, closed: true},
		{name: "success retains payload", completed: true, wantOrder: []string{"session"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			order := []string{}
			payload := &fakeDiskProducerResult{onClose: func() { order = append(order, "payload") }}
			result := &LocalRunResult{}
			if err := result.recordDiskCompletion(diskProducerCompletion{
				NodeID: "build-image", Payload: fakeDiskPayload(payload),
			}); err != nil {
				t.Fatal(err)
			}
			cause := e.New(e.CodeBuildFailed, "build sentinel")
			if err := finishLocalRun(result, recordingLocalSessionCloser{order: &order}, test.completed, cause); !errors.Is(err, cause) {
				t.Fatalf("finish error = %v, want original cause", err)
			}
			if strings.Join(order, ",") != strings.Join(test.wantOrder, ",") || payload.closed != test.closed {
				t.Fatalf("cleanup order=%v closed=%v, want order=%v closed=%v", order, payload.closed, test.wantOrder, test.closed)
			}
			if test.completed {
				if err := result.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRunLocalDryRunIsCompleteAndPerformsNoIO(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := githubTestPlan(t)
	var output bytes.Buffer
	result, err := RunLocal(context.Background(), plan, LocalRunOptions{
		Workspace: ".nkd/factory-dry-run", DryRun: true, Publish: false, Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != len(plan.Graph.Nodes) {
		t.Fatalf("dry-run nodes = %d, want %d", len(result.Nodes), len(plan.Graph.Nodes))
	}
	if !strings.Contains(output.String(), "[skipped] publish/release") ||
		!strings.Contains(output.String(), "package/worker") {
		t.Fatalf("dry-run did not expose packaging and opt-in publication:\n%s", output.String())
	}
	if _, err := os.Lstat(".nkd/factory-dry-run"); !os.IsNotExist(err) {
		t.Fatalf("dry-run created workspace: %v", err)
	}
}

func TestRunLocalRejectsUnsafeWorkspaceBeforeDependencies(t *testing.T) {
	plan := githubTestPlan(t)
	_, err := RunLocal(context.Background(), plan, LocalRunOptions{Workspace: t.TempDir(), DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "repository-relative child of .nkd") {
		t.Fatalf("unsafe workspace error = %v", err)
	}
}

func TestLocalRunnerIsRerunnableAndPreservesSourceCache(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	plan := githubTestPlan(t)
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/factory-test", Jobs: 3})
	deps := localRunDeps{
		prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
			return map[string]string{"linux/amd64": "fake"}, nil
		},
		executeNode: func(_ context.Context, plan *Plan, node Node, options LocalRunOptions, _ map[string]string) error {
			_, artifacts := planIndex(plan)
			for _, output := range node.Outputs {
				path, err := relocatedFactoryPath(artifacts[output].Path, options.Workspace)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(node.ID), 0o644); err != nil {
					return err
				}
			}
			return nil
		},
	}
	for run := 0; run < 2; run++ {
		if run == 1 {
			cache := filepath.Join(options.Workspace, "worker", "src", "cache-sentinel")
			if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			canary := filepath.Join(root, "outside-canary")
			if err := os.WriteFile(canary, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			packagePath := filepath.Join(options.Workspace, "packages", "worker")
			if err := os.Remove(packagePath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(canary, packagePath); err != nil {
				t.Fatal(err)
			}
		}
		result, err := runLocal(context.Background(), plan, options, deps)
		if err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
		if len(result.Nodes) != len(plan.Graph.Nodes) {
			t.Fatalf("run %d nodes = %d, want %d", run+1, len(result.Nodes), len(plan.Graph.Nodes))
		}
	}
	cache := filepath.Join(options.Workspace, "worker", "src", "cache-sentinel")
	if raw, err := os.ReadFile(cache); err != nil || string(raw) != "preserve" {
		t.Fatalf("source cache was not preserved: %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(options.Workspace, "run.json")); err != nil {
		t.Fatalf("run record missing: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "outside-canary")); err != nil || string(raw) != "outside" {
		t.Fatalf("rerun followed a derived-output symlink: %q, %v", raw, err)
	}
}

func TestLocalSchedulerOrdersNodesAndJoinsCancellation(t *testing.T) {
	for _, mode := range []string{"success", "failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			fail := mode != "success"
			t.Chdir(t.TempDir())
			base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
			if err != nil {
				t.Fatal(err)
			}
			stack, err := catalog.ApplyStack(base)
			if err != nil {
				t.Fatal(err)
			}
			definition, environment := fixtureInputs("schedule", "first", "")
			definition.Outputs[0].Components = []string{"kubectl"}
			sibling := definition.Outputs[0]
			sibling.Name = "second"
			definition.Outputs = append(definition.Outputs, sibling)
			plan, err := Compile(context.Background(), definition, environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			var failureNode string
			for _, node := range topologicalFactoryNodes(plan.Graph) {
				if node.Kind == NodeFetch {
					if failureNode != "" {
						failureNode = node.ID
						break
					}
					failureNode = node.ID
				}
			}
			var mu sync.Mutex
			completed := map[string]string{}
			active, peak := 0, 0
			both := make(chan struct{})
			var readyOnce sync.Once
			joined := 0
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			deps := localRunDeps{
				prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) { return nil, nil },
				executeNode: func(ctx context.Context, plan *Plan, node Node, _ LocalRunOptions, _ map[string]string) error {
					mu.Lock()
					if dependency := failedLocalDependency(plan.Graph, node, completed); dependency != "" {
						t.Errorf("%s started before %s", node.ID, dependency)
					}
					active++
					if active > peak {
						peak = active
					}
					if node.Kind == NodeFetch && active == 2 {
						readyOnce.Do(func() { close(both) })
					}
					mu.Unlock()
					defer func() { mu.Lock(); active--; joined++; mu.Unlock() }()
					if node.Kind == NodeFetch {
						select {
						case <-both:
						case <-ctx.Done():
							return ctx.Err()
						}
						if fail {
							if node.ID == failureNode {
								if mode == "canceled" {
									cancel()
									return ctx.Err()
								}
								return e.New(e.CodeVerifyFailed, "injected producer failure")
							}
							<-ctx.Done()
							return ctx.Err()
						}
					}
					mu.Lock()
					completed[node.ID] = "ok"
					mu.Unlock()
					return nil
				},
			}
			result, err := runLocal(ctx, plan, withLocalDefaults(LocalRunOptions{Workspace: ".nkd/schedule", Jobs: 2}), deps)
			if fail {
				want := e.CodeVerifyFailed
				if mode == "canceled" {
					want = e.CodeTimeout
				}
				assertFactoryErrorCode(t, err, want)
				if joined != 2 || active != 0 {
					t.Fatalf("returned before joining canceled producers: joined=%d active=%d", joined, active)
				}
				if _, err := os.Stat(".nkd/schedule/run.json"); !os.IsNotExist(err) {
					t.Fatalf("failed Run persisted success: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Nodes) != len(plan.Graph.Nodes) {
					t.Fatal("scheduler omitted nodes")
				}
			}
			if peak != 2 {
				t.Fatalf("peak concurrency=%d, want 2", peak)
			}
		})
	}
}
