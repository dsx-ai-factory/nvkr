// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	client "github.com/NVIDIA/nke-recipes/client/v1"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func errorCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestResolveBuildEqualsAuthoritativeModules(t *testing.T) {
	ctx := context.Background()
	selection := buildgraph.Selection{Components: sdkFixtureComponents(), Platform: "linux/amd64"}
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")

	wantCatalog, err := catalog.LoadContext(ctx, catalog.Dir(data))
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, err := buildgraph.Resolve(wantCatalog, selection)
	if err != nil {
		t.Fatal(err)
	}
	wantGraph, err := buildgraph.Compile(wantResolved)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := wantGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}

	sdk := client.New(client.WithData(data))
	got, err := sdk.ResolveBuild(ctx, client.ResolveRequest{Components: selection.Components, Platform: selection.Platform})
	if err != nil {
		t.Fatalf("facade ResolveBuild failed: %v", err)
	}
	if !reflect.DeepEqual(got.Resolved, wantResolved) {
		t.Fatalf("facade resolved value differs from buildgraph.Resolve\n got: %#v\nwant: %#v", got.Resolved, wantResolved)
	}
	if !reflect.DeepEqual(got.Graph, wantGraph) || got.BuildDigest != wantDigest {
		t.Fatalf("facade graph/digest differs from buildgraph.Compile: got %s, want %s", got.BuildDigest, wantDigest)
	}

}

func TestResolveBuildCarriesOutputFormatToResolution(t *testing.T) {
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")
	resolution, err := client.New(client.WithData(data)).ResolveBuild(context.Background(), client.ResolveRequest{
		Components: sdkFixtureComponents(), Platform: "linux/amd64", OutputFormat: "disk-image",
	})
	if resolution != nil || errorCode(err) != nkderr.CodeInvalidCatalog ||
		!strings.Contains(err.Error(), "selects no QCOW2 root") {
		t.Fatalf("disk output format was not enforced during SDK resolution: resolution=%+v err=%v", resolution, err)
	}
}

func TestResolveBuildRequiresExplicitCatalog(t *testing.T) {
	_, err := client.New().ResolveBuild(context.Background(), client.ResolveRequest{})
	if errorCode(err) != nkderr.CodeUsage || !strings.Contains(err.Error(), "requires catalog I/O") {
		t.Fatalf("missing catalog returned %v", err)
	}
}

type countingCatalogIO struct{ calls int }

func (catalogIO *countingCatalogIO) ReadFile(context.Context, string) ([]byte, error) {
	catalogIO.calls++
	return nil, errors.New("must not be called")
}
func (catalogIO *countingCatalogIO) ReadDir(context.Context, string) ([]string, error) {
	catalogIO.calls++
	return nil, errors.New("must not be called")
}
func (catalogIO *countingCatalogIO) Name() string { return "counting" }

func TestResolveBuildCancellationPreventsCatalogIO(t *testing.T) {
	catalogIO := &countingCatalogIO{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.New(client.WithCatalogIO(catalogIO)).ResolveBuild(ctx, client.ResolveRequest{
		Components: []string{"kubectl"}, Platform: "linux/amd64",
	})
	if errorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("cancellation must use the stable timeout code, got %v", err)
	}
	if catalogIO.calls != 0 {
		t.Fatalf("pre-canceled operation performed %d catalog reads", catalogIO.calls)
	}
}

type cancelRunner struct {
	started chan context.Context
	calls   int
}

func (runner *cancelRunner) Name() string { return "cancel-runner" }

func (runner *cancelRunner) Run(_ *ir.Graph, _ string, options pipeline.RunOptions) (*pipeline.Report, error) {
	runner.calls++
	runner.started <- options.Ctx
	<-options.Ctx.Done()
	return nil, options.Ctx.Err()
}

func TestRunPropagatesCancellationToRunner(t *testing.T) {
	graph := &ir.Graph{
		SchemaVersion: ir.SchemaVersion, Platform: "linux/amd64",
		Nodes: []ir.Node{
			{ID: "pull-test", Kind: ir.KindPull, Component: "test", Artifact: "https://example.test/test",
				Digest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: ir.NetworkDefault,
				OutputType: "raw", ArtifactPath: "test", Version: "1.0.0"},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-test"}},
		},
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runner := &cancelRunner{started: make(chan context.Context, 1)}
	c := client.New(client.WithRunner(runner))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, runErr := c.Run(ctx, client.RunRequest{Graph: graph, BuildDigest: digest})
		result <- runErr
	}()

	passedContext := <-runner.started
	if passedContext != ctx {
		t.Fatal("Run did not pass its exact context to pipeline.RunOptions")
	}
	cancel()
	if err := <-result; errorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("runner cancellation must be normalized to CodeTimeout, got %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner called %d times, want once", runner.calls)
	}
}

type recordingOutput struct {
	committed map[string][]byte
	commit    func(context.Context, string, []byte) error
}

func (output *recordingOutput) Commit(ctx context.Context, name string, canonicalBytes []byte) error {
	if output.commit != nil {
		return output.commit(ctx, name, canonicalBytes)
	}
	output.committed[name] = append([]byte(nil), canonicalBytes...)
	return nil
}

type fixedRunner struct{ calls int }

func (runner *fixedRunner) Name() string { return "fixed" }
func (runner *fixedRunner) Run(graph *ir.Graph, digest string, _ pipeline.RunOptions) (*pipeline.Report, error) {
	runner.calls++
	return &pipeline.Report{BuildDigest: digest}, nil
}

func TestRunRejectsUnsafeCallerGraphBeforeInjectedRunner(t *testing.T) {
	resolution, err := client.New(client.WithData(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"))).ResolveBuild(context.Background(), client.ResolveRequest{
		Components: sdkFixtureComponents(), Platform: "linux/amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ir.Node){
		"tagged toolchain": func(node *ir.Node) { node.Toolchain = "ubuntu:latest" },
		"networked build":  func(node *ir.Node) { node.Network = ir.NetworkDefault },
	} {
		t.Run(name, func(t *testing.T) {
			graph := *resolution.Graph
			graph.Nodes = append([]ir.Node(nil), resolution.Graph.Nodes...)
			found := false
			for i := range graph.Nodes {
				if graph.Nodes[i].Kind == ir.KindBuild {
					mutate(&graph.Nodes[i])
					found = true
					break
				}
			}
			if !found {
				t.Fatal("fixture has no source build")
			}
			runner := &fixedRunner{}
			_, err := client.New(client.WithRunner(runner)).Run(context.Background(), client.RunRequest{
				Graph: &graph, BuildDigest: resolution.BuildDigest,
			})
			if err == nil || runner.calls != 0 {
				t.Fatalf("unsafe graph reached injected runner: calls=%d err=%v", runner.calls, err)
			}
		})
	}
}

func sdkFixtureComponents() []string {
	return []string{"cni-plugins", "containerd", "coredns", "crictl", "etcd", "helm", "kube-apiserver", "kube-controller-manager", "kube-proxy", "kube-scheduler", "kubeadm", "kubectl", "kubelet", "pause", "runc"}
}

func TestRequestedAtomicOutputIsAdmittedBeforeCatalogIO(t *testing.T) {
	catalogIO := &countingCatalogIO{}
	_, err := client.New(client.WithCatalogIO(catalogIO)).ResolveBuild(context.Background(), client.ResolveRequest{
		Components: []string{"kubectl"}, Platform: "linux/amd64", OutputName: "graph.json",
	})
	if errorCode(err) != nkderr.CodeUsage {
		t.Fatalf("error = %v, want %s", err, nkderr.CodeUsage)
	}
	if catalogIO.calls != 0 {
		t.Fatalf("missing AtomicOutput performed %d catalog reads", catalogIO.calls)
	}
}

func TestResolveBuildAtomicOutputContract(t *testing.T) {
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")
	request := client.ResolveRequest{Components: sdkFixtureComponents(), Platform: "linux/amd64", OutputName: "graph.json"}
	expected, err := client.New(client.WithData(data)).ResolveBuild(context.Background(), client.ResolveRequest{
		Components: request.Components, Platform: request.Platform,
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := expected.Graph.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		cancel bool
		fail   bool
		code   nkderr.Code
	}{
		{name: "success"},
		{name: "adapter failure", fail: true, code: nkderr.CodeImmutableViolation},
		{name: "cancellation before commit", cancel: true, fail: true, code: nkderr.CodeTimeout},
		{name: "successful commit followed by cancellation", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output := &recordingOutput{committed: map[string][]byte{request.OutputName: []byte("previous")}}
			calls := 0
			output.commit = func(gotContext context.Context, name string, canonicalBytes []byte) error {
				calls++
				if gotContext != ctx || name != request.OutputName || !bytes.Equal(canonicalBytes, want) {
					t.Fatal("graph export changed its context, name or canonical bytes")
				}
				if test.cancel {
					cancel()
				}
				if test.fail {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return nkderr.New(nkderr.CodeImmutableViolation, "test adapter refused commit")
				}
				output.committed[name] = append([]byte(nil), canonicalBytes...)
				return nil
			}
			got, err := client.New(client.WithData(data), client.WithAtomicOutput(output)).ResolveBuild(ctx, request)
			if errorCode(err) != test.code || calls != 1 {
				t.Fatalf("error=%v calls=%d, want code=%s and one commit", err, calls, test.code)
			}
			if test.fail {
				if got != nil || string(output.committed[request.OutputName]) != "previous" {
					t.Fatal("failed export returned a resolution or changed the committed value")
				}
			} else if got == nil || got.BuildDigest != expected.BuildDigest || !bytes.Equal(output.committed[request.OutputName], want) {
				t.Fatal("successful export lost the exact graph or its build identity")
			}
		})
	}
}
