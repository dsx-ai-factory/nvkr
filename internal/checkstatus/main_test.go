// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

func fixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	// Fixture writes must never inherit another checkout's Git paths.
	local := localGitEnv(t)
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if !slices.Contains(local, key) {
			cmd.Env = append(cmd.Env, e)
		}
	}
	if err := ownedcmd.Run(ctx, cmd); err != nil {
		t.Fatalf("git %v: %s %v", args, out.String(), err)
	}
	return strings.TrimSpace(out.String())
}
func localGitEnv(t *testing.T) []string {
	t.Helper()
	out, err := ownedcmd.Output(t.Context(), "git", "rev-parse", "--local-env-vars")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(out))
}
func write(t *testing.T, repo, path, text string) {
	t.Helper()
	p := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
func commit(t *testing.T, repo string) string {
	t.Helper()
	fixtureGit(t, repo, "add", "-A")
	fixtureGit(t, repo, "commit", "-qm", "change")
	return fixtureGit(t, repo, "rev-parse", "HEAD")
}
func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	fixtureGit(t, repo, "init", "-q", "-b", "main")
	return repo
}
func clearLocalGitEnv(t *testing.T) {
	t.Helper()
	for _, name := range localGitEnv(t) {
		if value, ok := os.LookupEnv(name); ok {
			t.Cleanup(func() { os.Setenv(name, value) })
			os.Unsetenv(name)
		}
	}
}
func TestStatusDiff(t *testing.T) {
	clearLocalGitEnv(t)
	repo := newRepo(t)
	baseFiles := map[string]string{
		ledger + "README.md":                                 "# Architecture implementation and evidence\n",
		ledger + "adr-01-03.md":                              "| Owner | Implemented | Remaining |\n",
		ledger + "adr-21.md":                                 "| Owner | Implemented | Remaining |\n",
		ledger + "adr-17-cli-presentation-and-inspection.md": "## ADR-17 CLI presentation\n",
		"docs/guide.md":                                      "Existing text remains unrecorded.\n",
		"docs/old.md":                                        "Old text remains unproven.\n",
		"docs/moved-from.md":                                 "Old text remains unproven.\nTwo.\nThree.\nFour.\nFive.\n",
		"docs/dash.md":                                       "-- intro\n",
		"docs/straddle.md":                                   "Intro.\nCompletion requires a recorded\nOld tail.\n",
		"docs/three.md":                                      "It requires a\nrecorded\nOld tail.\n",
		"docs/split.md":                                      "The release is\nunrecorded.\n",
		"docs/items.md":                                      "- ADR-02 identity.\n",
		"docs/swap.md":                                       "Plain.\n",
		"outside.txt":                                        "Hosted acceptance remains unrecorded.\n",
	}
	for path, text := range baseFiles {
		write(t, repo, path, text)
	}
	if err := os.Symlink("old.md", filepath.Join(repo, "docs/link.md")); err != nil {
		t.Fatal(err)
	}
	base := commit(t, repo)
	const link = "; link its docs/adr/reconciliation/ row instead"
	type testCase struct {
		name, path, text string
		want             []string
	}
	cases := []testCase{
		{"added", "docs/guide.md", baseFiles["docs/guide.md"] + "Hosted acceptance remains unrecorded.\n", []string{`2: status restated ("remains unrecorded")` + link}},
		{"Unicode whitespace", "docs/unicode.md", "Hosted signing remains\u00a0unproven.\nHosted signing remains\u2003unproven.\nHosted signing remains\u0085unproven.\n", []string{`1: status restated ("remains unproven")` + link, `2: status restated ("remains unproven")` + link, `3: status restated ("remains unproven")` + link}},
		{"wrapped", "README.md", "Intro.\nCompletion requires a recorded\nrun of the workflow.\n", []string{`2: status restated ("requires a recorded run")` + link}},
		{"ADR path", "docs/adr/0021-generation.md", "Its hosted acceptance is unrecorded.\n", []string{`1: status restated ("is unrecorded"); link docs/adr/reconciliation/adr-21.md instead`}},
		{"ADR range", "docs/identity.md", "Identity has not been exercised\nfor ADR-02.\n", []string{`1: status restated ("has not been exercised"); link docs/adr/reconciliation/adr-01-03.md instead`}},
		{"topic is not owner", "docs/cli.md", "The ADR-17 viewer is untested.\n", []string{`1: status restated ("is untested")` + link}},
		{"rename", "docs/moved.md", baseFiles["docs/moved-from.md"] + "Signing is untested.\n", []string{`6: status restated ("is untested")` + link}},
		{"header-like line", "docs/dash.md", "++ Hosted acceptance remains unrecorded.\n", []string{`1: status restated ("remains unrecorded")` + link}},
		{"unverified subject", "docs/cycle.md", "The runtime cycle remains unverified; payloads remain unverified by design.\n", []string{`1: status restated ("cycle remains unverified")` + link}},
		{"category", "docs/category.md", "[Reconciliation](adr/reconciliation.md) owns externally exercised scope.\nThe release was not externally exercised.\n", []string{`2: status restated ("not externally exercised")` + link}},
		{"straddles unchanged", "docs/straddle.md", "Intro.\nCompletion requires a recorded\nrun of the workflow.\n", []string{`3: status restated ("requires a recorded run")` + link}},
		{"three lines", "docs/three.md", "It requires a\nrecorded\nrun of the workflow.\n", []string{`3: status restated ("requires a recorded run")` + link}},
		{"blank boundary", "docs/split.md", "The release is\n\nunrecorded.\n", nil},
		{"neighbor paragraph", "docs/neighbor.md", "ADR-02 owns identity.\n\nHosted signing remains unproven.\n", []string{`3: status restated ("remains unproven")` + link}},
		{"neighbor item", "docs/items.md", baseFiles["docs/items.md"] + "- Hosted signing remains unproven.\n", []string{`2: status restated ("remains unproven")` + link}},
		{"numbered item", "docs/numbered.md", "1. ADR-02 identity.\n2. Hosted signing remains\n   unproven.\n  - ADR-21 note.\n", []string{`2: status restated ("remains unproven")` + link}},
		{"symlink to file", "docs/link.md", "Hosted acceptance remains unrecorded.\n", []string{`1: status restated ("remains unrecorded")` + link}},
		{"space path", "docs/my file.md", "Signing is untested.\n", []string{`1: status restated ("is untested")` + link}},
		{"quoted path", "docs/naïve \"q\".md", "Signing is untested.\n", []string{`1: status restated ("is untested")` + link}},
		{"lookalike", "docs/adr/reconciliation.md", "Hosted signing remains unproven.\n", []string{`1: status restated ("remains unproven")` + link}},
		{"lookalike notes", "docs/adr/reconciliation-notes.md", "Hosted signing remains unproven.\n", []string{`1: status restated ("remains unproven")` + link}},
		{"ledger", ledger + "adr-21.md", baseFiles[ledger+"adr-21.md"] + "Hosted signing remains unproven.\n", nil},
		{"ledger topic", ledger + "native-arc-disk-route.md", "Hosted signing remains unproven.\n", nil},
		{"evidence", "docs/evidence/run.md", "Hosted signing remains unproven.\n", nil},
		{"lane", "docs/lanes.md", "Dry-run stays static-unqualified; local runs remain unqualified.\n", nil},
		{"unchanged", "docs/old.md", baseFiles["docs/old.md"] + "Unrelated.\n", nil},
		{"code", "pkg/x.go", "// remains unrecorded\n", nil},
		{"escaped", "docs/escaped.md", "Hosted publication was exercised. <!-- status-ok: dated log -->\n", nil},
		{"Unicode escape", "docs/unicode-escape.md", "Hosted publication was exercised. <!--\u00a0status-ok:\u2003dated log -->\n", nil},
		{"Unicode blank reason", "docs/unicode-reason.md", "<!-- status-ok:\u00a0\u2003 -->\n", []string{"1: status-ok escape needs a reason: <!-- status-ok: why -->"}},
		{"Unicode list boundary", "docs/unicode-items.md", "- ADR-02 identity.\n\u00a0-\u2003Hosted signing remains unproven.\n", []string{`2: status restated ("remains unproven")` + link}},
		{"wrapped escape", "docs/wrapped.md", "It requires a recorded\nrun. <!-- status-ok: quoted policy -->\n", nil},
		{"bare escape", "docs/bare.md", "Hosted publication was exercised. <!-- status-ok: -->\n", []string{`1: status restated ("Hosted publication was exercised")` + link, "1: status-ok escape needs a reason: <!-- status-ok: why -->"}},
	}
	if err := os.Remove(filepath.Join(repo, "docs/moved-from.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "docs/link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "docs/swap.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside.txt", filepath.Join(repo, "docs/swap.md")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		write(t, repo, tc.path, tc.text)
	}
	head := commit(t, repo)
	_, findings, err := check(t.Context(), repo, base, head)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got, want []string
			for _, f := range findings {
				if strings.HasPrefix(f, tc.path+":") {
					got = append(got, f)
				}
			}
			for _, s := range tc.want {
				want = append(want, tc.path+":"+s)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("got %q want %q", got, want)
			}
		})
		total += len(tc.want)
	}
	if len(findings) != total {
		t.Fatalf("unexpected findings: %v", findings)
	}
	for _, tc := range []struct {
		base    string
		code    int
		failure bool
	}{{base, 1, false}, {head, 0, false}, {"no-such-revision", 1, true}} {
		for _, args := range [][]string{{"--repo", repo, tc.base, head}, {tc.base, "--repo", repo, head}, {tc.base, head, "--repo", repo}, {tc.base, head, "--repo=" + repo}} {
			code, err := run(t.Context(), args, io.Discard, io.Discard)
			if code != tc.code || (err != nil) != tc.failure {
				t.Fatalf("exit %d error %v", code, err)
			}
			if tc.failure && (!strings.Contains(err.Error(), "STATUS_BASE") || !strings.Contains(err.Error(), tc.base)) {
				t.Fatal(err)
			}
		}
	}
	t.Run("cancel during output", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var summary strings.Builder
		code, err := run(ctx, []string{base, head, "--repo", repo}, cancelWriter{cancel}, &summary)
		if code != 1 || !errors.Is(err, context.Canceled) || summary.Len() != 0 {
			t.Fatalf("exit %d error %v summary %q", code, err, summary.String())
		}
	})
	t.Run("textconv ignored", func(t *testing.T) {
		write(t, repo, ".git/info/attributes", "*.md diff=hide\n")
		fixtureGit(t, repo, "config", "diff.hide.textconv", "true")
		_, got, err := check(t.Context(), repo, base, head)
		if err != nil || !slices.Equal(got, findings) {
			t.Fatalf("textconv changed findings: %v %v", got, err)
		}
		write(t, repo, ".git/info/attributes", "")
		fixtureGit(t, repo, "config", "--unset", "diff.hide.textconv")
	})
	t.Run("working tree", func(t *testing.T) {
		write(t, repo, "docs/old.md", baseFiles["docs/old.md"]+"Signing is unproven.\n")
		write(t, repo, "docs/new.md", "Publication has not been recorded.\n")
		if err := os.Remove(filepath.Join(repo, "docs/guide.md")); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"docs/guide.md", "docs/untracked-link.md"} {
			if err := os.Symlink("../outside.txt", filepath.Join(repo, path)); err != nil {
				t.Fatal(err)
			}
		}
		write(t, repo, "docs/notes.md", "Intro.\n\nRelease requires a\nrecorded run.\n\nThe release is\n\nunrecorded.\n")
		_, got, err := check(t.Context(), repo, head, "")
		want := []string{`docs/new.md:1: status restated ("has not been recorded")` + link, `docs/notes.md:3: status restated ("requires a recorded run")` + link, `docs/old.md:2: status restated ("is unproven")` + link}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("got %q want %q error %v", got, want, err)
		}
	})
}

type cancelWriter struct{ cancel context.CancelFunc }

func (w cancelWriter) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }

func TestOwnerFileNamesAndCache(t *testing.T) {
	clearLocalGitEnv(t)
	repo := newRepo(t)
	write(t, repo, ledger+"adr-21.md ", "not an owner\n")
	head := commit(t, repo)
	for _, revision := range []string{"", head} {
		if _, err := ownerFiles(t.Context(), repo, revision); err == nil {
			t.Fatal("accepted a trailing-space owner name")
		}
	}
	resolver := ledgerResolver{repo: repo}
	if _, err := resolver.row(t.Context(), "docs/adr/0021-generation.md", ""); err == nil {
		t.Fatal("accepted a ledger without an owner")
	}
	write(t, repo, ledger+"adr-21.md", "owner\n")
	if got, err := resolver.row(t.Context(), "docs/adr/0021-generation.md", ""); err != nil || got != ledger+"adr-21.md" {
		t.Fatalf("first lookup %q: %v", got, err)
	}
	if err := os.Rename(filepath.Join(repo, ledger), filepath.Join(repo, "saved-ledger")); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.row(t.Context(), "docs/guide.md", "ADR-21"); err != nil || got != ledger+"adr-21.md" {
		t.Fatalf("cached lookup %q: %v", got, err)
	}
}

func TestArgumentErrors(t *testing.T) {
	for _, args := range [][]string{{"HEAD", "--repo"}, {"--unknown"}, {"--repo", "."}} {
		if code, err := run(t.Context(), args, io.Discard, io.Discard); code != 2 || err == nil {
			t.Fatalf("%v: %d %v", args, code, err)
		}
	}
}

func TestUnicodeWordBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text  string
		count int
	}{
		{"Signing remains unprovené.", 0},
		{"Signing éremains unproven.", 0},
		{"Signing remains unproven².", 0},
		{"Signing remains unproven_thing.", 0},
		{"Signing (remains unproven).", 1},
		{"Signing remains unproven. <!-- status-oké: not an escape -->", 1},
		{"Signing remains unproven. <!-- status-ok: dated log -->", 0},
		{"Signing remains unproven. <!-- status-ok-->", 2},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got := scan([]line{{number: 1, text: tc.text, added: true}})
			if len(got) != tc.count {
				t.Fatalf("findings %v, want %d", got, tc.count)
			}
		})
	}
}
func TestMissingLedger(t *testing.T) {
	clearLocalGitEnv(t)
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{{"absent", nil, "cannot list docs/adr/reconciliation/"}, {"empty", []string{}, "has no adr-NN.md owner files"}, {"topics", []string{"README.md", "adr-17-cli.md"}, "has no adr-NN.md owner files"}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if tc.files != nil {
				if err := os.MkdirAll(filepath.Join(repo, ledger), 0o700); err != nil {
					t.Fatal(err)
				}
				for _, name := range tc.files {
					write(t, repo, ledger+name, "# topic\n")
				}
			}
			_, err := ownerFiles(t.Context(), repo, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
			if tc.name == "absent" && !strings.Contains(err.Error(), "contains the per-section ledger") {
				t.Fatal("missing recovery hint", err)
			}
		})
	}
	repo := newRepo(t)
	write(t, repo, "a.txt", "base")
	commit(t, repo)
	if _, err := ownerFiles(t.Context(), repo, "HEAD"); err == nil || !strings.Contains(err.Error(), "predates the per-section ledger; merge main") {
		t.Fatal(err)
	}
}

func TestFixtureLeavesCallerIndexAlone(t *testing.T) {
	clearLocalGitEnv(t)
	outside := newRepo(t)
	write(t, outside, "sentinel", "keep\n")
	fixtureGit(t, outside, "add", "sentinel")
	index := filepath.Join(outside, ".git/index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_INDEX_FILE", index)
	repo := newRepo(t)
	write(t, repo, "docs/a.md", "Intro.\n")
	base := commit(t, repo)
	write(t, repo, "docs/a.md", "Signing is untested.\n")
	head := commit(t, repo)
	_, findings, err := check(t.Context(), repo, base, head)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings %v: %v", findings, err)
	}
	after, err := os.ReadFile(index)
	if err != nil || string(before) != string(after) {
		t.Fatal("fixture changed caller's index", err)
	}
}
