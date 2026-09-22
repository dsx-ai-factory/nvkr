package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

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

func TestLocalRunRejectsUnsafeCallerGraphBeforeWork(t *testing.T) {
	for name, mutate := range map[string]func(*ir.Node){
		"tagged toolchain": func(node *ir.Node) { node.Toolchain = "ubuntu:latest" },
		"networked build":  func(node *ir.Node) { node.Network = ir.NetworkDefault },
	} {
		t.Run(name, func(t *testing.T) {
			graph := sampleGraph()
			mutate(&graph.Nodes[0])
			if _, err := graph.Digest(); err == nil {
				t.Fatal("unsafe graph gained a build digest")
			}
			var output bytes.Buffer
			report, err := (Local{}).Run(graph, "sha256:"+repeatHex("f"), RunOptions{Ctx: context.Background(), DryRun: true, Stdout: &output})
			if err == nil || output.Len() != 0 || report.Qualified() || len(report.Nodes) != 0 {
				t.Fatalf("unsafe graph crossed local execution seam: report=%+v output=%q err=%v", report, output.String(), err)
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
	graph := testSourceGraph(fetch, build)
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
	graph := testSourceGraph(fetch, build)
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
