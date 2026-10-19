// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestValidatePinSetFilePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		good bool
	}{
		{"controller/deploy/nke-sbom/nke-deploy.yaml", true},
		{"Chart.lock", true}, {"with spaces/[literal]*.yaml", true}, {".versions.yaml", true},
		{"", false}, {".", false}, {"..", false}, {"../Chart.lock", false},
		{"charts/../Chart.lock", false}, {"charts/./Chart.lock", false},
		{"charts//Chart.lock", false}, {"charts/", false}, {"/etc/passwd", false},
		{".git/config", false}, {"charts/.GiT/config", false}, {"C:/file", false},
		{`charts\Chart.lock`, false}, {":(glob)**", false}, {"charts/line\nfeed", false},
		{"charts/\x00", false}, {"charts/\x7f", false}, {"charts/\u0085", false},
		{"\xff", false}, {strings.Repeat("a", 4097), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePinSetFilePath(tc.name)
			if tc.good {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
}

func TestPinnedTreeUsesLockedCommitAndLiteralRegularBlobs(t *testing.T) {
	repo, pin, run := pinTreeFixture(t, map[string][]byte{
		"charts/Chart.lock":      []byte("version: 1.2.3\n"),
		"charts/[literal]*.yaml": []byte("literal\n"),
		"charts/matching.yaml":   []byte("wrong file\n"),
		"empty":                  nil,
	})
	run("tag", "controller/v1.0.0")
	if err := os.WriteFile(filepath.Join(repo, "charts/Chart.lock"), []byte("version: 9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "move upstream")
	run("tag", "-f", "controller/v1.0.0")
	reader := openTestPinnedTree(t, repo, pin)
	for _, tc := range []struct{ name, want string }{
		{"charts/Chart.lock", "version: 1.2.3\n"},
		{"charts/[literal]*.yaml", "literal\n"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reader.ReadFile(t.Context(), tc.name)
			if err != nil || string(got) != tc.want {
				t.Fatalf("ReadFile = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, name := range []string{"../charts/Chart.lock", "charts", "missing", "charts/*.yaml"} {
		t.Run("reject "+name, func(t *testing.T) {
			_, err := reader.ReadFile(t.Context(), name)
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
		})
	}
	private := reader.(*gitPinnedTree).dir
	if _, err := os.Stat(filepath.Join(private, "charts")); !os.IsNotExist(err) {
		t.Fatalf("source was checked out: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatalf("private repository survived Close: %v", err)
	}
	_, err := reader.ReadFile(t.Context(), "empty")
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
}

func TestPinnedTreeRejectsMetadataDisagreement(t *testing.T) {
	repo, pin, run := pinTreeFixture(t, map[string][]byte{"Chart.lock": []byte("fixture\n")})
	blob := run("rev-parse", "HEAD:Chart.lock")
	for _, tc := range []struct {
		name string
		edit func(*Pin)
		code e.Code
	}{
		{"wrong tree", func(p *Pin) { p.TreeHash = strings.Repeat("a", 40) }, e.CodeInvalidCatalog},
		{"wrong epoch", func(p *Pin) { p.SourceDateEpoch++ }, e.CodeInvalidCatalog},
		{"zero epoch", func(p *Pin) { p.SourceDateEpoch = 0 }, e.CodeInvalidCatalog},
		{"invalid commit", func(p *Pin) { p.Commit = "HEAD" }, e.CodeInvalidCatalog},
		{"invalid tree", func(p *Pin) { p.TreeHash = "HEAD^{tree}" }, e.CodeInvalidCatalog},
		{"noncommit object", func(p *Pin) { p.Commit = blob }, e.CodeInvalidCatalog},
		{"missing commit", func(p *Pin) { p.Commit = strings.Repeat("a", 40) }, e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := pin
			tc.edit(&candidate)
			reader, err := openPinnedTree(t.Context(), Source{Repo: repo}, candidate)
			if reader != nil {
				_ = reader.Close()
				t.Fatal("failed open returned a tree")
			}
			assertCatalogContractCode(t, err, tc.code)
		})
	}
}

func TestPinnedTreeRejectsSymlinksAndGitlinks(t *testing.T) {
	repo, pin, run := pinTreeFixture(t, map[string][]byte{"regular": []byte("fixture\n")})
	if err := os.Symlink("regular", filepath.Join(repo, "symlink")); err != nil {
		t.Fatal(err)
	}
	run("add", "symlink")
	run("update-index", "--add", "--cacheinfo", "160000,"+pin.Commit+",submodule")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "nonregular entries")
	pin.Commit, pin.TreeHash = run("rev-parse", "HEAD"), run("rev-parse", "HEAD^{tree}")
	reader := openTestPinnedTree(t, repo, pin)
	for _, name := range []string{"symlink", "submodule", "submodule/regular"} {
		t.Run(name, func(t *testing.T) {
			_, err := reader.ReadFile(t.Context(), name)
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
		})
	}
}

func TestPinnedTreeResourceLimits(t *testing.T) {
	repo, pin, _ := pinTreeFixture(t, map[string][]byte{
		"limit":    bytes.Repeat([]byte("a"), pinTreeFileLimit),
		"oversize": bytes.Repeat([]byte("b"), pinTreeFileLimit+1),
		"small":    []byte("small"), "empty": nil,
	})
	t.Run("file", func(t *testing.T) {
		reader := openTestPinnedTree(t, repo, pin)
		_, err := reader.ReadFile(t.Context(), "oversize")
		assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
		if reader.(*gitPinnedTree).total != 0 {
			t.Fatal("oversized blob body was read")
		}
	})
	t.Run("total", func(t *testing.T) {
		reader := openTestPinnedTree(t, repo, pin)
		for range pinTreeTotalLimit / pinTreeFileLimit {
			got, err := reader.ReadFile(t.Context(), "limit")
			if err != nil || len(got) != pinTreeFileLimit {
				t.Fatalf("limit read: bytes=%d, err=%v", len(got), err)
			}
		}
		_, err := reader.ReadFile(t.Context(), "small")
		assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
	})
	t.Run("reads", func(t *testing.T) {
		reader := openTestPinnedTree(t, repo, pin)
		// Exercise the boundary without thousands of identical Git invocations.
		reader.(*gitPinnedTree).reads = pinTreeReadLimit - 1
		if _, err := reader.ReadFile(t.Context(), "empty"); err != nil {
			t.Fatal(err)
		}
		_, err := reader.ReadFile(t.Context(), "empty")
		assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
	})
	t.Run("process capture", func(t *testing.T) {
		_, err := pinTreeGit(t.Context(), repo, 4, "show", "HEAD:small")
		assertCatalogContractCode(t, err, e.CodeFetchFailed)
	})
}

func TestPinnedTreeCancellation(t *testing.T) {
	repo, pin, _ := pinTreeFixture(t, map[string][]byte{"Chart.lock": []byte("fixture\n")})
	reader := openTestPinnedTree(t, repo, pin)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := openPinnedTree(ctx, Source{Repo: "must-not-contact.invalid"}, pin)
	assertCatalogContractCode(t, err, e.CodeTimeout)
	_, err = reader.ReadFile(ctx, "Chart.lock")
	assertCatalogContractCode(t, err, e.CodeTimeout)
	if reader.(*gitPinnedTree).reads != 0 {
		t.Fatal("canceled read consumed a file-read budget")
	}
	// Cancellation also releases callers waiting on another tree operation.
	tree := reader.(*gitPinnedTree)
	tree.gate <- struct{}{}
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err = reader.ReadFile(ctx, "Chart.lock")
	cancel()
	<-tree.gate
	assertCatalogContractCode(t, err, e.CodeTimeout)

	// A running command is canceled and joined, rather than only checking
	// cancellation at admission. This fake tool sleeps; it reads no source data.
	toolDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolDir, "git"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolDir)
	ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
	start := time.Now()
	_, err = reader.ReadFile(ctx, "Chart.lock")
	cancel()
	assertCatalogContractCode(t, err, e.CodeTimeout)
	if time.Since(start) > 3*time.Second {
		t.Fatal("canceled Git process was not promptly joined")
	}
}

func TestPinnedTreeDoesNotUseAmbientRepository(t *testing.T) {
	repo, pin, _ := pinTreeFixture(t, map[string][]byte{"Chart.lock": []byte("right tree\n")})
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "absent.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(t.TempDir(), "absent.objects"))
	reader := openTestPinnedTree(t, repo, pin)
	got, err := reader.ReadFile(t.Context(), "Chart.lock")
	if err != nil || string(got) != "right tree\n" {
		t.Fatalf("read from private repository: %q, %v", got, err)
	}
}

func pinTreeFixture(t *testing.T, files map[string][]byte) (string, Pin, func(...string) string) {
	t.Helper()
	repo := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_DATE", "1733939955 +0000")
	t.Setenv("GIT_COMMITTER_DATE", "1733939955 +0000")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--template=", "-q")
	run("config", "user.name", "Pinned Tree Test")
	run("config", "user.email", "pin-tree@example.invalid")
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "pinned tree fixture")
	return repo, Pin{Tag: "controller/v1.0.0", Commit: run("rev-parse", "HEAD"), TreeHash: run("rev-parse", "HEAD^{tree}"), SourceDateEpoch: 1733939955}, run
}

func openTestPinnedTree(t *testing.T, repo string, pin Pin) pinnedTree {
	t.Helper()
	reader, err := openPinnedTree(t.Context(), Source{Repo: repo}, pin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	return reader
}

func TestPinnedTreeBoundsUnrelatedObjectAcquisition(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(strconv.FormatBool(oversized), func(t *testing.T) {
			files := map[string][]byte{"versions.yaml": []byte("version: 1.2.3\n")}
			if oversized {
				blob := make([]byte, 256<<10)
				rng := rand.New(rand.NewPCG(1, 2))
				for i := range blob {
					blob[i] = byte(rng.Uint32())
				}
				files["unrelated.bin"] = blob
			}
			repo, pin, _ := pinTreeFixture(t, files)
			reader, err := openPinnedTreeWithLimit(t.Context(), Source{Repo: repo}, pin, 64<<10)
			if oversized {
				assertCatalogContractCode(t, err, e.CodeFetchFailed)
				if reader != nil {
					t.Fatal("oversized repository was admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			body, err := reader.ReadFile(t.Context(), "versions.yaml")
			if err != nil || string(body) != "version: 1.2.3\n" {
				t.Fatalf("read = %q, %v", body, err)
			}
		})
	}
}

func TestPinTreeGitEnforcesLimitDuringWrite(t *testing.T) {
	tools := t.TempDir()
	output := filepath.Join(t.TempDir(), "oversized")
	script := "#!/bin/sh\nexec /bin/dd if=/dev/zero of=\"$PIN_TREE_TEST_OUTPUT\" bs=1024 count=256\n"
	if err := os.WriteFile(filepath.Join(tools, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools)
	t.Setenv("PIN_TREE_TEST_OUTPUT", output)
	_, err := pinTreeGitWithLimit(t.Context(), t.TempDir(), 8192, 64<<10, "fetch")
	assertCatalogContractCode(t, err, e.CodeFetchFailed)
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 || info.Size() > 64<<10 {
		t.Fatalf("kernel write cap failed: %d bytes", info.Size())
	}
}

func TestPinnedTreeStoreAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		ok    bool
	}{
		{"one pack", []string{"pack/a.pack", "pack/a.idx"}, true},
		{"loose object", []string{"aa/object"}, false},
		{"extra pack", []string{"pack/a.pack", "pack/a.idx", "pack/b.pack"}, false},
		{"missing index", []string{"pack/a.pack"}, false},
		{"auxiliary file", []string{"pack/a.pack", "pack/a.idx", "pack/a.rev"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.files {
				name = filepath.Join(dir, "objects", name)
				if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := validatePinTreeStore(t.Context(), dir)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, e.CodeFetchFailed)
			}
		})
	}
}
