// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

func TestBoundedRegularInputAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		maximum  int64
		wantCode nkderr.Code
	}{
		{name: "bounded regular file", ctx: context.Background(), maximum: 4},
		{name: "too large", ctx: context.Background(), maximum: 3, wantCode: nkderr.CodePipelineDrift},
		{name: "negative limit", ctx: context.Background(), maximum: -1, wantCode: nkderr.CodePipelineDrift},
		{name: "unsupported limit", ctx: context.Background(), maximum: maxRegularInputBytes + 1, wantCode: nkderr.CodePipelineDrift},
		{name: "missing context", maximum: 4, wantCode: nkderr.CodeUsage},
		{name: "canceled", ctx: canceled, maximum: 4, wantCode: nkderr.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := readBoundedRegularInput(test.ctx, path, test.maximum)
			if cliErrorCode(err) != test.wantCode {
				t.Fatalf("read code = %q, want %q: %v", cliErrorCode(err), test.wantCode, err)
			}
			if err == nil && string(raw) != "data" {
				t.Fatalf("read bytes = %q, want data", raw)
			}
		})
	}
}

func TestBoundedRegularInputRejectsLinksAndFIFOsWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "member-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularInput(context.Background(), link, 64); cliErrorCode(err) != nkderr.CodePipelineDrift {
		t.Fatalf("symlink input read code = %q, want %q: %v",
			cliErrorCode(err), nkderr.CodePipelineDrift, err)
	}

	fifo := filepath.Join(root, "member-fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := readBoundedRegularInput(ctx, fifo, 64); cliErrorCode(err) != nkderr.CodePipelineDrift {
		t.Fatalf("FIFO input read code = %q, want %q without blocking: %v",
			cliErrorCode(err), nkderr.CodePipelineDrift, err)
	}
	app := &cli.App{Commands: []*cli.Command{cmdExecNode()}}
	err := app.RunContext(ctx, []string{"nkd-exec", "exec-node", "--nodes-digest", "sha256:" + strings.Repeat("a", 64), fifo})
	if cliErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("exec-node FIFO code = %q, want %q without blocking: %v",
			cliErrorCode(err), nkderr.CodeInvalidCatalog, err)
	}
}
