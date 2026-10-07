// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestExecNodeDryRun(t *testing.T) {
	var buf bytes.Buffer
	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	fetch := testFetchNode("fetch-kubernetes", "https://example.test/kubernetes", testSourceCommit, testSourceTree, "kubernetes", toolchain, "sha256:"+repeatHex("d"))
	n := completeTestBuild(ir.Node{
		ID: "build-kubelet", Kind: ir.KindBuild, Component: "kubelet",
		BuilderType: "oci-hermetic", Command: []string{"make", "kubelet"},
		Toolchain: toolchain, ArtifactPath: "_output/kubelet", WriteDir: "_output",
	}, fetch)
	if _, err := (Local{}).ExecNode(n, RunOptions{Ctx: context.Background(), DryRun: true, Stdout: &buf}); err != nil {
		t.Fatalf("dry-run exec-node must succeed: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("exec-node:kubelet")) {
		t.Fatalf("dry-run did not describe the node:\n%s", buf.String())
	}
}

func TestExecNodeRefusesDeclaredBuildWithoutEnforceableIsolation(t *testing.T) {
	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	fetch := testFetchNode("fetch-kubernetes", "https://example.test/kubernetes", testSourceCommit, testSourceTree, "kubernetes", toolchain, "sha256:"+repeatHex("d"))
	base := completeTestBuild(ir.Node{
		ID: "build-kubelet", Kind: ir.KindBuild, Component: "kubelet",
		BuilderType: "oci-hermetic", Command: []string{"make", "kubelet"},
		Toolchain: toolchain, ArtifactPath: "_output/kubelet", WriteDir: "_output",
	}, fetch)
	for _, mutation := range []func(*ir.Node){
		func(n *ir.Node) { n.SourceReadOnly = false },
		func(n *ir.Node) { n.WriteDir = "" },
	} {
		n := base
		mutation(&n)
		if _, err := (Local{}).ExecNode(n, RunOptions{Ctx: context.Background(), DryRun: true}); code(err) != "NKD-E-ENGINE-CAPABILITY" {
			t.Fatalf("unisolated build must fail closed, got %v", err)
		}
	}
}

func TestExecNodeVerifyIsNoop(t *testing.T) {
	res, err := (Local{}).ExecNode(ir.Node{ID: "verify", Kind: ir.KindVerify}, RunOptions{Ctx: context.Background()})
	if err != nil || res != nil {
		t.Fatalf("verify node is orchestration, exec-node should no-op: %v %v", res, err)
	}
}
