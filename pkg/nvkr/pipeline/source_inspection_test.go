// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestInspectRestoredSource(t *testing.T) {
	for _, scenario := range []string{"valid", "missing receipt", "wrong receipt", "drift", "rewritten receipt", "staged edit with rewritten receipt", "wrong HEAD", "canceled", "wrong node"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "src")
			dir := filepath.Join(root, "checkout")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("Git fixture: %v: %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			if err := os.WriteFile(filepath.Join(dir, "suite.go"), []byte("package suite\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "suite.go")
			git("-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
			n := ir.Node{Kind: ir.KindFetch, Dest: "checkout", Commit: git("rev-parse", "HEAD"), TreeHash: git("rev-parse", "HEAD^{tree}")}
			vendor, err := fetchVendorSpecDigest(n)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := sourceTreeDigestContext(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			path, err := sourceReceiptPath(root, n.Dest)
			if err != nil {
				t.Fatal(err)
			}
			receipt := sourceReceipt{Schema: sourceReceiptSchema, Commit: n.Commit, TreeHash: n.TreeHash, VendorSpecDigest: vendor, ClosureDigest: digest}
			if scenario == "wrong receipt" {
				receipt.Commit = strings.Repeat("a", 40)
			}
			if scenario != "missing receipt" {
				if err := writeSourceReceiptContext(t.Context(), path, receipt); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			switch scenario {
			case "drift":
				if err := os.WriteFile(filepath.Join(dir, "suite.go"), []byte("substituted\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "rewritten receipt", "staged edit with rewritten receipt":
				if err := os.WriteFile(filepath.Join(dir, "suite.go"), []byte("package tampered\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "staged edit with rewritten receipt" {
					git("add", "suite.go")
				}
				changed, err := sourceTreeDigestContext(t.Context(), dir)
				if err != nil {
					t.Fatal(err)
				}
				receipt.ClosureDigest = changed
				if err := writeSourceReceiptContext(t.Context(), path, receipt); err != nil {
					t.Fatal(err)
				}
			case "wrong HEAD":
				git("-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "different commit")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "wrong node":
				n.Kind = ir.KindBuild
			}
			got, err := InspectRestoredSource(ctx, n, root)
			if scenario == "valid" {
				if err != nil || got != digest {
					t.Fatalf("inspection=%q error=%v", got, err)
				}
				return
			}
			code := e.CodeFetchFailed
			if scenario == "canceled" {
				code = e.CodeTimeout
			}
			if scenario == "wrong node" {
				code = e.CodeInvalidCatalog
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != code {
				t.Fatalf("inspection error=%v, want code %s", err, code)
			}
			if got != "" {
				t.Fatal("failed inspection returned a source description")
			}
			if scenario == "missing receipt" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("inspection created missing receipt: %v", err)
				}
			}
		})
	}
}

func TestSourceReceiptReadCancellationKeepsTimeoutCode(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := readSourceReceiptContext(ctx, filepath.Join(t.TempDir(), "receipt.json"))
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("receipt-read cancellation = %v, want Timeout", err)
	}
}
