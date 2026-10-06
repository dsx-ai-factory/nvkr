//go:build linux

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"golang.org/x/sys/unix"
)

// These tests exercise the real descriptor, archive, copy-back and receipt
// owners with a modeled daemon. They are not a real Docker/Helm execution.
func vendorResultFixture(t *testing.T, kind string) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, entry := range []struct{ name, content string }{{"source/input", "vendored input"}, {"source/dependency", "pinned dependency"}} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(entry.content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, entry.content); err != nil {
			t.Fatal(err)
		}
	}
	var extra *tar.Header
	switch kind {
	case "Git directory":
		extra = &tar.Header{Name: "source/.git", Mode: 0o755, Typeflag: tar.TypeDir}
	case "Git symlink":
		extra = &tar.Header{Name: "source/.git", Linkname: "input", Typeflag: tar.TypeSymlink}
	case "escaped symlink":
		extra = &tar.Header{Name: "source/escape", Linkname: "../../outside", Typeflag: tar.TypeSymlink}
	case "unexpected root":
		extra = &tar.Header{Name: "outside", Typeflag: tar.TypeReg}
	}
	if extra != nil {
		if err := writer.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestSourceVendorHeldTransportAndReceipt(t *testing.T) {
	for _, kind := range []string{"success", "Git directory", "Git symlink", "escaped symlink", "unexpected root", "source mutation", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			node, dir, opts := pristineClosureFixture(t)
			node.VendorCmd = []string{"dependency", "build", "controller/deploy/charts/controller"}
			node.VendorImage = "registry.example/helm@sha256:" + strings.Repeat("1", 64)
			node.VendorSpecDigest, _ = ir.VendorSpecIdentity(node.VendorCmd, node.VendorImage)
			checkout, _, err := securepath.OpenDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer checkout.Close()
			workspacePath := t.TempDir()
			workspace, _, err := securepath.OpenDirectory(workspacePath)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			platform := "linux/" + runtime.GOARCH
			r := sourceVendorRuntimeFixture(t, platform)
			r.result = vendorResultFixture(t, kind)
			vendor := &sourceVendor{runtime: r, image: "sha256:" + strings.Repeat("b", 64), platform: platform,
				network: ir.NetworkDefault, command: append([]string(nil), node.VendorCmd...)}
			switch kind {
			case "source mutation":
				r.beforeStart = func() error { return os.WriteFile(filepath.Join(dir, "input"), []byte("concurrent mutation"), 0o644) }
			case "cancellation":
				r.beforeStart = func() error { cancel(); return ctx.Err() }
			}
			gitBefore, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
			if err != nil {
				t.Fatal(err)
			}
			opts.Ctx, opts.fetchDirectory = ctx, checkout
			err = ensureSourceClosure(node, dir, opts, func() error {
				return vendor.run(ctx, workspace, checkout, io.Discard, io.Discard)
			})
			if kind == "success" {
				if err != nil {
					t.Fatal(err)
				}
				for path, want := range map[string]string{"input": "vendored input", "dependency": "pinned dependency"} {
					got, err := os.ReadFile(filepath.Join(dir, path))
					if err != nil || string(got) != want {
						t.Fatalf("vendored source %s: %q / %v", path, got, err)
					}
				}
				receiptPath, _ := sourceReceiptPath(opts.SourceRoot, node.Dest)
				receipt, exists, err := readSourceReceiptContext(t.Context(), receiptPath)
				got, hashErr := sourceTreeDigestContext(t.Context(), dir)
				if err != nil || hashErr != nil || !exists || receipt.ClosureDigest != got || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash || receipt.VendorSpecDigest != node.VendorSpecDigest {
					t.Fatalf("vendor receipt did not bind original source and resulting closure: %+v, %v / %v", receipt, err, hashErr)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe vendor output succeeded")
				}
				receiptPath, _ := sourceReceiptPath(opts.SourceRoot, node.Dest)
				if _, err := os.Stat(receiptPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed vendoring issued a receipt: %v", err)
				}
				if kind != "source mutation" {
					input, err := os.ReadFile(filepath.Join(dir, "input"))
					if err != nil || string(input) != "source bytes" {
						t.Fatalf("rejected output mutated original source: %q / %v", input, err)
					}
				}
				if kind == "cancellation" && code(err) != e.CodeTimeout {
					t.Fatalf("cancellation code lost: %v", err)
				}
			}
			gitAfter, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
			if err != nil || !bytes.Equal(gitBefore, gitAfter) {
				t.Fatalf("original Git directory changed: %q / %v", gitAfter, err)
			}
			reader := tar.NewReader(bytes.NewReader(r.input))
			for {
				header, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil || strings.Contains(header.Name, "/.git/") || strings.HasSuffix(header.Name, "/.git") {
					t.Fatalf("Git metadata or invalid carrier reached vendor: %+v / %v", header, err)
				}
			}
			if len(r.cleaned) != 2 || r.cleaned[0][0] != "rm" || r.cleaned[1][0] != "volume" {
				t.Fatalf("container/volume cleanup lost: %v", r.cleaned)
			}
			entries, err := os.ReadDir(workspacePath)
			if err != nil || len(entries) != 0 {
				t.Fatalf("vendor transport workspace leaked: %v / %v", entries, err)
			}
		})
	}
}

func TestSourceVendorHeldCopyBackRejectsUnsafeTreeBeforeMutation(t *testing.T) {
	for _, kind := range []string{"Git file", "Git directory", "Git symlink", "escape", "FIFO"} {
		t.Run(kind, func(t *testing.T) {
			_, checkoutPath, _ := pristineClosureFixture(t)
			checkout, _, err := securepath.OpenDirectory(checkoutPath)
			if err != nil {
				t.Fatal(err)
			}
			defer checkout.Close()
			before, err := sourceTreeDigestContext(t.Context(), checkoutPath)
			if err != nil {
				t.Fatal(err)
			}
			result := t.TempDir()
			switch kind {
			case "Git file":
				err = os.WriteFile(filepath.Join(result, ".git"), []byte("gitdir: outside"), 0o644)
			case "Git directory":
				err = os.Mkdir(filepath.Join(result, ".git"), 0o755)
			case "Git symlink":
				err = os.Symlink("safe", filepath.Join(result, ".git"))
			case "escape":
				err = os.Symlink("../outside", filepath.Join(result, "escaped"))
			case "FIFO":
				err = unix.Mkfifo(filepath.Join(result, "fifo"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = replaceVendoredSource(t.Context(), checkout, result, before)
			if code(err) != e.CodeFetchFailed {
				t.Fatalf("unsafe returned source admitted: %v", err)
			}
			after, err := sourceTreeDigestContext(t.Context(), checkoutPath)
			if err != nil || after != before {
				t.Fatalf("unsafe result altered original checkout: %s / %v", after, err)
			}
		})
	}
}

func TestSourceFetchGitDirectoryReplacementUsesOriginalDescriptor(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	_, repository, _ := pristineClosureFixture(t)
	t.Chdir(repository)
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	outside, commands := t.TempDir(), t.TempDir()
	foreign := filepath.Join(outside, "foreign")
	if err := os.Mkdir(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(repository, "src", "source")
	// The wrapper changes only the pathname, then delegates to actual Git. Both
	// inherited directory descriptors remain open through real init and join.
	script := fmt.Sprintf("#!/bin/sh\nset -eu\n/bin/mv %q %q\n/bin/ln -s %q %q\nexec %q \"$@\"\n",
		filepath.Join(checkout, ".git"), filepath.Join(outside, "original-git"), foreign, filepath.Join(checkout, ".git"), git)
	if err := os.WriteFile(filepath.Join(commands, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commands)
	node := testFetchNode("fetch-source", "https://example.invalid/source", testSourceCommit, testSourceTree,
		"source", "registry.example/toolchain@sha256:"+strings.Repeat("c", 64), "")
	err = (Local{}).fetch(node, RunOptions{Ctx: t.Context(), SourceWorkspace: workspace, SourceRoot: "src",
		Stdout: io.Discard, Stderr: io.Discard})
	if code(err) != e.CodeFetchFailed {
		t.Fatalf("Git directory substitution was not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "original-git", "HEAD")); err != nil {
		t.Fatalf("actual Git did not initialize the retained original directory: %v", err)
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatalf("real Git wrote into substituted metadata path: %v / %v", entries, err)
	}
	receiptPath, _ := sourceReceiptPath("src", node.Dest)
	if _, err := os.Stat(receiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("substituted source metadata issued receipt: %v", err)
	}
}

func TestSourceFetchHeldGitDisablesRepositoryHookCredentialAccess(t *testing.T) {
	node, dir, _ := pristineClosureFixture(t)
	checkout, _, err := securepath.OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	git, _, err := securepath.OpenChild(checkout, ".git", true)
	if err != nil {
		t.Fatal(err)
	}
	defer git.Close()
	marker := filepath.Join(t.TempDir(), "repository-hook-executed")
	hook := fmt.Sprintf("#!/bin/sh\nif test -n \"${NKD_SOURCE_TOKEN:-}\"; then printf exposed > %q; else printf executed > %q; fi\n", marker, marker)
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	options := RunOptions{Ctx: t.Context(), fetchDirectory: checkout, fetchGitDirectory: git,
		SourceGitEnvironment: []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
			"NKD_SOURCE_TOKEN=test-only-synthetic-source-token"}}
	if _, err := sourceFetchGitOut(options, dir, "checkout", "--detach", node.Commit); err != nil {
		t.Fatalf("actual held Git checkout failed: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository hook ran in selected credential environment: %v", err)
	}
}

func TestSourceFetchHeldGitDisablesRepositoryFilesystemMonitorCredentialAccess(t *testing.T) {
	_, dir, _ := pristineClosureFixture(t)
	checkout, _, err := securepath.OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	metadata, _, err := securepath.OpenChild(checkout, ".git", true)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	marker := filepath.Join(t.TempDir(), "repository-monitor-executed")
	monitor := filepath.Join(t.TempDir(), "fsmonitor")
	script := fmt.Sprintf("#!/bin/sh\nif test -n \"${NKD_SOURCE_TOKEN:-}\"; then printf exposed > %q; else printf executed > %q; fi\nprintf 'controlled-clock\\000'\n", marker, marker)
	if err := os.WriteFile(monitor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "git", "-C", dir, "config", "core.fsmonitor", monitor)
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("configuring controlled repository monitor: %v %s", err, raw)
	}
	options := RunOptions{Ctx: t.Context(), fetchDirectory: checkout, fetchGitDirectory: metadata,
		SourceGitEnvironment: []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
			"NKD_SOURCE_TOKEN=test-only-synthetic-source-token"}}
	if _, err := sourceFetchGitOut(options, dir, "status", "--porcelain", "--untracked-files=all", "--ignored"); err != nil {
		t.Fatalf("actual held Git status failed: %v", err)
	}
	if raw, err := os.ReadFile(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository monitor ran in selected credential environment: marker=%q, error=%v", raw, err)
	}
}

func TestSourceFetchProductionOwnerRejectsMidFetchParentSubstitution(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	node, repository, _ := pristineClosureFixture(t)
	node.Repo = repository
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	outside, commands := t.TempDir(), t.TempDir()
	original, foreign := filepath.Join(outside, "original"), filepath.Join(outside, "foreign")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(commands, "swapped")
	// Only the first command swaps the public parent, then every command uses
	// actual Git with the production owner's retained checkout and metadata FDs.
	script := fmt.Sprintf("#!/bin/sh\nset -eu\nif test ! -e %q; then\n/bin/mv .nkd %q\n/bin/ln -s %q .nkd\nprintf done > %q\nfi\nexec %q \"$@\"\n", marker, original, foreign, marker, git)
	if err := os.WriteFile(filepath.Join(commands, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	opts := withDefaults(RunOptions{Ctx: ctx, SourceRoot: ".nkd/src", SourceWorkspace: workspace,
		Stdout: io.Discard, Stderr: io.Discard})
	if err := (Local{}).fetch(node, opts); code(err) != e.CodeFetchFailed {
		t.Fatalf("substituted production output parent succeeded: %v", err)
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatalf("real Git wrote through substituted parent: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(original, "src", node.Dest, "input")); err != nil {
		t.Fatalf("real Git did not finish against the original held checkout: %v", err)
	}
	receipt, _ := sourceReceiptPath(filepath.Join(original, "src"), node.Dest)
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed source parent issued a receipt: %v", err)
	}
}
