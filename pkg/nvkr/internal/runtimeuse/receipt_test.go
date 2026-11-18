// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeuse

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// This fixture tests internal receipt lifetime and binding, not runtime
// production or evidence authentication. Those belong to the factory issuer.
func fixture() (Binding, ir.Node) {
	digest := "sha256:" + strings.Repeat("a", 64)
	selection := ir.RuntimeSelection{Tool: "runtime", BuildDigest: digest, Component: "vm-runtime", Platform: "linux/amd64", Invocation: ir.RuntimeInvocation}
	node := ir.Node{ID: "build-image", Kind: ir.KindBuild, Component: "image", BuilderType: ir.BuilderVMImage,
		Needs: []string{"fetch-image"}, Runtime: &selection, ToolchainPlatform: "linux/amd64", Platform: "linux/amd64",
		Network: ir.NetworkNone, Workdir: "/src", SourceDest: "source-image", SourceCommit: strings.Repeat("b", 40), SourceTreeHash: strings.Repeat("c", 40),
		VendorSpecDigest: digest, Disk: &ir.DiskSpec{Format: "qcow2", Encryption: "none"},
		OutputType: "disk-image", ArtifactPath: "image.qcow2", Version: "1.0.0",
		Packer: &ir.PackerConfig{ProjectPath: ".", PackerVersion: "1.16.2", BuildName: "image.qemu.node", ArchitectureVariable: "architecture",
			AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory"},
		Requirements: &execution.Requirements{Resources: &execution.ResourceRequest{CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768},
			AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			Capabilities:         []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}}, TimeoutSeconds: 7200}}
	return Binding{PlanDigest: digest, Subject: digest + "/" + digest + "/node-image/build-image", Purpose: "packer-build", Runtime: selection,
		Production: ProductionBinding{BuildDigest: digest, Platform: selection.Platform, Component: selection.Component, Digest: digest, Size: 1,
			LayoutDigest: digest, LayerDigest: digest, TerminalDigest: digest}, Observation: digest, SBOM: digest}, node
}

func requireCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v; want %s", err, want)
	}
}

func TestReceiptAuthenticatesOncePerBoundaryAndCannotReplay(t *testing.T) {
	binding, node := fixture()
	authentications := 0
	receipt, err := Issue(t.Context(), binding, node, func(context.Context) error { authentications++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer receipt.Close()
	_, err = receipt.CompletedBinding()
	requireCode(t, err, e.CodeGateUnmet)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	work := 0
	if err := receipt.Execute(ctx, binding.Subject, node, binding.Production, func(context.Context) error { work++; return nil }); err != nil {
		t.Fatal(err)
	}
	if authentications != 3 || work != 1 {
		t.Fatalf("authentication/work = %d/%d", authentications, work)
	}
	completed, err := receipt.CompletedBinding()
	if err != nil || completed != binding {
		t.Fatalf("completion = %+v, %v", completed, err)
	}
	copy := *receipt
	requireCode(t, copy.Execute(ctx, binding.Subject, node, binding.Production, func(context.Context) error { t.Fatal("receipt replayed"); return nil }), e.CodeGateUnmet)
	receipt.Close()
	_, err = copy.CompletedBinding()
	requireCode(t, err, e.CodeGateUnmet)
}

func TestReceiptRejectsSubstitutionBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Binding, *ir.Node)
	}{
		{"foreign Run subject", func(b *Binding, _ *ir.Node) { b.Subject += "-other" }},
		{"runtime manifest", func(b *Binding, _ *ir.Node) { b.Production.Digest = "sha256:" + strings.Repeat("f", 64) }},
		{"runtime layout", func(b *Binding, _ *ir.Node) { b.Production.LayoutDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"runtime layer", func(b *Binding, _ *ir.Node) { b.Production.LayerDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"runtime result", func(b *Binding, _ *ir.Node) { b.Production.TerminalDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"consumer command", func(_ *Binding, n *ir.Node) { n.Packer.PackerVersion = "1.16.3" }},
		{"consumer requirement", func(_ *Binding, n *ir.Node) { n.Runtime.Tool = "sibling" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, node := fixture()
			receipt, err := Issue(t.Context(), binding, node, func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer receipt.Close()
			tc.change(&binding, &node)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			requireCode(t, receipt.Execute(ctx, binding.Subject, node, binding.Production, func(context.Context) error { t.Fatal("substituted use executed"); return nil }), e.CodeVerifyFailed)
		})
	}
}

func TestReceiptCloseCancelsAndJoinsConsumer(t *testing.T) {
	binding, node := fixture()
	receipt, err := Issue(t.Context(), binding, node, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseWork := sync.OnceFunc(func() { close(release) })
	defer receipt.Close()
	defer releaseWork()
	result := make(chan error, 1)
	go func() {
		result <- receipt.Execute(ctx, binding.Subject, node, binding.Production, func(callCtx context.Context) error {
			close(started)
			<-callCtx.Done()
			close(canceled)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("consumer did not start")
	}
	closed := make(chan struct{})
	go func() { receipt.Close(); close(closed) }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("Close did not cancel consumer")
	}
	select {
	case <-closed:
		t.Fatal("Close returned without joining consumer")
	default:
	}
	releaseWork()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("Close did not join consumer")
	}
	requireCode(t, <-result, e.CodeTimeout)
	_, err = receipt.CompletedBinding()
	requireCode(t, err, e.CodeGateUnmet)
}

func TestReceiptRejectsAbsentRevokedAndUnboundedUse(t *testing.T) {
	binding, node := fixture()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	work := func(context.Context) error { t.Fatal("unadmitted work executed"); return nil }
	requireCode(t, (&Receipt{}).Execute(ctx, binding.Subject, node, binding.Production, work), e.CodeGateUnmet)
	receipt, err := Issue(ctx, binding, node, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, receipt.Execute(context.WithoutCancel(ctx), binding.Subject, node, binding.Production, work), e.CodeEngineCapability)
	receipt.Close()
	requireCode(t, receipt.Execute(ctx, binding.Subject, node, binding.Production, work), e.CodeGateUnmet)
}

func TestReceiptRefusesChangedEvidenceAfterWork(t *testing.T) {
	binding, node := fixture()
	checks := 0
	receipt, err := Issue(t.Context(), binding, node, func(context.Context) error {
		checks++
		if checks == 3 {
			return e.New(e.CodeVerifyFailed, "changed original evidence")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receipt.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	requireCode(t, receipt.Execute(ctx, binding.Subject, node, binding.Production, func(context.Context) error { return nil }), e.CodeVerifyFailed)
	_, err = receipt.CompletedBinding()
	requireCode(t, err, e.CodeGateUnmet)
}

func TestReceiptIssueRequiresCompleteConsumerAndEvidenceBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Binding)
	}{
		{"missing observation", func(b *Binding) { b.Observation = "" }},
		{"missing SBOM", func(b *Binding) { b.SBOM = "" }},
		{"foreign component", func(b *Binding) { b.Production.Component = "sibling" }},
		{"foreign producer", func(b *Binding) { b.Production.BuildDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"wrong purpose", func(b *Binding) { b.Purpose = "inspection" }},
		{"wrong Plan", func(b *Binding) { b.PlanDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"missing relocated Plan", func(b *Binding) { b.Subject = b.PlanDigest + "/node-image/build-image" }},
		{"foreign consumer", func(b *Binding) { b.Subject += "-other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, node := fixture()
			tc.change(&binding)
			receipt, err := Issue(t.Context(), binding, node, func(context.Context) error { t.Fatal("invalid binding reached authority callback"); return nil })
			requireCode(t, err, e.CodeVerifyFailed)
			if receipt != nil {
				t.Fatal("invalid binding issued a receipt")
			}
		})
	}
	binding, node := fixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	receipt, err := Issue(ctx, binding, node, func(context.Context) error { cancel(); return nil })
	requireCode(t, err, e.CodeTimeout)
	if receipt != nil {
		t.Fatal("canceled issuer returned authority")
	}
}
