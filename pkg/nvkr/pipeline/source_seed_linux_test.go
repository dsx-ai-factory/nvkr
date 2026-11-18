// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"compress/zlib"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestSharedSourceSeedAuthenticatesEachConsumer(t *testing.T) {
	for _, transport := range []bool{false, true} {
		for _, scenario := range []string{"same vendor", "shallow same vendor", "shallow different vendor", "different vendor", "different vendor with hidden index entries", "different vendor with attributes", "different vendor with sparse checkout", "corrupt Git object", "object alternates", "different platform", "unknown platform", "repository mismatch", "commit mismatch", "tree mismatch", "closure drift", "missing receipt", "occupied destination", "symlink destination", "canceled"} {
			t.Run(fmt.Sprintf("%s/carrier=%t", scenario, transport), func(t *testing.T) {
				seed, directory, options := pristineClosureFixture(t)
				if strings.HasPrefix(scenario, "shallow ") {
					parent := seed.Commit
					if _, err := gitOut(t.Context(), directory, "-c", "user.name=Test", "-c", "user.email=test@example.test", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "shallow tip"); err != nil {
						t.Fatal(err)
					}
					var err error
					seed.Commit, err = gitOut(t.Context(), directory, "rev-parse", "HEAD")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(directory, ".git/shallow"), []byte(seed.Commit+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(filepath.Join(directory, ".git/objects", parent[:2], parent[2:]), filepath.Join(t.TempDir(), "absent-parent")); err != nil {
						t.Fatal(err)
					}
				}
				seed.Kind, seed.Repo = ir.KindFetch, "https://example.invalid/source"
				seed.VendorCmd = []string{"modeled-vendor"}
				seed.VendorImage = "registry.example/vendor@sha256:" + repeatHex("a")
				if err := ensureSourceClosure(seed, directory, options, func() error {
					// A vendor may modify tracked files as well as add dependencies.
					if err := os.WriteFile(filepath.Join(directory, "input"), []byte("vendored source"), 0644); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(directory, "vendored"), []byte("dependencies"), 0644)
				}); err != nil {
					t.Fatal(err)
				}
				t.Chdir(filepath.Dir(options.SourceRoot))
				workspace, err := HoldSourceWorkspace(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = workspace.Close() })
				ctx, root, release, err := workspace.borrow(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				consumer := seed
				consumer.ID, consumer.Dest = "consumer-fetch", "consumer-source"
				consumerPath := filepath.Join("consumer", consumer.Dest)
				seedPlatform := "linux/" + runtime.GOARCH
				want := e.CodeFetchFailed
				switch scenario {
				case "same vendor":
					want = ""
				case "shallow same vendor":
					want = ""
					if err := os.WriteFile(filepath.Join(directory, ".git/shallow"), []byte(seed.Commit+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "shallow different vendor", "different vendor", "different vendor with hidden index entries", "different vendor with attributes", "different vendor with sparse checkout":
					want = ""
					consumer.VendorCmd = []string{"different-vendor"}
					if scenario == "different vendor with hidden index entries" {
						if _, err := gitOut(ctx, directory, "update-index", "--skip-worktree", "input"); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "different vendor with attributes" {
						if err := os.WriteFile(filepath.Join(directory, ".git/info/attributes"), []byte("input text eol=crlf\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "different vendor with sparse checkout" {
						if _, err := gitOut(ctx, directory, "config", "core.sparseCheckout", "true"); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(directory, ".git/info/sparse-checkout"), []byte("absent-path\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				case "corrupt Git object":
					object, err := gitOut(ctx, directory, "rev-parse", "HEAD:input")
					if err != nil {
						t.Fatal(err)
					}
					objectPath := filepath.Join(directory, ".git/objects", object[:2], object[2:])
					if err := os.Chmod(objectPath, 0600); err != nil {
						t.Fatal(err)
					}
					file, err := os.OpenFile(objectPath, os.O_WRONLY|os.O_TRUNC, 0600)
					if err != nil {
						t.Fatal(err)
					}
					writer := zlib.NewWriter(file)
					payload := "different source bytes"
					if _, err := fmt.Fprintf(writer, "blob %d\x00%s", len(payload), payload); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				case "object alternates":
					if err := os.WriteFile(filepath.Join(directory, ".git/objects/info/alternates"), []byte(filepath.Join(t.TempDir(), "absent-objects")+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "different platform":
					want = ""
					seedPlatform = "linux/amd64"
					if runtime.GOARCH == "amd64" {
						seedPlatform = "linux/arm64"
					}
				case "unknown platform":
					seedPlatform, want = "", e.CodeEngineCapability
				case "repository mismatch":
					consumer.Repo = "https://example.invalid/other"
				case "commit mismatch":
					consumer.Commit = repeatHex("b")[:40]
				case "tree mismatch":
					consumer.TreeHash = repeatHex("c")[:40]
				case "closure drift":
					if err := os.WriteFile(filepath.Join(directory, "input"), []byte("tampered"), 0644); err != nil {
						t.Fatal(err)
					}
				case "missing receipt":
					path, err := sourceReceiptPath("src", seed.Dest)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path, path+".hidden"); err != nil {
						t.Fatal(err)
					}
				case "occupied destination":
					if err := os.MkdirAll(consumerPath, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(consumerPath, "keep"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink destination":
					if err := os.Symlink("src", "consumer"); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					want = e.CodeTimeout
				}
				if want == "" && transport {
					// Exercise the real job boundary: ordinary source transport excludes
					// Git metadata, so seed owners require the narrow source-seed carrier.
					seedPath := filepath.Join("src", seed.Dest)
					if err := transportarchive.PackSourceSeed(ctx, "seed.tar", ".", seedPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(seedPath, filepath.Join(t.TempDir(), "original")); err != nil {
						t.Fatal(err)
					}
					if err := transportarchive.Unpack(ctx, "seed.tar", ".", []string{seedPath}); err != nil {
						t.Fatal(err)
					}
				}
				reuse := scenario == "same vendor" || scenario == "shallow same vendor"
				err = seedSourceWorkspace(ctx, root, seed, "src", consumer, "consumer", seedPlatform, nil)
				if code(err) != want {
					t.Fatalf("seed result = %v, want code %s", err, want)
				}
				if want != "" {
					return
				}
				path, err := sourceReceiptPath("consumer", consumer.Dest)
				if err != nil {
					t.Fatal(err)
				}
				_, exists, err := readSourceReceiptContext(ctx, path)
				if err != nil || exists != reuse {
					t.Fatalf("consumer receipt = %t / %v", exists, err)
				}
				input, err := os.ReadFile(filepath.Join(consumerPath, "input"))
				if err != nil {
					t.Fatal(err)
				}
				wantInput := "vendored source"
				if !reuse {
					wantInput = "source bytes"
					if _, err := os.Stat(filepath.Join(consumerPath, "vendored")); !os.IsNotExist(err) {
						t.Fatalf("foreign vendor bytes survived: %v", err)
					}
				}
				if string(input) != wantInput {
					t.Fatalf("consumer input = %q, want %q", input, wantInput)
				}
				for _, name := range []string{"attributes", "sparse-checkout"} {
					if _, err := os.Stat(filepath.Join(consumerPath, ".git/info", name)); !os.IsNotExist(err) {
						t.Fatalf("seed %s copied into consumer metadata: %v", name, err)
					}
				}
				vendorCalls := 0
				consumerAbsolute, err := filepath.Abs(consumerPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := ensureSourceClosure(consumer, consumerAbsolute, RunOptions{Ctx: ctx, SourceRoot: "consumer"}, func() error {
					vendorCalls++
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if (vendorCalls == 1) == reuse {
					t.Fatalf("consumer vendor ran %d times", vendorCalls)
				}
			})
		}
	}

}

func TestSharedSourceAdmitsConsumerRuntimeBeforeRestoration(t *testing.T) {
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	node := ir.Node{ID: "consumer", Kind: ir.KindFetch, Repo: "https://example.invalid/source", Dest: "source",
		VendorCmd: []string{"vendor"}, VendorImage: "registry.example/vendor@sha256:" + repeatHex("a")}
	called := false
	err = (Local{}).fetchWithSourceVendorAdmission(node, RunOptions{Ctx: t.Context(), SourceWorkspace: workspace,
		SourceRoot: "consumer", SourceSeed: &SourceSeed{Instruction: node, Root: "absent", Platform: "linux/" + runtime.GOARCH}},
		func(context.Context, ir.Node, string) (*sourceVendor, error) {
			called = true
			return nil, e.New(e.CodeEngineCapability, "consumer runtime refused")
		})
	if !called || code(err) != e.CodeEngineCapability {
		t.Fatalf("consumer admission called=%t, err=%v", called, err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime refusal wrote source files: %v / %v", entries, err)
	}
}
