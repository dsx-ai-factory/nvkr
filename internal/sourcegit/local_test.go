// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func localFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_AUTHOR_NAME=NKD Fixture", "GIT_AUTHOR_EMAIL=nkd@example.test", "GIT_COMMITTER_NAME=NKD Fixture", "GIT_COMMITTER_EMAIL=nkd@example.test"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git failed: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func localFixtureRepository(t *testing.T) (string, string) {
	t.Helper()
	source := t.TempDir()
	localFixtureGit(t, source, "init", "-q")
	for name, body := range map[string]string{"input.txt": "pinned source\n", ".gitattributes": "input.txt filter=poison text\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	localFixtureGit(t, source, "add", ".")
	localFixtureGit(t, source, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	return source, localFixtureGit(t, source, "rev-parse", "HEAD")
}

func runLocalFixture(t *testing.T, environment []string, dir string, args ...string) []byte {
	t.Helper()
	cmd, err := LocalCommand(t.Context(), environment, dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := ownedcmd.Run(t.Context(), cmd); err != nil {
		t.Fatalf("isolated Git failed: %v: %s", err, output.Bytes())
	}
	return output.Bytes()
}

func TestLocalGitNeutralizesGlobalCallbacksAndConversions(t *testing.T) {
	source, commit := localFixtureRepository(t)
	for _, withHome := range []bool{false, true} {
		t.Run(map[bool]string{false: "no account", true: "developer account"}[withHome], func(t *testing.T) {
			home, checkout, hooks := t.TempDir(), t.TempDir(), t.TempDir()
			marker := filepath.Join(home, "executed")
			callback := filepath.Join(home, "callback")
			if err := os.WriteFile(callback, []byte("#!/bin/sh\nprintf executed > '"+marker+"'\ncat\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\nprintf hook > '"+marker+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			configuration := "[core]\n hooksPath = " + hooks + "\n fsmonitor = " + callback + "\n autocrlf = true\n eol = crlf\n[filter \"poison\"]\n smudge = " + callback + "\n clean = " + callback + "\n required = true\n"
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(configuration), 0o600); err != nil {
				t.Fatal(err)
			}
			environment := []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + source + ".insteadOf", "GIT_CONFIG_VALUE_0=https://example.test/nke.git"}
			if withHome {
				environment = append(environment, "HOME="+home)
			}
			runLocalFixture(t, environment, checkout, "init", "-q")
			runLocalFixture(t, environment, checkout, "fetch", "-q", "--depth=1", "https://example.test/nke.git", commit)
			runLocalFixture(t, environment, checkout, "checkout", "-q", "--detach", "FETCH_HEAD")
			runLocalFixture(t, environment, checkout, "status", "--porcelain=v1")
			body, err := os.ReadFile(filepath.Join(checkout, "input.txt"))
			if err != nil || string(body) != "pinned source\n" {
				t.Fatalf("checkout bytes changed: %q / %v", body, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("ambient Git callback executed: %v", err)
			}
			for _, operation := range []string{"init", "checkout", "status", "rev-parse"} {
				cmd, err := LocalCommand(t.Context(), environment, checkout, operation)
				if err != nil || localEnvironmentValue(cmd.Env, "HOME") != "" {
					t.Fatalf("non-fetch command inherited developer account: %v", err)
				}
			}
		})
	}
}

func TestLocalGitRejectsRepositoryCallbacks(t *testing.T) {
	for _, key := range []string{"filter.poison.smudge", "include.path", "url.https://foreign.test.insteadOf", "core.alternateRefsCommand", "remote.origin.promisor", "extensions.worktreeConfig"} {
		t.Run(key, func(t *testing.T) {
			source, _ := localFixtureRepository(t)
			localFixtureGit(t, source, "config", key, "true")
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, source, "status", "--porcelain=v1")
			if cmd != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("repository callback configuration admitted: %v", err)
			}
		})
	}
}

func TestLocalGitPreservesDeveloperHelperOnlyForFetch(t *testing.T) {
	source, commit := localFixtureRepository(t)
	root, home, checkout := t.TempDir(), t.TempDir(), t.TempDir()
	bare := filepath.Join(root, "source.git")
	localFixtureGit(t, root, "clone", "--bare", "-q", source, bare)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Dir: root,
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "fixture" || password != "synthetic-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	helper := `!f() { if [ "$1" = get ]; then printf '%s\n' username=fixture password=synthetic-password; fi; }; f`
	localFixtureGit(t, home, "config", "--file", filepath.Join(home, ".gitconfig"), "credential."+server.URL+".helper", helper)
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "UNRELATED_SECRET=not-admitted"}
	runLocalFixture(t, environment, checkout, "init", "-q")
	runLocalFixture(t, environment, checkout, "fetch", "-q", "--depth=1", server.URL+"/source.git", commit)
	runLocalFixture(t, environment, checkout, "checkout", "-q", "--detach", "FETCH_HEAD")
	if head := strings.TrimSpace(string(runLocalFixture(t, environment, checkout, "rev-parse", "HEAD"))); head != commit {
		t.Fatal("authenticated fetch did not return selected commit")
	}
	for _, operation := range []string{"fetch", "status"} {
		cmd, err := LocalCommand(t.Context(), environment, checkout, operation)
		if err != nil {
			t.Fatal(err)
		}
		if localEnvironmentValue(cmd.Env, "UNRELATED_SECRET") != "" {
			t.Fatal("ambient secret inherited by Git")
		}
		if operation == "status" && (localEnvironmentValue(cmd.Env, "HOME") != "" || localEnvironmentValue(cmd.Env, "GIT_CONFIG_COUNT") != "1") {
			t.Fatal("credential configuration escaped fetch-only scope")
		}
	}
}

func TestLocalGitAdmissionContextIsCoded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		ctx  context.Context
		code e.Code
	}{{nil, e.CodeUsage}, {ctx, e.CodeTimeout}} {
		cmd, err := LocalCommand(test.ctx, nil, "", "status")
		if cmd != nil || errorCode(err) != test.code {
			t.Fatalf("invalid context admitted: %v", err)
		}
	}
}

func TestLocalGitRejectsUnselectedProcessConfiguration(t *testing.T) {
	for _, fields := range [][]string{
		{"GIT_CONFIG_COUNT=not-a-count"},
		{"GIT_CONFIG_COUNT=17"},
		{"GIT_CONFIG_COUNT=-1"},
		{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=!unexpected"},
		{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url.relative.insteadOf", "GIT_CONFIG_VALUE_0=https://example.test/nke"},
		{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url./tmp/repository.insteadOf"},
	} {
		cmd, err := LocalCommand(t.Context(), append([]string{"PATH=" + os.Getenv("PATH")}, fields...), "", "fetch")
		if cmd != nil || errorCode(err) != e.CodeEngineCapability {
			t.Fatalf("unselected process configuration admitted: %v", err)
		}
	}
}

func TestLocalGitPreservesImplicitCredentialBoolean(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[credential]\nuseHttpPath\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, "", "fetch")
	if err != nil {
		t.Fatal(err)
	}
	if localEnvironmentValue(cmd.Env, "GIT_CONFIG_KEY_1") != "credential.usehttppath" || localEnvironmentValue(cmd.Env, "GIT_CONFIG_VALUE_1") != "true" {
		t.Fatal("Git's implicit credential boolean changed during projection")
	}
}
