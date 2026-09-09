package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

func graph() []ir.Node {
	return []ir.Node{
		{ID: "verify", Kind: ir.KindVerify, Needs: []string{"build-a", "build-b"}},
		{ID: "build-b", Kind: ir.KindBuild, Component: "b", Needs: []string{"fetch-1"}},
		{ID: "build-a", Kind: ir.KindBuild, Component: "a", Needs: []string{"fetch-1"}},
		{ID: "fetch-1", Kind: ir.KindFetch, Repo: "https://x/k", Commit: "70d3cc986aa8221cd1dfb1121852688902d3bf53", TreeHash: "195abcc8a74b", Dest: "k-70d3cc986aa8"},
	}
}

func TestPublicRunnerEntryPointsRejectNilContextAtAdmission(t *testing.T) {
	qualified := RunOptions{Qualification: &QualifiedExecution{}}
	if _, err := (Local{}).Run(nil, "", qualified); code(err) != nkderr.CodeUsage {
		t.Fatalf("Run accepted a nil context or inspected its graph first: %v", err)
	}
	if _, err := (Local{}).VerifyResults(nil, "", "missing", qualified); code(err) != nkderr.CodeUsage {
		t.Fatalf("VerifyResults accepted a nil context or inspected its inputs first: %v", err)
	}
	if _, err := (Local{}).ExecNode(ir.Node{}, RunOptions{}); code(err) != nkderr.CodeUsage {
		t.Fatalf("ExecNode accepted a nil context or inspected its node first: %v", err)
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
	if _, err := topoSort(cyclic); code(err) != nkderr.CodeInvalidCatalog {
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
	if code(err) != nkderr.CodeUnsupported {
		t.Fatalf("unknown --only component must fail closed, got %v", err)
	}
}

// verify must be authoritative about the current run: an artifact that exists but
// was not produced this run (no collected entry) fails closed, and a recorded
// artifact whose bytes changed fails closed. This guards the stale-artifact /
// --keep-going gap and the "presence != verification" gap.
func TestVerifyRunAuthoritative(t *testing.T) {
	root := t.TempDir()
	opts := withDefaults(RunOptions{SourceRoot: filepath.Join(root, "work", "src")})
	producer := ir.Node{ID: "build-kubectl", Kind: ir.KindBuild, Component: "kubectl",
		OutputType: "host-package", WriteDir: "out", ArtifactPath: "out/kubectl"}
	full := artifactHostPath(opts, producer)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("compiled-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _, err := sha256File(full)
	if err != nil {
		t.Fatal(err)
	}

	builds := map[string]ir.Node{
		producer.ID: producer,
	}
	included := map[string]bool{"build-kubectl": true, "verify": true}
	l := Local{}

	// happy path: collected records the true digest + the file it hashed.
	good := []ArtifactResult{{Component: "kubectl", SHA256: sum, AbsPath: full}}
	if err := l.verify(ir.Node{Kind: ir.KindVerify}, builds, included, good, opts); err != nil {
		t.Fatalf("valid artifact should verify: %v", err)
	}

	// stale artifact: file exists but this run produced no collected entry -> fail.
	if err := l.verify(ir.Node{Kind: ir.KindVerify}, builds, included, nil, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("missing-from-run artifact must fail closed, got %v", err)
	}

	// tampered/wrong content: recorded digest no longer matches the file -> fail.
	wrong := []ArtifactResult{{Component: "kubectl", SHA256: "sha256:deadbeef", AbsPath: full}}
	if err := l.verify(ir.Node{Kind: ir.KindVerify}, builds, included, wrong, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("digest mismatch must fail closed, got %v", err)
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
	g := testSourceGraph("t", fetch, buildA, buildB)
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

func TestLocalRunAndLoweringRejectUnsafeCallerGraphBeforeWork(t *testing.T) {
	for name, mutate := range map[string]func(*ir.Node){
		"tagged toolchain": func(node *ir.Node) { node.Toolchain = "ubuntu:latest" },
		"networked build":  func(node *ir.Node) { node.Network = ir.NetworkDefault },
	} {
		t.Run(name, func(t *testing.T) {
			graph := sampleGraph()
			mutate(&graph.Nodes[0])
			if _, err := graph.Digest(); err == nil {
				t.Fatal("unsafe graph gained a recipe digest")
			}
			var output bytes.Buffer
			report, err := (Local{}).Run(graph, "sha256:"+repeatHex("f"), RunOptions{Ctx: context.Background(), DryRun: true, Stdout: &output})
			if err == nil || output.Len() != 0 || report.Qualified() || len(report.Nodes) != 0 {
				t.Fatalf("unsafe graph crossed local execution seam: report=%+v output=%q err=%v", report, output.String(), err)
			}
			if jobs, err := LowerJobs(graph); err == nil || len(jobs) != 0 {
				t.Fatalf("unsafe graph crossed lowering seam: jobs=%+v err=%v", jobs, err)
			}
		})
	}
}

func TestLocalRunCancellationStopsGitFetch(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	fetch := testFetchNode("fetch-cancel", "https://example.test/source", testSourceCommit, testSourceTree,
		"source", toolchain, "sha256:"+repeatHex("d"))
	build := completeTestBuild(ir.Node{ID: "build-cancel", Kind: ir.KindBuild, Component: "cancel", Toolchain: toolchain}, fetch)
	graph := testSourceGraph("cancel-fetch", fetch, build)
	digest := mustGraphDigest(t, graph)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	started := time.Now()
	_, err := (Local{}).Run(graph, digest, RunOptions{
		Ctx: ctx, SourceRoot: filepath.Join(t.TempDir(), "work", "src"), Stdout: &output,
	})
	if code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled fetch returned %v", err)
	}
	if !strings.Contains(output.String(), "[start] fetch") {
		t.Fatalf("slow fetch was silent until completion: %q", output.String())
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("git fetch ignored cancellation for %s", elapsed)
	}
}

func TestLocalDryRunKeepsLifecycleProgressQuiet(t *testing.T) {
	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	fetch := testFetchNode("fetch-plan", "https://example.test/source", testSourceCommit, testSourceTree,
		"source", toolchain, "sha256:"+repeatHex("d"))
	build := completeTestBuild(ir.Node{ID: "build-plan", Kind: ir.KindBuild, Component: "plan", Toolchain: toolchain}, fetch)
	graph := testSourceGraph("quiet-plan", fetch, build)
	digest := mustGraphDigest(t, graph)
	var output bytes.Buffer
	if _, err := (Local{}).Run(graph, digest, RunOptions{
		Ctx: context.Background(), DryRun: true, Stdout: &output,
	}); err != nil {
		t.Fatal(err)
	}
	for _, noisy := range []string{"[start]", "[container]", "[source]", "[done]"} {
		if strings.Contains(output.String(), noisy) {
			t.Fatalf("dry-run plan contains live lifecycle marker %q: %s", noisy, output.String())
		}
	}
}

func TestMapCancellationPreservesRetryableTimeoutContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := mapCancellation(ctx, nkderr.New(nkderr.CodeBuildFailed, "semantic wrapper"), "build")
	if code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("cancellation was not retained as a retryable timeout: %v", err)
	}
}

func TestReaperCloseStopsWaiterAndIsConcurrentIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reaper := newReaper(ctx, "unused-runtime")

	const closers = 32
	var wait sync.WaitGroup
	wait.Add(closers)
	for range closers {
		go func() {
			defer wait.Done()
			reaper.close()
		}()
	}
	wait.Wait()

	select {
	case <-reaper.done:
	default:
		t.Fatal("reaper close returned before its context waiter exited")
	}
	if reaper.add("late-container") {
		t.Fatal("closed reaper accepted a container that can no longer be cleaned")
	}
}

func TestReaperContextCancellationStopsWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reaper := newReaper(ctx, "unused-runtime")
	cancel()
	select {
	case <-reaper.done:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not stop the reaper waiter")
	}
	// Joining an already-canceled waiter remains idempotent.
	reaper.close()
}

func TestLocalRunAlwaysClosesReaperWithoutCancelingCallerContext(t *testing.T) {
	for _, test := range []struct {
		name      string
		badDigest bool
	}{
		{name: "success"},
		{name: "early-error", badDigest: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reaper := newReaper(ctx, "unused-runtime")
			graph := sampleGraph()
			digest := mustGraphDigest(t, graph)
			if test.badDigest {
				digest = "sha256:" + repeatHex("f")
			}

			_, err := (Local{}).Run(graph, digest, RunOptions{
				Ctx: ctx, DryRun: true, reap: reaper,
			})
			if test.badDigest && err == nil {
				t.Fatal("invalid graph identity unexpectedly succeeded")
			}
			if !test.badDigest && err != nil {
				t.Fatal(err)
			}
			select {
			case <-reaper.done:
			default:
				t.Fatal("Local.Run returned while its reaper waiter was still live")
			}
		})
	}
}

func TestReaperCleanupIsBoundedAndDoesNotHoldRegistryLock(t *testing.T) {
	reaper := newReaper(context.Background(), "unused-runtime")
	reaper.cleanupTimeout = 200 * time.Millisecond
	type cleanupCall struct {
		started time.Time
		names   []string
	}
	cleanupEntered := make(chan cleanupCall, 2)
	reaper.cleanupCommand = func(ctx context.Context, grace time.Duration, _ string,
		_ *heldRuntimeExecutable, names []string,
	) error {
		cleanupEntered <- cleanupCall{started: time.Now(), names: append([]string(nil), names...)}
		<-ctx.Done()
		// Model a child that ignores SIGTERM until WaitDelay escalates. The reaper
		// must reserve this grace inside, rather than after, its shared deadline.
		timer := time.NewTimer(grace)
		defer timer.Stop()
		<-timer.C
		return ctx.Err()
	}
	if !reaper.add("blocked-container") {
		t.Fatal("open reaper rejected a container")
	}
	if !reaper.add("later-container") {
		t.Fatal("open reaper rejected a second container")
	}
	closed := make(chan struct{})
	go func() {
		reaper.close()
		close(closed)
	}()
	calls := make([]cleanupCall, 0, 2)
	for range 2 {
		select {
		case call := <-cleanupEntered:
			calls = append(calls, call)
		case <-time.After(2 * time.Second):
			t.Fatal("not every registered container received a cleanup attempt")
		}
	}
	cleanupStarted := calls[0].started
	gotNames := []string{calls[0].names[0], calls[1].names[0]}
	sort.Strings(gotNames)
	if got := strings.Join(gotNames, ","); got != "blocked-container,later-container" {
		t.Fatalf("cleanup did not attempt the complete registered set: %q", got)
	}

	registryOperation := make(chan bool, 1)
	go func() {
		reaper.forget("blocked-container")
		registryOperation <- reaper.add("too-late")
	}()
	select {
	case accepted := <-registryOperation:
		if accepted {
			t.Fatal("reaper accepted a container after cleanup began")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cleanup held the registry mutex across subprocess I/O")
	}

	select {
	case <-closed:
		if elapsed := time.Since(cleanupStarted); elapsed > 450*time.Millisecond {
			t.Fatalf("cleanup exceeded its shared deadline: %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("unresponsive cleanup boundary made reaper.close hang")
	}
}

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}

func TestQualifiedPackagingFailsBeforeGraphOrPathIO(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "must-remain-missing")
	_, err := (Local{}).Run(nil, "", RunOptions{
		Ctx: context.Background(), SourceRoot: missingRoot, Package: true,
		Qualification: &QualifiedExecution{},
	})
	if code(err) != nkderr.CodeEngineCapability {
		t.Fatalf("qualified packaging error = %v, want engine-capability", err)
	}
	if _, statErr := os.Stat(missingRoot); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported qualified packaging touched its source root: %v", statErr)
	}
}
