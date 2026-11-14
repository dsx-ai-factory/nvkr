// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestPublicRunnerEntryPointsRejectNilContextAtAdmission(t *testing.T) {
	if _, err := (Local{}).ExecNode(ir.Node{}, RunOptions{}); code(err) != nvkrerr.CodeUsage {
		t.Fatalf("ExecNode accepted a nil context or inspected its node first: %v", err)
	}
}

func TestExecNodeCancellationStopsGitFetch(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	fetch := testFetchNode("fetch-cancel", "https://example.test/source", testSourceCommit, testSourceTree,
		"source", toolchain, "sha256:"+repeatHex("d"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	started := time.Now()
	_, err := (Local{}).ExecNode(fetch, RunOptions{Launch: true,
		Ctx: ctx, SourceRoot: filepath.Join(t.TempDir(), "work", "src"), Stdout: &output,
	})
	if code(err) != nvkrerr.CodeTimeout {
		t.Fatalf("canceled fetch returned %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("git fetch ignored cancellation for %s", elapsed)
	}
}

func TestMapCancellationPreservesRetryableTimeoutContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := mapCancellation(ctx, nvkrerr.New(nvkrerr.CodeBuildFailed, "semantic wrapper"), "build")
	if code(err) != nvkrerr.CodeTimeout || !nvkrerr.Retryable(err) {
		t.Fatalf("cancellation was not retained as a retryable timeout: %v", err)
	}
}

func code(err error) nvkrerr.Code {
	var e *nvkrerr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestErrorCodeMatchesPublicExitStatusThroughWrapping(t *testing.T) {
	primary := nvkrerr.New(nvkrerr.CodeBuildFailed, "build failed")
	secondary := nvkrerr.New(nvkrerr.CodeVerifyFailed, "log collection failed")
	for _, test := range []struct {
		name string
		err  error
		want nvkrerr.Code
		exit int
	}{
		{name: "nil", exit: 0},
		{name: "uncoded", err: errors.New("uncoded"), exit: 1},
		{name: "direct", err: primary, want: nvkrerr.CodeBuildFailed, exit: 21},
		{name: "wrapped", err: fmt.Errorf("outer: %w", primary), want: nvkrerr.CodeBuildFailed, exit: 21},
		{name: "joined nil", err: errors.Join(primary, nil), want: nvkrerr.CodeBuildFailed, exit: 21},
		{name: "joined secondary", err: errors.Join(primary, secondary), want: nvkrerr.CodeBuildFailed, exit: 21},
		{name: "nested join", err: errors.Join(errors.Join(primary, secondary), context.Canceled), want: nvkrerr.CodeBuildFailed, exit: 21},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := code(test.err); got != test.want {
				t.Fatalf("code(%v) = %q, want %q", test.err, got, test.want)
			}
			if got := nvkrerr.ExitCode(test.err); got != test.exit {
				t.Fatalf("ExitCode(%v) = %d, want %d", test.err, got, test.exit)
			}
		})
	}
}
