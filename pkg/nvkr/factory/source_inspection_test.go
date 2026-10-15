// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"sigs.k8s.io/yaml"
)

func TestInspectRestoredSourceRejectsUnboundSelectionBeforeWork(t *testing.T) {
	for _, scenario := range []string{"unknown node", "non fetch", "duplicate node", "missing receipt", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			plan := githubTestPlan(t)
			nodeID := "unknown"
			ctx := t.Context()
			code := e.CodeInvalidCatalog
			for _, node := range plan.Graph.Nodes {
				if (scenario == "non fetch" && node.Kind != NodeFetch) || (scenario != "non fetch" && node.Kind == NodeFetch) {
					nodeID = node.ID
					break
				}
			}
			switch scenario {
			case "unknown node":
				nodeID = "unknown"
			case "duplicate node":
				plan.Graph.Nodes = append(plan.Graph.Nodes, plan.Graph.Nodes[0])
			case "missing receipt":
				code = e.CodeFetchFailed
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			}
			t.Chdir(t.TempDir())
			_, err := InspectRestoredSource(ctx, plan, nodeID)
			assertFactoryErrorCode(t, err, code)
			files, err := os.ReadDir(".")
			if err != nil || len(files) != 0 {
				t.Fatalf("rejected inspection wrote output: %v / %v", files, err)
			}
		})
	}
}

type sourceInspectionCatalog struct {
	catalog.ContextDataProvider
	lock []byte
}

func (provider sourceInspectionCatalog) ReadFile(name string) ([]byte, error) {
	if name == catalog.LockFile {
		return provider.lock, nil
	}
	return provider.ContextDataProvider.ReadFile(name)
}

func (provider sourceInspectionCatalog) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == catalog.LockFile {
		return provider.lock, nil
	}
	return provider.ContextDataProvider.ReadFileContext(ctx, name)
}

func TestInspectRestoredSourceChecksExactPlanPaths(t *testing.T) {
	workspace := t.TempDir()
	checkout := filepath.Join(workspace, "fixture-checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", checkout}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("Git fixture: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(checkout, "suite.go"), []byte("package suite\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "suite.go")
	git("-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	commit, tree := git("rev-parse", "HEAD"), git("rev-parse", "HEAD^{tree}")
	epoch, err := strconv.ParseInt(git("show", "-s", "--format=%ct", "HEAD"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	provider := testCatalogProvider(t)
	raw, err := provider.ReadFile(catalog.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	var lock catalog.PinLock
	if err := yaml.UnmarshalStrict(raw, &lock); err != nil {
		t.Fatal(err)
	}
	pin := lock.Pins["containerd"]
	pin.Commit, pin.TreeHash, pin.SourceDateEpoch = commit, tree, epoch
	lock.Pins["containerd"] = pin
	raw, err = canonical.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	base, err := catalog.LoadContext(t.Context(), sourceInspectionCatalog{ContextDataProvider: provider.(catalog.ContextDataProvider), lock: raw})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("source-inspection", "worker", "ubuntu-24.04")
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	var node Node
	for _, candidate := range plan.Graph.Nodes {
		if candidate.Kind == NodeFetch && candidate.IRNode == "fetch-containerd" {
			node = candidate
			break
		}
	}
	if node.ID == "" {
		for _, candidate := range plan.Graph.Nodes {
			if candidate.Kind != NodeFetch {
				continue
			}
			_, instruction, err := plan.producerInstruction(candidate)
			if err == nil && instruction.Commit == commit {
				node = candidate
				break
			}
		}
	}
	if node.ID == "" {
		t.Fatal("compiled Plan omitted fixture source")
	}
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	source := artifacts["source/"+node.BuildTarget+"/"+node.IRNode]
	receipt := artifacts["source-receipt/"+node.BuildTarget+"/"+node.IRNode]
	expectedPath := filepath.Join(workspace, filepath.FromSlash(source.Path))
	if err := os.MkdirAll(filepath.Dir(expectedPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(checkout, expectedPath); err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSuffix(expectedPath, "/"+instruction.Dest)
	if len(instruction.VendorCmd) != 0 {
		t.Fatal("fixture selected unexpected vendor execution")
	}
	if _, err := (pipeline.Local{}).ExecNode(instruction, pipeline.RunOptions{Ctx: t.Context(), SourceRoot: root}); err != nil {
		t.Fatal(err)
	}
	digest, err := pipeline.InspectRestoredSource(t.Context(), instruction, root)
	if err != nil {
		t.Fatal(err)
	}
	derivedReceipt, err := pipeline.SourceReceiptPath(root, instruction.Dest)
	if err != nil || derivedReceipt != filepath.Join(workspace, filepath.FromSlash(receipt.Path)) {
		t.Fatalf("Plan receipt mismatch: %s %s %v", derivedReceipt, receipt.Path, err)
	}
	t.Chdir(workspace)
	got, err := InspectRestoredSource(t.Context(), plan, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := RestoredSourceDescription{Node: node.ID, Path: expectedPath, Commit: commit, TreeHash: tree, ClosureDigest: digest}
	if got != want {
		t.Fatalf("source description = %+v, want %+v", got, want)
	}
}
