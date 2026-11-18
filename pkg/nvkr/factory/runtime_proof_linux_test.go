// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestMain(m *testing.M) {
	if handled, err := pipeline.RuntimeNamespaceEntry(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Preparation deliberately runs as the ordinary runner user. The root phase
// cannot fetch source or read the checkout's credentials/environment.
func TestNativeRuntimePrepare(t *testing.T) {
	if os.Getenv("NVKR_TEST_RUNTIME_PHASE") != "prepare" {
		t.Skip("explicit native runtime preparation only")
	}
	if os.Geteuid() == 0 {
		t.Fatal("source preparation must be unprivileged")
	}
	plan := nativeRuntimeProofPlan(t, os.Getenv("NVKR_TEST_RUNTIME_CATALOG"),
		os.Getenv("NVKR_TEST_RUNTIME_COMMIT"), os.Getenv("NVKR_TEST_RUNTIME_TREE"))
	t.Chdir(nativeRuntimeProofWorkspace(t))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("plan.json", raw, 0o444); err != nil {
		t.Fatal(err)
	}
	for _, node := range topologicalFactoryNodes(plan.Graph) {
		if node.Kind == NodeFetch {
			if err := executeSourceFetch(ctx, plan, node.ID, []string{"PATH=/usr/bin:/bin"}, os.Stdout, os.Stderr, (pipeline.Local{}).ExecNode); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func nativeRuntimeProofWorkspace(t *testing.T) string {
	t.Helper()
	path := os.Getenv("NVKR_TEST_RUNTIME_WORKSPACE")
	if !filepath.IsAbs(path) {
		t.Fatal("proof requires an explicit absolute workspace")
	}
	return path
}

// This integration test uses the concrete production dispatcher and original
// Run owner. It exercises no provider admission, signing or publication, and
// its exported descriptions cannot restore any of the live receipts.
func TestNativeRuntimeConsumer(t *testing.T) {
	if os.Getenv("NVKR_TEST_RUNTIME_PHASE") != "consume" {
		t.Skip("explicit native runtime consumer proof only")
	}
	if os.Geteuid() != 0 {
		t.Fatal("consumer requires the admitted privileged bootstrap")
	}
	t.Chdir(nativeRuntimeProofWorkspace(t))
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
	defer cancel()
	plan, err := LoadPlanContext(ctx, "plan.json")
	if err != nil {
		t.Fatal(err)
	}
	root := planNode(plan, "node-image/build-image")
	workspace, _, err := securepath.OpenDirectory(".")
	if err != nil {
		t.Fatal(err)
	}
	defer nativeRuntimeProofClose(t, workspace.Close)
	nativeRuntimeProofMemory(t, "initial admission", workspace.Name())
	admission, err := AdmitLocalVMBuild(ctx, plan, root.ID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer nativeRuntimeProofClose(t, admission.Close)
	scratch, err := newRuntimeScratch(ctx, 16384)
	if err != nil {
		t.Fatal(err)
	}
	defer nativeRuntimeProofClose(t, scratch.Close)
	session, err := pipeline.NewNodeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	diagnostics, err := pipeline.NewPackerDiagnosticSink(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer nativeRuntimeProofClose(t, diagnostics.Close)
	executor, err := (localVMPackerResolver{}).Admit(ctx, plan, session, vmDriverRunConfig{
		NodeID: root.ID, Runtime: plan.Environment.RuntimeBinary(), Stdout: os.Stdout, Stderr: os.Stderr, Diagnostics: diagnostics,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer nativeRuntimeProofClose(t, executor.Close)
	digest, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	owner := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, Qualification: TierLocalExecution,
		PlanDigest: digest, Workspace: ".nvkr/factory", runtimeScratch: scratch}
	defer nativeRuntimeProofClose(t, owner.Close)
	operations := newOperationSession(plan, operationPlacement{runtime: plan.Environment.RuntimeBinary(), jobs: 1,
		producer: session.ExecNode, owner: owner}, os.Stdout, os.Stderr)
	_, artifacts := planIndex(plan)
	for _, node := range topologicalFactoryNodes(plan.Graph) {
		t.Logf("operation %s (%s)", node.ID, node.Kind)
		switch {
		case node.Kind == NodeFetch: // Already fetched with its receipt outside root.
		case node.ID == root.ID:
			nativeRuntimeProofMemory(t, "consumer admission", workspace.Name(), scratch.location)
			err = populateDiskProducerCompletion(ctx, plan, root.ID, owner,
				func(ctx context.Context, selected Node, _ ir.Node) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
					return dispatchAdmittedVMProducer(ctx, admission, plan, selected, executor, owner)
				})
		case node.BuildTarget == "internal/runtime":
			err = operations.dispatch(ctx, plan, node)
		case node.Kind == NodeVerify:
			err = executeHeldDiskObservation(ctx, plan, node, artifacts, owner)
		default:
			t.Fatalf("unexpected proof operation %s (%s)", node.ID, node.Kind)
		}
		if err != nil {
			t.Fatal(err)
		}
		owner.Nodes = append(owner.Nodes, LocalNodeResult{ID: node.ID, Kind: node.Kind, Status: "ok"})
	}
	observation, result, err := owner.inspectDiskLifecycleMetadata(ctx, plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeUse == nil || result.RuntimeUse.SBOM == "" {
		t.Fatal("disk did not complete original runtime and SBOM consumption")
	}
	nativeRuntimeProofRecord(t, "disk-observation.json", observation)
	nativeRuntimeProofRecord(t, "disk-result.json", result)
	layer := result.RuntimeUse.Production.LayerDigest
	layout := artifacts[plan.InternalTools[0].ArtifactID].Path
	inventory, err := runtimeProofTarInventory(ctx, filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(layer, "sha256:")), layer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("runtime-tar-entries.json", inventory, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := commitGitHubDiskFile(ctx, "disk.qcow2", func(file *os.File) error {
		_, err := owner.ExportDiskLifecyclePayload(ctx, plan, plan.LifecycleOutputs[0], file)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("bounded disk completed: %s (%d bytes); original runtime use: %+v", observation.Digest, observation.Size, result.RuntimeUse)
}

func nativeRuntimeProofRecord(t *testing.T, name string, record any) {
	t.Helper()
	raw, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o444); err != nil {
		t.Fatal(err)
	}
}

func nativeRuntimeProofClose(t *testing.T, close func() error) {
	t.Helper()
	if err := close(); err != nil {
		t.Error(err)
	}
}
