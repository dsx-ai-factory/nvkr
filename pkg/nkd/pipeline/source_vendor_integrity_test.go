// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Exercise concrete source/archive/receipt owners with the existing modeled
// daemon. These protocol regressions do not prove actual Docker/Helm execution.
type sourceVendorIntegrityRuntime struct {
	*sourceVendorTestRuntime
	intercept func(io.Reader, io.Writer, []string) (bool, error)
}

func (r *sourceVendorIntegrityRuntime) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if r.intercept != nil {
		handled, err := r.intercept(stdin, stdout, args)
		if handled || err != nil {
			r.commands = append(r.commands, append([]string(nil), args...))
			return err
		}
	}
	return r.sourceVendorTestRuntime.Run(ctx, stdin, stdout, stderr, args...)
}

func TestSourceVendorTransportIntegrityFailuresCannotIssueReceipt(t *testing.T) {
	for _, test := range []struct {
		name, diagnostic string
		code             e.Code
		prework          bool
	}{
		{"changed daemon before work", "runtime identity changed", e.CodeEngineCapability, true},
		{"changed image after admission", "image identity changed", e.CodeEngineCapability, true},
		{"empty creation confirmation", "did not confirm", e.CodeFetchFailed, false},
		{"input carrier changed after transfer", "rechecking vendor input carrier", e.CodeFetchFailed, false},
		{"input carrier closed after transfer", "rechecking vendor input carrier", e.CodeFetchFailed, false},
		{"malformed returned archive", "decoding source vendor return carrier", e.CodeFetchFailed, false},
		{"exhausted return byte budget", "collecting bounded source vendor closure", e.CodeFetchFailed, false},
		{"changed daemon after collection", "runtime identity changed", e.CodeEngineCapability, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, checkoutPath, opts := pristineClosureFixture(t)
			node.VendorCmd = []string{"dependency", "build"}
			node.VendorImage = "registry.example/vendor@sha256:" + strings.Repeat("1", 64)
			checkout, _, err := securepath.OpenDirectory(checkoutPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			workspacePath := t.TempDir()
			workspace, _, err := securepath.OpenDirectory(workspacePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = workspace.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			t.Cleanup(cancel)
			platform := "linux/" + runtime.GOARCH
			base := sourceVendorRuntimeFixture(t, platform)
			base.result = vendorResultFixture(t, "success")
			adapter := &sourceVendorIntegrityRuntime{sourceVendorTestRuntime: base}
			vendor := &sourceVendor{runtime: adapter, image: "sha256:" + strings.Repeat("b", 64), platform: platform,
				network: ir.NetworkDefault, command: node.VendorCmd}
			switch test.name {
			case "changed daemon before work":
				base.version = []byte(`{"Client":{"Version":"changed"}}`)
			case "changed image after admission":
				base.image = strings.Replace(base.image, vendor.image, "sha256:"+strings.Repeat("c", 64), 1)
			case "empty creation confirmation":
				adapter.intercept = func(_ io.Reader, _ io.Writer, args []string) (bool, error) { return args[0] == "create", nil }
			case "input carrier changed after transfer", "input carrier closed after transfer":
				adapter.intercept = func(stdin io.Reader, _ io.Writer, args []string) (bool, error) {
					if args[0] != "cp" || stdin == nil {
						return false, nil
					}
					file, ok := stdin.(*os.File)
					if !ok {
						return true, errors.New("fixture did not receive the original held input carrier")
					}
					if _, err := io.Copy(io.Discard, file); err != nil {
						return true, err
					}
					if test.name == "input carrier closed after transfer" {
						return true, file.Close()
					}
					return true, os.WriteFile(file.Name(), []byte("substituted carrier"), 0o600)
				}
			case "malformed returned archive":
				base.result = []byte("invalid non-tar source carrier")
			case "exhausted return byte budget":
				adapter.intercept = func(stdin io.Reader, stdout io.Writer, args []string) (bool, error) {
					if args[0] != "cp" || stdin != nil {
						return false, nil
					}
					limit, ok := stdout.(*packerContainerLimitWriter)
					if !ok || limit.remaining <= 0 {
						return true, errors.New("fixture did not reach the admitted bounded result writer")
					}
					// Model an already-consumed allowance without allocating its
					// potentially multi-GiB size or substituting host capacity.
					limit.remaining = 1
					_, err := stdout.Write([]byte("overflow"))
					return true, err
				}
			case "changed daemon after collection":
				base.beforeStart = func() error { base.version = []byte(`{"Client":{"Version":"changed"}}`); return nil }
			}
			opts.Ctx, opts.fetchDirectory = ctx, checkout
			err = ensureSourceClosure(node, checkoutPath, opts, func() error {
				return vendor.run(ctx, workspace, checkout, io.Discard, io.Discard)
			})
			if code(err) != test.code || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("wrong integrity guard: want %s/%q, got %v", test.code, test.diagnostic, err)
			}
			assertVendorReceiptAbsent(t, node, opts)
			input, err := os.ReadFile(filepath.Join(checkoutPath, "input"))
			if err != nil || string(input) != "source bytes" {
				t.Fatalf("rejected transport changed original source: %q / %v", input, err)
			}
			if test.prework {
				for _, command := range base.commands {
					if command[0] == "volume" || command[0] == "create" || command[0] == "cp" || command[0] == "start" {
						t.Fatalf("identity rejection reached mutable runtime work: %v", base.commands)
					}
				}
			} else if len(base.cleaned) != 2 {
				t.Fatalf("failure lost private container/volume cleanup: %v", base.cleaned)
			}
			entries, err := os.ReadDir(workspacePath)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed transport leaked private staging: %v / %v", entries, err)
			}
		})
	}
}

// The existing context-check boundaries provide a deterministic mutation point
// after a real file has been copied. No production copy/receipt seam is added.
type vendorCopyMutationContext struct {
	context.Context
	mu     sync.Mutex
	mutate func() (bool, error)
	hit    bool
	err    error
}

func (ctx *vendorCopyMutationContext) Err() error {
	ctx.mu.Lock()
	if !ctx.hit && ctx.err == nil {
		ctx.hit, ctx.err = ctx.mutate()
	}
	ctx.mu.Unlock()
	return ctx.Context.Err()
}

func TestSourceVendorCopiedInputDigestIsCheckedBeforeRuntimeWork(t *testing.T) {
	node, checkoutPath, opts := pristineClosureFixture(t)
	node.VendorCmd = []string{"dependency", "build"}
	node.VendorImage = "registry.example/vendor@sha256:" + strings.Repeat("1", 64)
	checkout, _, err := securepath.OpenDirectory(checkoutPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkout.Close() })
	workspacePath := t.TempDir()
	workspace, _, err := securepath.OpenDirectory(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	bounded, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	ctx := &vendorCopyMutationContext{Context: bounded}
	ctx.mutate = func() (bool, error) {
		paths, err := filepath.Glob(filepath.Join(workspacePath, "*", "input", "source", "input"))
		if err != nil || len(paths) != 1 {
			return false, err
		}
		info, err := os.Stat(paths[0])
		if err != nil || info.ModTime().Unix() != 0 {
			return false, err
		}
		// The real copier has finished and normalized the file, but the
		// subsequent snapshot hash has not yet authenticated it.
		return true, os.WriteFile(paths[0], []byte("changed copied input"), 0o644)
	}
	platform := "linux/" + runtime.GOARCH
	adapter := sourceVendorRuntimeFixture(t, platform)
	vendor := &sourceVendor{runtime: adapter, image: "sha256:" + strings.Repeat("b", 64), platform: platform,
		network: ir.NetworkDefault, command: node.VendorCmd}
	opts.Ctx, opts.fetchDirectory = ctx, checkout
	err = ensureSourceClosure(node, checkoutPath, opts, func() error {
		return vendor.run(ctx, workspace, checkout, io.Discard, io.Discard)
	})
	if !ctx.hit || ctx.err != nil || code(err) != e.CodeFetchFailed || !strings.Contains(err.Error(), "authenticating vendor input") {
		t.Fatalf("copied input comparison not reached: mutated=%t mutation=%v guard=%v", ctx.hit, ctx.err, err)
	}
	assertVendorReceiptAbsent(t, node, opts)
	for _, command := range adapter.commands {
		if command[0] == "volume" || command[0] == "create" || command[0] == "cp" || command[0] == "start" {
			t.Fatalf("unverified source copy reached mutable runtime work: %v", adapter.commands)
		}
	}
	if got, err := os.ReadFile(filepath.Join(checkoutPath, "input")); err != nil || string(got) != "source bytes" {
		t.Fatalf("rejected copied bytes changed original checkout: %q / %v", got, err)
	}
	entries, err := os.ReadDir(workspacePath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected copied source leaked staging: %v / %v", entries, err)
	}
}

func TestSourceVendorPartialCopyAndFinalDigestFailuresCannotIssueReceipt(t *testing.T) {
	for _, test := range []struct{ name, diagnostic string }{
		{"late unreadable member", "copying validated vendor closure"},
		{"late member mutation", "authenticating vendor copy-back"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "late unreadable member" && os.Geteuid() == 0 {
				t.Skip("permission refusal must execute as a non-root user")
			}
			node, checkoutPath, opts := pristineClosureFixture(t)
			node.VendorCmd = []string{"dependency", "build"}
			node.VendorImage = "registry.example/vendor@sha256:" + strings.Repeat("1", 64)
			checkout, _, err := securepath.OpenDirectory(checkoutPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			returned := t.TempDir()
			for _, name := range []string{"a", "z"} {
				if err := os.WriteFile(filepath.Join(returned, name), []byte("pinned member"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := sourceTreeDigestContext(t.Context(), checkoutPath)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &vendorCopyMutationContext{Context: t.Context()}
			ctx.mutate = func() (bool, error) {
				first := filepath.Join(checkoutPath, "a")
				info, err := os.Stat(first)
				if os.IsNotExist(err) || err == nil && info.ModTime().Unix() != 0 {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				if test.name == "late unreadable member" {
					return true, os.Chmod(filepath.Join(returned, "z"), 0)
				}
				return true, os.WriteFile(filepath.Join(returned, "z"), []byte("changed bytes"), 0o644)
			}
			opts.Ctx, opts.fetchDirectory = ctx, checkout
			err = ensureSourceClosure(node, checkoutPath, opts, func() error {
				return replaceVendoredSource(ctx, checkout, returned, before)
			})
			if !ctx.hit || ctx.err != nil {
				t.Fatalf("fixture did not mutate after a completed first copy: hit=%t err=%v", ctx.hit, ctx.err)
			}
			if code(err) != e.CodeFetchFailed || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("partial copy/final digest guard lost: %v", err)
			}
			assertVendorReceiptAbsent(t, node, opts)
			if got, err := os.ReadFile(filepath.Join(checkoutPath, "a")); err != nil || string(got) != "pinned member" {
				t.Fatalf("fixture did not reach partial copy-back: %q / %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(checkoutPath, "input")); !os.IsNotExist(err) {
				t.Fatalf("fixture did not pass original source replacement: %v", err)
			}
			if _, err := os.Stat(filepath.Join(checkoutPath, ".git", "HEAD")); err != nil {
				t.Fatalf("partial failure damaged original Git metadata: %v", err)
			}
			called := false
			opts.Ctx = t.Context()
			err = ensureSourceClosure(node, checkoutPath, opts, func() error { called = true; return nil })
			if called || code(err) != e.CodeFetchFailed || !strings.Contains(err.Error(), "unreceipted") {
				t.Fatalf("partial unreceipted checkout was reused: vendor=%t err=%v", called, err)
			}
			assertVendorReceiptAbsent(t, node, opts)
		})
	}
}

func assertVendorReceiptAbsent(t *testing.T, node ir.Node, opts RunOptions) {
	t.Helper()
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed vendor operation issued a receipt: %v", err)
	}
}
