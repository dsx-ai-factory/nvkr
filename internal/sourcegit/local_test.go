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
	for _, key := range []string{"filter.poison.smudge", "include.path", "url.https://foreign.test.insteadOf", "core.alternateRefsCommand", "remote.origin.promisor", "extensions.worktreeConfig",
		"core.sshCommand", "core.gitProxy", "core.askPass", "core.worktree", "http.proxy", "http.sslVerify"} {
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

func TestLocalRedirectEnvironmentAdmitsMaintainedPathBases(t *testing.T) {
	for _, test := range []struct {
		name, key string
		valid     bool
	}{
		{"canonical", "url./absolute/source.insteadOf", true},
		{"workflow terminal separator", "url./absolute/source/.insteadOf", true},
		{"Git variable case", "url./absolute/source/.insteadof", true},
		{"embedded dot", "url./absolute/./source/.insteadOf", false},
		{"embedded parent", "url./absolute/other/../source/.insteadOf", false},
		{"repeated separator", "url./absolute/source//.insteadOf", false},
		{"relative", "url.relative/source/.insteadOf", false},
		{"unselected URI", "url.file:///absolute/source/.insteadOf", false},
		{"unselected config", "safe.directory", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=" + test.key, "GIT_CONFIG_VALUE_0=https://example.test/source"}
			got, err := LocalRedirectEnvironment(environment)
			if test.valid {
				if err != nil || strings.Join(got, "\n") != strings.Join(environment, "\n") {
					t.Fatalf("maintained exact rewrite was not preserved: %v %q", err, got)
				}
			} else if got != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("unselected rewrite admitted: %v %q", err, got)
			}
		})
	}
}

func TestLocalGitTrailingSeparatorRewriteUsesExactRepository(t *testing.T) {
	source, commit := localFixtureRepository(t)
	checkout := t.TempDir()
	environment := []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + source + "/.insteadOf", "GIT_CONFIG_VALUE_0=https://example.test/source"}
	runLocalFixture(t, environment, checkout, "init", "-q")
	runLocalFixture(t, environment, checkout, "fetch", "-q", "--depth=1", "https://example.test/source", commit)
	runLocalFixture(t, environment, checkout, "checkout", "-q", "--detach", "FETCH_HEAD")
	if head := strings.TrimSpace(string(runLocalFixture(t, environment, checkout, "rev-parse", "HEAD"))); head != commit {
		t.Fatal("workflow rewrite did not fetch the selected repository commit")
	}
}

func TestLocalGitExplicitChildDoesNotDiscoverAncestorRepository(t *testing.T) {
	for _, callback := range []bool{false, true} {
		t.Run(map[bool]string{false: "harmless ancestor", true: "callback ancestor"}[callback], func(t *testing.T) {
			source, _ := localFixtureRepository(t)
			marker := filepath.Join(t.TempDir(), "callback-ran")
			if callback {
				localFixtureGit(t, source, "config", "filter.poison.clean", "touch '"+marker+"'; cat")
				if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("edited source\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				// Verify the fixture is capable of executing the forbidden callback
				// before testing that the isolated child command cannot reach it.
				localFixtureGit(t, source, "status", "--porcelain=v1")
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("ancestor callback fixture was not exercised: %v", err)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			}
			child := filepath.Join(source, "child")
			if err := os.Mkdir(child, 0o700); err != nil {
				t.Fatal(err)
			}
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, child, "status", "--porcelain=v1")
			if err != nil {
				t.Fatal(err)
			}
			// Explicit child paths are intentionally not treated as the enclosing
			// source repository, even when that enclosing repository is harmless.
			if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "not a git repository") {
				t.Fatalf("explicit child discovered ancestor: %v %q", err, output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("ancestor filter executed: %v", err)
			}
			// A fresh source may still initialize its own repository at this exact
			// path. The ceiling excludes only discovery of the enclosing checkout.
			runLocalFixture(t, []string{"PATH=" + os.Getenv("PATH")}, child, "init", "-q")
			runLocalFixture(t, []string{"PATH=" + os.Getenv("PATH")}, child, "status", "--porcelain=v1")
		})
	}
}

func TestLocalGitRequiresAbsoluteExplicitDirectory(t *testing.T) {
	for _, dir := range []string{".", "relative/source", "../source"} {
		t.Run(dir, func(t *testing.T) {
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, dir, "status")
			if cmd != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("relative repository admitted: %v", err)
			}
		})
	}
}

func TestLocalGitCredentialSupportRemainsExplicitAndFetchOnly(t *testing.T) {
	home := t.TempDir()
	for key, value := range map[string]string{"credential.helper": "synthetic-helper", "credential.username": "synthetic-user", "credential.useHttpPath": "true", "credential.credentialStore": "unselected-store", "credential.namespace": "unselected-namespace"} {
		localFixtureGit(t, home, "config", "--file", filepath.Join(home, ".gitconfig"), key, value)
	}
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home,
		"GH_TOKEN=unselected-token", "GH_CONFIG_DIR=unselected-location", "GCM_CREDENTIAL_STORE=unselected-store", "DBUS_SESSION_BUS_ADDRESS=unselected-session"}
	for _, operation := range []string{"fetch", "status"} {
		t.Run(operation, func(t *testing.T) {
			cmd, err := LocalCommand(t.Context(), environment, "", operation)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"GH_TOKEN", "GH_CONFIG_DIR", "GCM_CREDENTIAL_STORE", "DBUS_SESSION_BUS_ADDRESS"} {
				if localEnvironmentValue(cmd.Env, key) != "" {
					t.Fatalf("%s inherited unselected %s", operation, key)
				}
			}
			joined := strings.Join(cmd.Env, "\n")
			if strings.Contains(joined, "unselected-store") || strings.Contains(joined, "unselected-namespace") {
				t.Fatal("unselected credential configuration was forwarded")
			}
			for _, key := range []string{"credential.helper", "credential.username", "credential.usehttppath"} {
				present := strings.Contains(joined, "="+key+"\n")
				if key == "credential.helper" && operation == "status" {
					continue // The public baseline resets helpers to empty.
				}
				if present != (operation == "fetch") {
					t.Fatalf("%s projected %s incorrectly", operation, key)
				}
			}
		})
	}
}

func TestLocalGitConditionalCredentialsUseSelectedSourceDirectory(t *testing.T) {
	source, _ := localFixtureRepository(t)
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := localFixtureRepository(t)
	home := t.TempDir()
	credentials := filepath.Join(home, "selected-credentials")
	localFixtureGit(t, home, "config", "--file", credentials, "credential.helper", "selected-synthetic-helper")
	localFixtureGit(t, home, "config", "--file", filepath.Join(home, ".gitconfig"), "includeIf.gitdir:"+filepath.Join(source, ".git")+".path", credentials)
	for _, test := range []struct {
		name, directory, operation string
		selected                   bool
	}{
		{"selected fetch", source, "fetch", true},
		{"other fetch", other, "fetch", false},
		{"selected status", source, "status", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, test.directory, test.operation)
			if err != nil {
				t.Fatal(err)
			}
			got := localEnvironmentValue(cmd.Env, "GIT_CONFIG_VALUE_1") == "selected-synthetic-helper"
			if got != test.selected {
				t.Fatalf("conditional credential selection=%t want=%t", got, test.selected)
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

func TestLocalGitTransportEnvironmentIsFetchOnly(t *testing.T) {
	transport := []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
		"NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy", "GIT_SSL_CAINFO", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	environment := []string{"PATH=" + os.Getenv("PATH"), "GIT_SSH_COMMAND=unselected-callback", "UNRELATED_SECRET=not-admitted"}
	for _, key := range transport {
		environment = append(environment, key+"=synthetic-transport-setting")
	}
	for _, operation := range []string{"init", "fetch", "checkout", "status"} {
		t.Run(operation, func(t *testing.T) {
			cmd, err := LocalCommand(t.Context(), environment, "", operation)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range transport {
				want := ""
				if operation == "fetch" {
					want = "synthetic-transport-setting"
				}
				if got := localEnvironmentValue(cmd.Env, key); got != want {
					t.Fatalf("%s inherited %s=%q, want %q", operation, key, got, want)
				}
			}
			for _, key := range []string{"GIT_SSH_COMMAND", "UNRELATED_SECRET"} {
				if got := localEnvironmentValue(cmd.Env, key); got != "" {
					t.Fatalf("%s inherited unselected %s", operation, key)
				}
			}
		})
	}
}
