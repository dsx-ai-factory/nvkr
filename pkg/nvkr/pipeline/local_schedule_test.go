// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Failure policy changes which ready nodes may start, but never permits Run to
// return before work already in flight has finished using its inputs.
func TestRunSchedulerFailurePolicyJoinsStartedWork(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keepGoing bool
	}{
		{name: "stop new work"},
		{name: "keep going", keepGoing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			transport := &scheduleFailureTransport{
				slowStarted: make(chan struct{}), releaseSlow: make(chan struct{}),
				slowDone: make(chan struct{}), laterStarted: make(chan struct{}),
			}
			progress := &scheduleFailureProgress{failed: make(chan struct{})}
			graph := &ir.Graph{SchemaVersion: ir.SchemaVersion, Platform: "linux/amd64"}
			verify := ir.Node{ID: "verify", Kind: ir.KindVerify}
			for _, name := range []string{"a-fail", "b-slow", "c-later"} {
				id := "pull-" + name
				graph.Nodes = append(graph.Nodes, ir.Node{
					ID: id, Kind: ir.KindPull, Component: name, OutputType: "oci-image",
					Network: ir.NetworkDefault, Platform: "linux/amd64",
					Artifact: "registry.example/" + name, Digest: sha256Of([]byte(name)),
					Version: "1.0.0",
				})
				verify.Needs = append(verify.Needs, id)
			}
			graph.Nodes = append(graph.Nodes, verify)
			digest := mustGraphDigest(t, graph)
			type result struct {
				report *Report
				err    error
			}
			results := make(chan result, 1)
			done := make(chan struct{})
			root := filepath.Join(t.TempDir(), "src")
			go func() {
				report, err := (Local{}).Run(graph, digest, RunOptions{
					Ctx: ctx, SourceRoot: root, Jobs: 2, KeepGoing: tc.keepGoing,
					OCI: transport, Stdout: progress,
				})
				results <- result{report: report, err: err}
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("Run did not join its workers during test cleanup")
				}
			})
			select {
			case <-progress.failed:
			case <-ctx.Done():
				t.Fatal("first failure was not reported")
			}
			select {
			case <-done:
				t.Fatal("Run returned while the slow acquisition still held its inputs")
			default:
			}
			if tc.keepGoing {
				select {
				case <-transport.laterStarted:
				case <-ctx.Done():
					t.Fatal("KeepGoing did not use the free slot for remaining ready work")
				}
			}
			close(transport.releaseSlow)
			var got result
			select {
			case got = <-results:
			case <-ctx.Done():
				t.Fatal("Run did not finish after its last blocked acquisition returned")
			}
			if code(got.err) != nvkrerr.CodeFetchFailed || !strings.Contains(got.err.Error(), "first acquisition failed") {
				t.Fatalf("later failures replaced the first coded error: %v", got.err)
			}
			select {
			case <-transport.slowDone:
			default:
				t.Fatal("Run returned before the slow acquisition finished")
			}
			if !tc.keepGoing {
				select {
				case <-transport.laterStarted:
					t.Fatal("Run started queued work after failure without KeepGoing")
				default:
				}
			}
			wantNodes := 2
			if tc.keepGoing {
				wantNodes = 4 // every pull plus terminal verification of missing artifacts
			}
			if len(got.report.Nodes) != wantNodes || len(got.report.Artifacts) != 0 || got.report.Qualified() {
				t.Fatalf("failed execution has unexpected report: %+v", got.report)
			}
		})
	}
}

type scheduleFailureTransport struct {
	slowStarted  chan struct{}
	releaseSlow  chan struct{}
	slowDone     chan struct{}
	laterStarted chan struct{}
}

func (transport *scheduleFailureTransport) Acquire(ctx context.Context, reference, _ string) (string, int64, error) {
	switch {
	case strings.Contains(reference, "/a-fail@"):
		select {
		case <-transport.slowStarted:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
		return "", 0, nvkrerr.New(nvkrerr.CodeFetchFailed, "first acquisition failed")
	case strings.Contains(reference, "/b-slow@"):
		close(transport.slowStarted)
		defer close(transport.slowDone)
		select {
		case <-transport.releaseSlow:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
		return "", 0, nvkrerr.New(nvkrerr.CodeVerifyFailed, "slow acquisition failed")
	default:
		close(transport.laterStarted)
		return "", 0, nvkrerr.New(nvkrerr.CodeVerifyFailed, "later acquisition failed")
	}
}

func (*scheduleFailureTransport) Verify(context.Context, string, string) error {
	return nvkrerr.New(nvkrerr.CodeVerifyFailed, "failed acquisitions cannot verify")
}

type scheduleFailureProgress struct {
	failed chan struct{}
	once   sync.Once
}

func (progress *scheduleFailureProgress) Write(raw []byte) (int, error) {
	if strings.Contains(string(raw), "[failed] pull a-fail ") {
		progress.once.Do(func() { close(progress.failed) })
	}
	return len(raw), nil
}

func graph() []ir.Node {
	return []ir.Node{
		{ID: "verify", Kind: ir.KindVerify, Needs: []string{"build-a", "build-b"}},
		{ID: "build-b", Kind: ir.KindBuild, Component: "b", Needs: []string{"fetch-1"}},
		{ID: "build-a", Kind: ir.KindBuild, Component: "a", Needs: []string{"fetch-1"}},
		{ID: "fetch-1", Kind: ir.KindFetch, Repo: "https://x/k", Commit: "70d3cc986aa8221cd1dfb1121852688902d3bf53", TreeHash: "195abcc8a74b", Dest: "k-70d3cc986aa8"},
	}
}

func TestTopoSortOrderAndDeterminism(t *testing.T) {
	order, err := topoSort(graph())
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, n := range order {
		pos[n.ID] = i
	}
	// every dependency must precede its dependents.
	if !(pos["fetch-1"] < pos["build-a"] && pos["fetch-1"] < pos["build-b"]) {
		t.Errorf("fetch must precede builds: %v", pos)
	}
	if !(pos["build-a"] < pos["verify"] && pos["build-b"] < pos["verify"]) {
		t.Errorf("builds must precede verify: %v", pos)
	}
	// deterministic: ties broken by ID, so build-a precedes build-b.
	if pos["build-a"] > pos["build-b"] {
		t.Errorf("tie-break must be by ID (build-a before build-b): %v", pos)
	}
}

func TestTopoSortCycle(t *testing.T) {
	cyclic := []ir.Node{
		{ID: "x", Kind: ir.KindBuild, Needs: []string{"y"}},
		{ID: "y", Kind: ir.KindBuild, Needs: []string{"x"}},
	}
	if _, err := topoSort(cyclic); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("cycle must be detected, got %v", err)
	}
}

func TestSelectNodesAll(t *testing.T) {
	inc, err := selectNodes(graph(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inc) != 4 {
		t.Fatalf("no filter should include all 4 nodes, got %d", len(inc))
	}
}

func TestSelectNodesOnlyPullsFetchDepAndVerify(t *testing.T) {
	inc, err := selectNodes(graph(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	// build-a + its fetch-1 dep + verify; NOT build-b.
	for _, want := range []string{"build-a", "fetch-1", "verify"} {
		if !inc[want] {
			t.Errorf("expected %s included", want)
		}
	}
	if inc["build-b"] {
		t.Error("build-b must be excluded when --only=a")
	}
}

func TestSelectNodesUnknownComponentFailsClosed(t *testing.T) {
	_, err := selectNodes(graph(), []string{"does-not-exist"})
	if code(err) != nvkrerr.CodeUnsupported {
		t.Fatalf("unknown --only component must fail closed, got %v", err)
	}
}

// The concurrent scheduler must run the whole included DAG and produce a
// deterministic, dependency-ordered report regardless of jobs / completion order.
func TestRunSchedulerConcurrentDeterministic(t *testing.T) {
	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	vendorSpec := "sha256:" + repeatHex("d")
	fetch := testFetchNode("fetch-1", "https://example.test/k", testSourceCommit, testSourceTree,
		"k-source", toolchain, vendorSpec)
	buildA := completeTestBuild(ir.Node{ID: "build-a", Kind: ir.KindBuild, Component: "a", Toolchain: toolchain}, fetch)
	buildB := completeTestBuild(ir.Node{ID: "build-b", Kind: ir.KindBuild, Component: "b", Toolchain: toolchain}, fetch)
	g := testSourceGraph(fetch, buildA, buildB)
	digest := mustGraphDigest(t, g)
	run := func(jobs int) *Report {
		rep, err := Local{}.Run(g, digest, RunOptions{Ctx: context.Background(), Jobs: jobs, DryRun: true})
		if err != nil {
			t.Fatalf("jobs=%d: %v", jobs, err)
		}
		return rep
	}
	serial := run(1)
	parallel := run(4)

	if len(serial.Nodes) != 4 {
		t.Fatalf("expected 4 node results, got %d", len(serial.Nodes))
	}
	// report is dependency-ordered: fetch, then builds, then verify.
	order := make([]string, len(serial.Nodes))
	for i, n := range serial.Nodes {
		order[i] = n.Kind
	}
	want := []string{ir.KindFetch, ir.KindBuild, ir.KindBuild, ir.KindVerify}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("report not phase-ordered: %v", order)
		}
	}
	// serial and parallel runs yield the identical report ordering (determinism).
	for i := range serial.Nodes {
		if serial.Nodes[i].ID != parallel.Nodes[i].ID {
			t.Fatalf("node %d differs by jobs: %q vs %q", i, serial.Nodes[i].ID, parallel.Nodes[i].ID)
		}
	}
}
