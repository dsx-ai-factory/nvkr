// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

// controllerCatalogFixture keeps the authored producers, patches and chart pins,
// but rederives their pin set from a local Git tree of checked-in deploy metadata.
// The authored source pins are asserted before replacement. This compiles the
// catalog contract without private GitHub credentials; it does not exercise the
// upstream source tree or claim that the resulting Plan can build its producers.
func controllerCatalogFixture(t *testing.T, directory string) (*catalog.Catalog, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(directory)); err != nil {
		t.Fatal(err)
	}
	lock, err := catalog.LoadLockContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// The release is whatever the authored lock records, so a reviewed catalog
	// maintenance bump does not break this fixture. The chart-owning nvke source
	// and its chart source are one maintained release cohort and must agree; the
	// independent nvke-cli role only has to be a complete commit.
	sourceCommit := lock.Pins["nvke"].Commit
	for _, name := range []string{"nvke", "nvke-cli", "nvke-helm"} {
		if !fullCommit.MatchString(lock.Pins[name].Commit) {
			t.Fatalf("authored %s source=%q is not a full commit", name, lock.Pins[name].Commit)
		}
	}
	if lock.Pins["nvke-helm"].Commit != sourceCommit {
		t.Fatalf("authored nvke-helm source=%s, want the nvke source %s", lock.Pins["nvke-helm"].Commit, sourceCommit)
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate controller pin-set fixture")
	}
	repo := t.TempDir()
	if err := os.CopyFS(repo, os.DirFS(filepath.Join(filepath.Dir(filename), "../catalog/testdata/nvke-pin-set"))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_AUTHOR_DATE", "1733939955 +0000")
	t.Setenv("GIT_COMMITTER_DATE", "1733939955 +0000")
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		cmd.WaitDelay = time.Second
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	run("init", "--template=", "-q")
	run("config", "user.name", "Controller Catalog Test")
	run("config", "user.email", "controller-catalog@example.invalid")
	run("add", ".")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "controller deploy metadata fixture")
	commit, tree := run("rev-parse", "HEAD"), run("rev-parse", "HEAD^{tree}")
	for _, name := range []string{"nvke", "nvke-cli", "nvke-helm"} {
		pin := lock.Pins[name]
		pin.Commit, pin.TreeHash, pin.SourceDateEpoch = commit, tree, 1733939955
		lock.Pins[name] = pin
	}
	for name, pin := range lock.Artifacts {
		if pin.DerivedFrom != "nvke@"+sourceCommit {
			t.Fatalf("authored chart %s has unexpected pin-set owner %q", name, pin.DerivedFrom)
		}
		pin.DerivedFrom = "nvke@" + commit
		lock.Artifacts[name] = pin
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/dsx-ai-factory/nvke")
	if err := catalog.WriteLockContext(t.Context(), dir, lock); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	return cat, commit
}

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
