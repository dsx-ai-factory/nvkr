// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestSourceFetchWorkspaceCloseCancelsAndJoinsActualGit(t *testing.T) {
	node, repository, _ := pristineClosureFixture(t)
	node.Kind, node.Repo, node.Network = ir.KindFetch, repository, ir.NetworkDefault
	var err error
	node.VendorSpecDigest, err = ir.VendorSpecIdentity(node.VendorCmd, node.VendorImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	workspace, err := HoldSourceWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	started := filepath.Join(t.TempDir(), "git-started")
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte("#!/bin/sh\nprintf ready > '"+started+"'\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment, err := sourcegit.Baseline(shim + string(os.PathListSeparator) + os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := (Local{}).ExecNode(node, withDefaults(RunOptions{Ctx: ctx, SourceRoot: "src",
			SourceWorkspace: workspace, SourceGitEnvironment: environment, Launch: true,
			Stdout: io.Discard, Stderr: io.Discard}))
		result <- err
	}()
	// Always cancel the caller and join before cleanup Close, including Fatal.
	t.Cleanup(func() {
		cancel()
		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Error("actual Git fetch did not join after caller cancellation")
		}
	})
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-result:
			result <- err
			t.Fatalf("fetch returned before blocking Git started: %v", err)
		case <-deadline:
			t.Fatal("blocking Git did not start")
		case <-time.After(time.Millisecond):
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- workspace.Close() }()
	select {
	case err := <-result:
		result <- err // The failure cleanup above still owns a join observation.
		if code(err) != e.CodeTimeout {
			t.Fatalf("live fetch lost borrowed cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace Close did not stop and join actual Git")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace Close returned without a bounded join")
	}
	assertVendorReceiptAbsent(t, node, RunOptions{SourceRoot: "src"})
}

func TestSourceVendorFetchCoreUsesHeldTransportBeforeReceipt(t *testing.T) {
	node, source, opts := pristineClosureFixture(t)
	node.Repo, node.Network = source, ir.NetworkDefault
	node.VendorImage, node.VendorCmd = "sha256:"+strings.Repeat("b", 64), []string{"dependency", "build", "controller/deploy/charts/controller"}
	var err error
	node.VendorSpecDigest, err = ir.VendorSpecIdentity(node.VendorCmd, node.VendorImage)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(opts.SourceRoot))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	workspace, err := HoldSourceWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	r := sourceVendorRuntimeFixture(t, "linux/"+runtime.GOARCH)
	r.result = vendorResultFixture(t, "success")
	held := &heldRuntimeExecutable{}
	opts = withDefaults(RunOptions{Ctx: ctx, SourceRoot: "src", SourceWorkspace: workspace,
		Runtime: "selected-runtime", Launch: true, Stdout: io.Discard, Stderr: io.Discard})
	calls := 0
	err = (Local{}).fetchWithSourceVendorAdmission(node, opts,
		func(observed context.Context, selected ir.Node, executable string) (*sourceVendor, error) {
			calls++
			if _, bounded := observed.Deadline(); !bounded || executable != opts.Runtime || !slices.Equal(selected.VendorCmd, node.VendorCmd) {
				t.Fatal("fetch core lost exact vendor admission inputs")
			}
			return &sourceVendor{held: held, runtime: r, image: node.VendorImage,
				platform: "linux/" + runtime.GOARCH, network: node.Network, command: selected.VendorCmd}, nil
		})
	if err != nil || calls != 1 {
		t.Fatalf("actual Git/closure core failed modeled vendor admission: calls=%d error=%v", calls, err)
	}
	held.closeOnce.Do(func() { t.Fatal("fetch core did not close vendor runtime before receipt") })
	created := false
	for _, command := range r.commands {
		if command[0] == "create" {
			created = true
			if !slices.Contains(command, "--mount") || slices.Contains(command, "-v") {
				t.Fatalf("held fetch regressed to host-bind vendor route: %v", command)
			}
		}
	}
	if !created || len(r.cleaned) != 2 {
		t.Fatalf("fetch core bypassed private vendor/cleanup: created=%t cleanup=%v", created, r.cleaned)
	}
	receiptPath, err := sourceReceiptPath("src", node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(ctx, receiptPath)
	digest, hashErr := sourceTreeDigestContext(ctx, source)
	if err != nil || hashErr != nil || !exists || receipt.ClosureDigest != digest || receipt.VendorSpecDigest != node.VendorSpecDigest {
		t.Fatalf("fetch core receipt lost vendor closure: %+v / %v / %v", receipt, err, hashErr)
	}
}

// Opt-in execution uses an already-local exact native image ID and an explicitly
// selected installed runtime. It never pulls an image or changes runtime config.
// The image's unchanged entrypoint must support the command sh -ec (e.g. a
// locally admitted BusyBox image with no custom entrypoint).
func TestSourceVendorRealRuntimeOptIn(t *testing.T) {
	image, executable := os.Getenv("NVKR_TEST_SOURCE_VENDOR_IMAGE"), os.Getenv("NVKR_TEST_SOURCE_VENDOR_RUNTIME")
	if image == "" && executable == "" {
		t.Skip("requires an explicitly selected installed runtime and already-local native image ID")
	}
	if !canonical.IsDigest(image) || executable == "" {
		t.Fatal("real vendor test requires both an exact local sha256 image ID and a selected runtime")
	}
	node, source, opts := pristineClosureFixture(t)
	node.Kind, node.Repo, node.Network = ir.KindFetch, source, ir.NetworkDefault
	node.VendorImage = image
	node.VendorCmd = []string{"sh", "-ec", `test -z "${NVKR_SOURCE_TOKEN+x}"; test -z "${GIT_CONFIG_COUNT+x}"; test -z "${SSH_AUTH_SOCK+x}"; printf 'real-runtime-vendored' > dependency`}
	var err error
	node.VendorSpecDigest, err = ir.VendorSpecIdentity(node.VendorCmd, node.VendorImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(opts.SourceRoot))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	workspace, err := HoldSourceWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	var runtimeStderr bytes.Buffer
	opts = withDefaults(RunOptions{Ctx: ctx, SourceRoot: "src", SourceWorkspace: workspace,
		Runtime: executable, Launch: true, Stdout: io.Discard, Stderr: &runtimeStderr})
	if _, err := (Local{}).ExecNode(node, opts); err != nil {
		t.Fatalf("%v; runtime stderr: %s", err, runtimeStderr.String())
	}
	got, err := os.ReadFile(filepath.Join(source, "dependency"))
	if err != nil || string(got) != "real-runtime-vendored" {
		t.Fatalf("actual vendor runtime did not return exact closure: %q / %v", got, err)
	}
	receiptPath, err := sourceReceiptPath("src", node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(ctx, receiptPath)
	digest, hashErr := sourceTreeDigestContext(ctx, source)
	if err != nil || hashErr != nil || !exists || receipt.ClosureDigest != digest || receipt.VendorSpecDigest != node.VendorSpecDigest {
		t.Fatalf("actual runtime receipt lost exact closure binding: %+v / %v / %v", receipt, err, hashErr)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".nvkr-packer-") {
			t.Fatal("actual source runtime leaked private transport or Git home")
		}
	}
}
