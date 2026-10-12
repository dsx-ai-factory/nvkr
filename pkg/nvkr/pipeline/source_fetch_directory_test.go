// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestOrdinaryFetchFailureExplainsCredentialPolicy(t *testing.T) {
	for _, operation := range []string{"fetch", "status", "canceled"} {
		t.Run(operation, func(t *testing.T) {
			tools := t.TempDir()
			if err := os.WriteFile(filepath.Join(tools, "git"), []byte("#!/bin/sh\nprintf synthetic-private-credential >&2\nexit 128\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("GIT_CONFIG_COUNT", "")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			selected := operation
			if operation == "canceled" {
				cancel()
				selected = "fetch"
			}
			_, err := gitOut(ctx, t.TempDir(), selected)
			wantCode := e.Code("")
			switch operation {
			case "fetch":
				wantCode = e.CodeFetchFailed
			case "canceled":
				wantCode = e.CodeTimeout
			}
			if err == nil || code(err) != wantCode || strings.Contains(err.Error(), "local credential policy:") != (operation == "fetch") {
				t.Fatalf("fetch diagnosis or cancellation code changed: %v", err)
			}
			if operation == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation cause lost")
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 128 {
					t.Fatal("Git exit status lost")
				}
			}
			if strings.Contains(err.Error(), "synthetic-private-credential") {
				t.Fatal("fetch diagnosis exposed private child output")
			}
		})
	}
}

func TestSourceFetchPreservesGitAdmissionFailures(t *testing.T) {
	for _, boundary := range []string{"fetch probe", "exec-node probe", "verify tree", "closure cleanliness"} {
		t.Run(boundary, func(t *testing.T) {
			node, dir, opts := pristineClosureFixture(t)
			cmd := exec.CommandContext(t.Context(), "git", "-C", dir, "config", "core.sshCommand", "unsupported")
			if raw, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, raw)
			}
			var err error
			switch boundary {
			case "fetch probe":
				err = (Local{}).fetch(node, withDefaults(opts))
			case "exec-node probe":
				err = (Local{}).execFetch(node, withDefaults(opts))
			case "verify tree":
				err = (Local{}).verifyTree(dir, node, opts)
			case "closure cleanliness":
				err = ensureSourceClosure(node, dir, opts, nil)
			}
			if code(err) != e.CodeEngineCapability || e.ExitCode(err) != 29 {
				t.Fatalf("source boundary lost admission code: %v", err)
			}
		})
	}
}

func TestSourceGitBoundaryFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"capability", e.New(e.CodeEngineCapability, "fixture"), true},
		{"bounded query timeout", e.New(e.CodeTimeout, "fixture"), true},
		{"wrapped capability", fmt.Errorf("fixture: %w", e.New(e.CodeEngineCapability, "fixture")), true},
		{"ordinary fetch", e.New(e.CodeFetchFailed, "fixture"), false},
		{"ordinary tool exit", errors.New("fixture"), false},
		{"success", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sourceGitBoundaryError(test.err); got != test.want {
				t.Fatalf("boundary failure=%t want=%t", got, test.want)
			}
		})
	}
}

func TestSourceFetchDoesNotTolerateRemoteAddPolicyRefusal(t *testing.T) {
	for _, boundary := range []string{"local", "exec-node"} {
		t.Run(boundary, func(t *testing.T) {
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			toolDir := t.TempDir()
			root := t.TempDir()
			dir := filepath.Join(root, "source")
			marker := filepath.Join(toolDir, "initialized")
			// Model repository configuration appearing after init. Git remains
			// the real config parser; the wrapper changes only this fixture.
			body := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" init "*)
    %q "$@" || exit $?
    %q -C %q config core.sshCommand unsupported || exit $?
    : > %q
    exit 0
    ;;
esac
exec %q "$@"
`, realGit, realGit, dir, marker, realGit)
			if err := os.WriteFile(filepath.Join(toolDir, "git"), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			node := ir.Node{ID: "fetch-source", Dest: "source", Repo: "https://example.test/source", Commit: strings.Repeat("a", 40)}
			opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: root, Stdout: io.Discard, Stderr: io.Discard})
			if boundary == "local" {
				err = (Local{}).fetch(node, opts)
			} else {
				err = (Local{}).execFetch(node, opts)
			}
			if code(err) != e.CodeEngineCapability || e.ExitCode(err) != 29 {
				t.Fatalf("remote-add admission was ignored or recoded: %v", err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("fixture did not reach init before remote-add admission: %v", err)
			}
		})
	}
}

func holdSourceFetchDirectory(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening source fetch directory")
	}
	root, relative := ".", path
	if filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, e.Wrap(e.CodeFetchFailed, err, "locating source fetch workspace")
		}
		relative, err = filepath.Rel(cwd, path)
		if err != nil {
			return nil, e.Wrap(e.CodeFetchFailed, err, "locating source fetch directory")
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			// Local callers can select an absolute workspace outside cwd. Hold its
			// nearest existing directory as the explicit root; generated Plan paths
			// instead stay beneath cwd and never use this branch.
			root, relative = path, ""
			for {
				info, statErr := os.Lstat(root)
				if statErr == nil {
					if !info.IsDir() {
						return nil, e.New(e.CodeFetchFailed, "source fetch workspace is not a real directory")
					}
					break
				}
				if !os.IsNotExist(statErr) {
					return nil, e.Wrap(e.CodeFetchFailed, statErr, "locating source fetch workspace")
				}
				relative = filepath.Join(filepath.Base(root), relative)
				root = filepath.Dir(root)
			}
		}
	}
	// CreateParent opens/creates every component with no-follow descriptor access;
	// the final placeholder is never created or used as a source member.
	directory, _, err := securepath.CreateParentBeneath(root, filepath.Join(relative, ".nvkr-held-directory"))
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "holding safe source fetch directory")
	}
	return directory, nil
}

func TestSourceFetchHeldGitDoesNotInheritAmbientCredentials(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual held Git child execution requires Linux")
	}
	checkout, err := holdSourceFetchDirectory(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	commands := t.TempDir()
	if err := os.WriteFile(filepath.Join(commands, "git"), []byte("#!/bin/sh\nexec /usr/bin/env\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := commands + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", path)
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "ACTIONS_RUNTIME_TOKEN", "REGISTRY_AUTH_FILE", "HTTPS_PROXY", "UNSELECTED_SECRET"} {
		t.Setenv(key, "ambient-secret")
	}
	for _, operation := range []string{"fetch", "init", "checkout", "status"} {
		opts := RunOptions{Ctx: t.Context(), fetchDirectory: checkout, fetchRepository: "https://example.test/source",
			SourceGitEnvironment: sourceGitPrivateTestEnvironment(t, "https://example.test/source", path)}
		output, err := sourceFetchGitOut(opts, ".", operation)
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(output, "\n")
		if strings.Contains(output, "ambient-secret") || !slices.Contains(fields, "GIT_CONFIG_GLOBAL=/dev/null") || !slices.Contains(fields, "GIT_CONFIG_NOSYSTEM=1") {
			t.Fatal("held Git child inherited ambient state or lacked its fixed baseline")
		}
		if slices.Contains(fields, "NVKR_SOURCE_TOKEN=test-only-synthetic-source-token") != (operation == "fetch") {
			t.Fatal("selected source credential escaped its network-fetch-only child scope")
		}
		if !slices.Contains(fields, "HOME=/proc/self/fd/4/.") || !slices.Contains(fields, "XDG_CONFIG_HOME=/proc/self/fd/4/.") {
			t.Fatal("Git child did not receive its owned empty home descriptor")
		}
	}
}

func TestSourceFetchHeldDirectoryAndReceiptSurviveParentSubstitution(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held Git directory execution is Linux-only")
	}
	node, dir, opts := pristineClosureFixture(t)
	checkout, err := holdSourceFetchDirectory(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	opts.fetchDirectory = checkout
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := holdSourceFetchDirectory(t.Context(), filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer receipts.Close()
	opts.fetchReceiptDirectory = receipts
	parent := filepath.Dir(opts.SourceRoot)
	heldParent := parent + "-held"
	outside := t.TempDir()
	if err := os.Rename(parent, heldParent); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(parent)
		_ = os.Rename(heldParent, parent)
	}()
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceFetchGitOut(opts, dir, "config", "nvkr.held", "true"); err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			t.Logf("controlled Git diagnostic: %s", failure.Stderr)
		}
		t.Fatal(err)
	}
	if err := ensureSourceClosure(node, dir, opts, nil); err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), path, receipts)
	if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
		t.Fatalf("held receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("Git or receipt wrote through replacement: %v / %v", entries, err)
	}
}

func TestSourceFetchHeldWorkspacePlatformAdmissionBeforeWork(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("unsupported-platform admission is exercised on other hosts")
	}
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if workspace != nil || code(err) != e.CodeEngineCapability {
		t.Fatalf("unsupported held Git route: %v", err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsupported platform created output: %v / %v", entries, err)
	}
}

func TestSourceFetchHeldWorkspaceRealLocalGit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held Git directory execution is Linux-only")
	}
	node, repository, _ := pristineClosureFixture(t)
	node.Repo = repository
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: "src", SourceWorkspace: workspace, Stdout: io.Discard, Stderr: io.Discard})
	for range 2 {
		if err := (Local{}).fetch(node, opts); err != nil {
			t.Fatal(err)
		}
	}
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), path)
	if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
		t.Fatalf("held fetch receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}
}

func TestSourceFetchRealLocalGitCreatesAndReusesReceipt(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("ordinary Local fetch compatibility is exercised on Linux and Darwin")
	}
	node, repository, _ := pristineClosureFixture(t)
	node.Repo = repository
	opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: filepath.Join(t.TempDir(), "src"), Stdout: io.Discard, Stderr: io.Discard})
	for range 2 {
		if err := (Local{}).fetch(node, opts); err != nil {
			t.Fatal(err)
		}
	}
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), path)
	if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
		t.Fatalf("real fetch receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}
}

func TestSourceFetchHeldCheckoutDoesNotReuseEnclosingRepository(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual held Git execution requires Linux")
	}
	node, repository, _ := pristineClosureFixture(t)
	node.Repo = repository
	t.Chdir(repository)
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: "src", SourceWorkspace: workspace, Stdout: io.Discard, Stderr: io.Discard})
	if err := (Local{}).fetch(node, opts); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join("src", node.Dest)
	if info, err := os.Stat(filepath.Join(checkout, ".git")); err != nil || !info.IsDir() {
		t.Fatalf("fresh held source lacks its own Git directory: %v", err)
	}
	for _, name := range []string{"input", ".gitignore"} {
		if bytes, err := os.ReadFile(filepath.Join(checkout, name)); err != nil || len(bytes) == 0 {
			t.Fatalf("fresh held fetch reused the enclosing repo without source member %s: %v", name, err)
		}
	}
}

func TestSourceFetchHeldWorkspaceRejectsUnsafeRelativePathsBeforeCreation(t *testing.T) {
	for _, path := range []string{"/absolute", "../outside", "a/../b"} {
		t.Run(path, func(t *testing.T) {
			t.Chdir(t.TempDir())
			workspace, _, err := securepath.OpenDirectory(".")
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			directory, err := holdSourceFetchWorkspaceDirectory(t.Context(), workspace, path)
			if code(err) != e.CodeEngineCapability || directory != nil {
				t.Fatalf("unsafe held path returned directory=%v, error=%v", directory, err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("unsafe held path created output: %v, %v", entries, err)
			}
		})
	}
}

func TestSourceFetchHeldWorkspaceMisuseFailsBeforeWork(t *testing.T) {
	for _, misuse := range []string{"build", "not launched", "qualified", "whole graph", "environment-only whole graph", "environment without workspace"} {
		t.Run(misuse, func(t *testing.T) {
			issued := misuse == "build" || misuse == "not launched" || misuse == "qualified"
			if issued && runtime.GOOS != "linux" {
				t.Skip("misuse of an issued source workspace requires Linux")
			}
			t.Chdir(t.TempDir())
			workspace := &SourceWorkspace{}
			var err error
			if issued {
				workspace, err = HoldSourceWorkspace(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = workspace.Close() })
			}
			node := testFetchNode("fetch-source", "https://example.test/source", testSourceCommit, testSourceTree,
				"source", "registry.example/toolchain@sha256:"+repeatHex("c"), "")
			opts := RunOptions{Ctx: t.Context(), SourceRoot: "src", SourceWorkspace: workspace, Launch: true}
			switch misuse {
			case "build":
				node = completeTestBuild(ir.Node{ID: "build-source", Kind: ir.KindBuild, Component: "source"}, node)
			case "not launched":
				opts.Launch = false
			case "qualified":
				opts.Qualification = &QualifiedExecution{}
			case "environment without workspace", "environment-only whole graph":
				opts.SourceWorkspace = nil
				opts.SourceGitEnvironment = []string{"LC_ALL=C"}
			}
			if err := node.Validate(); err != nil {
				t.Fatalf("misuse fixture must otherwise be a valid producer: %v", err)
			}
			if misuse == "whole graph" || misuse == "environment-only whole graph" {
				_, err = (Local{}).Run(nil, "", opts)
			} else {
				_, err = (Local{}).ExecNode(node, opts)
			}
			if code(err) != e.CodeEngineCapability {
				t.Fatalf("held capability misuse admitted: %v", err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("misuse created output: %v, %v", entries, err)
			}
		})
	}
}

func TestSourceFetchHeldReceiptDirectoryRejectsSubstitutionBeforeGit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual held source fetch requires Linux")
	}
	node, repository, _ := pristineClosureFixture(t)
	node.Repo = repository
	t.Chdir(t.TempDir())
	outside := t.TempDir()
	if err := os.Symlink(outside, "source-receipts"); err != nil {
		t.Fatal(err)
	}
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	err = (Local{}).fetch(node, RunOptions{Ctx: t.Context(), SourceRoot: "src", SourceWorkspace: workspace})
	if code(err) != e.CodeFetchFailed {
		t.Fatalf("substituted receipt root admitted: %v", err)
	}
	for _, root := range []string{outside, filepath.Join("src", node.Dest)} {
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("receipt rejection allowed Git or outside output: %v, %v", entries, err)
		}
	}
}

func TestSourceFetchOrdinaryLocalWorkspaceCompatibility(t *testing.T) {
	for _, layout := range []string{"absolute linked parent", "relative linked parent", "relative parent"} {
		t.Run(layout, func(t *testing.T) {
			node, repository, _ := pristineClosureFixture(t)
			node.Repo = repository
			base := t.TempDir()
			cwd := filepath.Join(base, "cwd")
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(cwd)
			root := filepath.Join(base, "linked", "src")
			if layout == "relative parent" {
				root = filepath.Join("..", "shared", "src")
			} else {
				link := filepath.Join(base, "linked")
				if layout == "relative linked parent" {
					link = filepath.Join(cwd, "linked")
					root = filepath.Join("linked", "src")
				}
				if err := os.Symlink(t.TempDir(), link); err != nil {
					t.Fatal(err)
				}
			}
			opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: root, Stdout: io.Discard, Stderr: io.Discard})
			for range 2 {
				if err := (Local{}).fetch(node, opts); err != nil {
					t.Fatal(err)
				}
			}
			path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
			if err != nil {
				t.Fatal(err)
			}
			receipt, exists, err := readSourceReceiptContext(t.Context(), path)
			if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
				t.Fatalf("ordinary receipt mismatch: %+v, exists=%t, error=%v", receipt, exists, err)
			}
		})
	}
}
