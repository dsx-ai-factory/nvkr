// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeuse"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// This fixture exercises the private staging boundary with synthetic production
// and issuer state. It does not establish native assembly or factory admission.
func consumerRootFixture(t *testing.T) (*ProducedRuntime, *runtimeuse.Receipt, runtimeuse.Binding, ir.Node) {
	t.Helper()
	producer, result, options, _ := runtimeReceiptFixtureFiles(t, map[string]string{"marker": "exact runtime bytes"})
	digest := "sha256:" + strings.Repeat("4", 64)
	tool, err := captureExecutedRuntime(t.Context(), "linux/amd64", digest, producer, result, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	production, err := tool.BindingContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	selection := ir.RuntimeSelection{Tool: "runtime", BuildDigest: digest, Component: producer.Component, Platform: "linux/amd64", Invocation: ir.RuntimeInvocation}
	node := ir.Node{ID: "build-image", Kind: ir.KindBuild, Component: "image", BuilderType: ir.BuilderVMImage,
		Needs: []string{"fetch-image"}, Runtime: &selection, ToolchainPlatform: "linux/amd64", Platform: "linux/amd64",
		Network: ir.NetworkNone, Workdir: "/src", SourceDest: "source-image", SourceCommit: strings.Repeat("b", 40), SourceTreeHash: strings.Repeat("c", 40),
		VendorSpecDigest: digest, Disk: &ir.DiskSpec{Format: "qcow2", Encryption: "none"}, OutputType: "disk-image", ArtifactPath: "image.qcow2", Version: "1.0.0",
		Packer: &ir.PackerConfig{ProjectPath: ".", PackerVersion: "1.16.2", BuildName: "image.qemu.node", ArchitectureVariable: "architecture",
			AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory"},
		Requirements: &execution.Requirements{Resources: &execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 1024, EphemeralStorageMiB: 4096},
			AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			Capabilities:         []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred}}, TimeoutSeconds: 60}}
	binding := runtimeuse.Binding{PlanDigest: digest, Subject: digest + "/" + digest + "/node-image/build-image", Purpose: "packer-build",
		Runtime: selection, Production: production, Observation: digest, SBOM: digest}
	use, err := runtimeuse.Issue(t.Context(), binding, node, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(use.Close)
	return tool, use, binding, node
}

func TestRuntimeConsumerStagesOnlyExactHeldLayerAndCleans(t *testing.T) {
	tool, use, binding, node := consumerRootFixture(t)
	scratch := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := tool.withConsumerRoot(ctx, use, binding.Subject, node, scratch, func(_ context.Context, root *os.File) error {
		file, _, err := securepath.OpenChild(root, "marker", false)
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := io.ReadAll(file)
		if err == nil && string(raw) != "exact runtime bytes" {
			t.Fatalf("staged content = %q", raw)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := use.CompletedBinding(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging cleanup = %v, %v", entries, err)
	}
	if code(tool.withConsumerRoot(ctx, use, binding.Subject, node, scratch, func(context.Context, *os.File) error {
		t.Fatal("replayed runtime use")
		return nil
	})) != e.CodeGateUnmet {
		t.Fatal("completed use replayed")
	}
}

func TestRuntimeConsumerRejectsUnownedUseBeforeStaging(t *testing.T) {
	for _, scenario := range []string{"absent use", "zero use", "revoked use", "closed production", "foreign consumer", "unbounded use"} {
		t.Run(scenario, func(t *testing.T) {
			tool, use, binding, node := consumerRootFixture(t)
			want := e.CodeGateUnmet
			switch scenario {
			case "absent use":
				use = nil
			case "zero use":
				use = &runtimeuse.Receipt{}
			case "revoked use":
				use.Close()
			case "closed production":
				_ = tool.Close()
			case "foreign consumer":
				node.Packer.PackerVersion = "1.16.3"
				want = e.CodeVerifyFailed
			}
			scratch := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if scenario == "unbounded use" {
				ctx, want = context.WithoutCancel(ctx), e.CodeEngineCapability
			}
			err := tool.withConsumerRoot(ctx, use, binding.Subject, node, scratch, func(context.Context, *os.File) error {
				t.Fatal("unowned runtime entered consumer")
				return nil
			})
			if code(err) != want {
				t.Fatalf("admission = %v; want %s", err, want)
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejection staged files: %v, %v", entries, err)
			}
		})
	}
}

func TestRuntimeConsumerRevocationJoinsStagingCleanup(t *testing.T) {
	tool, use, binding, node := consumerRootFixture(t)
	scratch := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- tool.withConsumerRoot(ctx, use, binding.Subject, node, scratch, func(callCtx context.Context, _ *os.File) error {
			close(started)
			<-callCtx.Done()
			return e.Wrap(e.CodeTimeout, callCtx.Err(), "consumer canceled")
		})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("consumer did not enter staged root")
	}
	use.Close()
	if err := <-result; code(err) != e.CodeTimeout {
		t.Fatalf("revoked consumer = %v", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("Close returned before staging cleanup: %v, %v", entries, err)
	}
	if err := tool.Close(); err != nil {
		t.Fatal(err)
	}
}
