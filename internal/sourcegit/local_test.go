// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestLocalGitRejectsHiddenSubmoduleDriftWithoutNestedCallbacks(t *testing.T) {
	source, _ := localFixtureRepository(t)
	nested := filepath.Join(source, "synthetic-private-submodule")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	localFixtureGit(t, nested, "init", "-q")
	input := filepath.Join(nested, "input.txt")
	if err := os.WriteFile(input, []byte("pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	localFixtureGit(t, nested, "add", "input.txt")
	localFixtureGit(t, nested, "-c", "commit.gpgsign=false", "commit", "-qm", "nested fixture")
	commit := localFixtureGit(t, nested, "rev-parse", "HEAD")
	// A gitlink need not have a .gitmodules descriptor. Git owns its index
	// framing; the admission must reject this form too.
	localFixtureGit(t, source, "update-index", "--add", "--cacheinfo", "160000,"+commit+",synthetic-private-submodule")
	localFixtureGit(t, source, "-c", "commit.gpgsign=false", "commit", "-qm", "gitlink fixture")
	if err := os.WriteFile(input, []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := localFixtureGit(t, source, "-c", "diff.ignoreSubmodules=all", "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("fixture did not reproduce hidden drift: %q", got)
	}
	if got := localFixtureGit(t, source, "-c", "diff.ignoreSubmodules=none", "status", "--porcelain"); got == "" {
		t.Fatal("fixture has no actual submodule drift")
	}
	marker := filepath.Join(t.TempDir(), "executed")
	if err := os.WriteFile(filepath.Join(nested, ".gitattributes"), []byte("input.txt filter=poison\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	localFixtureGit(t, nested, "config", "filter.poison.clean", "printf executed > '"+marker+"'; cat")
	localFixtureGit(t, nested, "add", "input.txt")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("nested fixture callback did not execute without isolation: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	for _, operation := range [][]string{{"status", "--porcelain"}, {"rev-parse", "HEAD"}, {"checkout", "--detach", "HEAD"}} {
		cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, source, operation...)
		if cmd != nil || errorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "gitlinks are unsupported") {
			t.Fatalf("hidden nested source admitted for %s: %v", operation[0], err)
		}
		if strings.Contains(err.Error(), source) || strings.Contains(err.Error(), "synthetic-private-submodule") {
			t.Fatal("submodule refusal exposed private source identity")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("nested Git callback executed during admission: %v", err)
	}
}

func TestLocalGitRejectsSubmoduleDescriptors(t *testing.T) {
	for _, kind := range []string{"untracked", "tracked", "indexed but deleted", "ignored", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			source, _ := localFixtureRepository(t)
			descriptor := filepath.Join(source, ".gitmodules")
			if kind == "symlink" {
				if err := os.Symlink("synthetic-private-missing-target", descriptor); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(descriptor, []byte("[submodule \"synthetic-private-subsection\"]\n url = https://synthetic-private-url.test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "tracked", "indexed but deleted":
				localFixtureGit(t, source, "add", ".gitmodules")
				if kind == "indexed but deleted" {
					if err := os.Remove(descriptor); err != nil {
						t.Fatal(err)
					}
				}
			case "ignored":
				if err := os.WriteFile(filepath.Join(source, ".gitignore"), []byte(".gitmodules\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, source, "status", "--porcelain")
			if cmd != nil || errorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), ".gitmodules is unsupported") {
				t.Fatalf("submodule descriptor admitted: %v", err)
			}
			for _, secret := range []string{source, "synthetic-private-subsection", "synthetic-private-url.test", "synthetic-private-missing-target"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("descriptor refusal exposed private identity")
				}
			}
		})
	}
}

func TestLocalGitIndexModesHaveASeparateBound(t *testing.T) {
	source, _ := localFixtureRepository(t)
	blob := localFixtureGit(t, source, "rev-parse", "HEAD:input.txt")
	var entries strings.Builder
	for index := 0; index < 10_000; index++ {
		entries.WriteString("100644 " + blob + "\tfixture-" + strconv.Itoa(index) + "\n")
	}
	cmd := exec.CommandContext(t.Context(), "git", "-C", source, "update-index", "--index-info")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	cmd.Stdin = strings.NewReader(entries.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("large index fixture failed: %v: %s", err, output)
	}
	// 10,002 modes exceed the configuration query's 64 KiB limit, but not
	// the explicitly separate index query bound. No worktree files are read.
	runLocalFixture(t, []string{"PATH=" + os.Getenv("PATH")}, source, "rev-parse", "HEAD")
}

func TestLocalGitIndexQueryFailsClosed(t *testing.T) {
	source, _ := localFixtureRepository(t)
	path := os.Getenv("PATH")
	baseline, err := Baseline(path)
	if err != nil {
		t.Fatal(err)
	}
	git, err := executable(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		args  []string
		limit int
	}{
		{"output bound", []string{"ls-files", "--format=%(objectmode)"}, 1},
		// Git config's documented no-match exit 1 is tolerated only by the
		// config adapter, never by the mandatory index-admission query.
		{"empty nonzero exit", []string{"config", "--get", "nkd.synthetic-private-absent"}, 64 << 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"-C", source}, test.args...)
			data, err := localGitQuery(t.Context(), git, baseline, args, "index modes", test.limit, false)
			if data != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("failed index query admitted: %v", err)
			}
			if strings.Contains(err.Error(), source) || strings.Contains(err.Error(), "synthetic-private-absent") {
				t.Fatal("failed query exposed private source identity")
			}
		})
	}
}

func TestLocalGitRefusalNamesOnlyStaticConfigurationFamilies(t *testing.T) {
	for _, test := range []struct {
		name, family string
		keys         []string
	}{
		{"callbacks", "core.*", []string{"core.sshCommand"}},
		{"delegation", "extensions.*", []string{"extensions.worktreeConfig"}},
		{"filters", "filter.*", []string{"filter.synthetic-private-subsection.clean"}},
		{"hooks", "hook.*", []string{"hook.synthetic-private-subsection.command"}},
		{"HTTP", "http.*", []string{"http.https://synthetic-private-url.test.proxy"}},
		{"includes", "include.*", []string{"include.path"}},
		{"conditional includes", "includeif.*", []string{"includeIf.gitdir:/synthetic-private-path.path"}},
		{"object sources", "remote.*", []string{"remote.synthetic-private-subsection.promisor"}},
		{"URL rewrites", "url.*", []string{"url.https://synthetic-private-url.test.insteadOf"}},
		{"sorted distinct families", "filter.*, http.*, url.*", []string{"url.https://synthetic-private-url.test.insteadOf", "filter.synthetic-private-subsection.clean", "http.https://synthetic-private-url.test.proxy", "filter.second-private-subsection.smudge"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, _ := localFixtureRepository(t)
			for _, key := range test.keys {
				value := "synthetic-private-value"
				if key == "extensions.worktreeConfig" {
					value = "true" // Git validates this boolean before its config query.
				}
				localFixtureGit(t, source, "config", key, value)
			}
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, source, "status", "--porcelain=v1")
			if cmd != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("unsupported configuration admitted: %v", err)
			}
			want := "selected source repository contains unsupported Git configuration families: " + test.family
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal omits static families: %v", err)
			}
			for _, private := range append([]string{source, "synthetic-private-subsection", "second-private-subsection", "synthetic-private-url.test", "/synthetic-private-path", "synthetic-private-value"}, test.keys...) {
				if strings.Contains(err.Error(), private) {
					t.Fatal("refusal exposed private configuration or source identity")
				}
			}
		})
	}
}

func TestLocalRedirectRefusalNamesIndexWithoutPrivateConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, key, value, want string
	}{
		{"unselected variable", "credential.synthetic-private-subsection.helper", "synthetic-private-value", "GIT_CONFIG_KEY_1 must select an explicit absolute local repository redirect"},
		{"private URI", "url.file:///synthetic-private-path/.insteadOf", "https://synthetic-private-url.test/source", "GIT_CONFIG_KEY_1 must use a canonical absolute local repository path"},
		{"noncanonical path", "url./synthetic-private-path/../source.insteadOf", "synthetic-private-value", "GIT_CONFIG_KEY_1 must use a canonical absolute local repository path"},
		{"missing value", "url./synthetic-private-path.insteadOf", "", "GIT_CONFIG_VALUE_1 must provide a nonempty repository redirect source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=url./absolute/source/.insteadOf", "GIT_CONFIG_VALUE_0=https://example.test/source",
				"GIT_CONFIG_KEY_1=" + test.key, "GIT_CONFIG_VALUE_1=" + test.value}
			got, err := LocalRedirectEnvironment(environment)
			if got != nil || errorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("refusal omits failed selector index: %v", err)
			}
			for _, private := range []string{test.key, "synthetic-private-subsection", "synthetic-private-path", "synthetic-private-url.test", "synthetic-private-value"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("redirect refusal exposed private configuration")
				}
			}
		})
	}
}

func TestLocalGitValuelessCredentialDiagnosticIsPrivate(t *testing.T) {
	for _, test := range []struct {
		name, section, variable, want string
	}{
		{"helper", "credential", "helper", "credential.helper requires an explicit value"},
		{"scoped helper", `credential "https://synthetic-private-url.test/source"`, "helper", "credential.helper requires an explicit value"},
		{"username", `credential "https://synthetic-private-url.test/source"`, "username", "credential.username requires an explicit value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("["+test.section+"]\n"+test.variable+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, "", "fetch")
			if cmd != nil || errorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("valueless credential refusal omits its static family: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic-private-url.test") || strings.Contains(err.Error(), home) {
				t.Fatal("credential refusal exposed account or repository identity")
			}
		})
	}
}

func TestLocalGitPreservesExplicitEmptyHelperReset(t *testing.T) {
	for _, section := range []string{"credential", `credential "https://synthetic-private-url.test/source"`} {
		t.Run(section, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("["+section+"]\nhelper =\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, "", "fetch")
			if err != nil {
				t.Fatal(err)
			}
			if localEnvironmentValue(cmd.Env, "GIT_CONFIG_COUNT") != "2" || !strings.HasSuffix(localEnvironmentValue(cmd.Env, "GIT_CONFIG_KEY_1"), ".helper") || localEnvironmentValue(cmd.Env, "GIT_CONFIG_VALUE_1") != "" {
				t.Fatal("explicit empty helper reset changed during projection")
			}
		})
	}
}

func TestLocalGitRenumbersTwoRedirectsAfterDeveloperCredential(t *testing.T) {
	home := t.TempDir()
	localFixtureGit(t, home, "config", "--file", filepath.Join(home, ".gitconfig"), "credential.helper", "synthetic-helper")
	first, firstCommit := localFixtureRepository(t)
	second, secondCommit := localFixtureRepository(t)
	keys := []string{"url." + first + ".insteadOf", "url." + second + ".insteadOf"}
	values := []string{"https://example.test/first", "https://example.test/second"}
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=" + keys[0], "GIT_CONFIG_VALUE_0=" + values[0],
		"GIT_CONFIG_KEY_1=" + keys[1], "GIT_CONFIG_VALUE_1=" + values[1]}
	for index, commit := range []string{firstCommit, secondCommit} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			checkout := t.TempDir()
			runLocalFixture(t, environment, checkout, "init", "-q")
			cmd, err := LocalCommand(t.Context(), environment, checkout, "fetch", "-q", "--depth=1", values[index], commit)
			if err != nil {
				t.Fatal(err)
			}
			if localEnvironmentValue(cmd.Env, "GIT_CONFIG_COUNT") != "4" ||
				localEnvironmentValue(cmd.Env, "GIT_CONFIG_KEY_1") != "credential.helper" ||
				localEnvironmentValue(cmd.Env, "GIT_CONFIG_VALUE_1") != "synthetic-helper" {
				t.Fatal("credential projection displaced redirects or helper reset")
			}
			for slot := range keys {
				suffix := strconv.Itoa(slot + 2)
				if localEnvironmentValue(cmd.Env, "GIT_CONFIG_KEY_"+suffix) != keys[slot] || localEnvironmentValue(cmd.Env, "GIT_CONFIG_VALUE_"+suffix) != values[slot] {
					t.Fatal("exact redirect changed during credential renumbering")
				}
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := ownedcmd.Run(t.Context(), cmd); err != nil {
				t.Fatalf("renumbered fetch failed: %v: %s", err, &output)
			}
			runLocalFixture(t, environment, checkout, "checkout", "-q", "--detach", "FETCH_HEAD")
			if head := strings.TrimSpace(string(runLocalFixture(t, environment, checkout, "rev-parse", "HEAD"))); head != commit {
				t.Fatal("renumbered fetch returned a different source")
			}
		})
	}
}

func TestLocalGitConfigurationQueryFailuresAreClosedAndPrivate(t *testing.T) {
	for _, test := range []struct{ name, config string }{
		{"overflow", "[credential]\nhelper = " + strings.Repeat("synthetic-private-value", 4096) + "\n"},
		{"invalid configuration", "[credential\nsynthetic-private-value\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd, err := LocalCommand(t.Context(), []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, "", "fetch")
			if cmd != nil || errorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "configuration query failed") {
				t.Fatalf("failed configuration query produced an executable fetch: %v", err)
			}
			if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), "synthetic-private-value") {
				t.Fatal("configuration query refusal exposed private details")
			}
		})
	}
}

func TestLocalConfigurationDiagnosticDoesNotEchoUnexpectedRecords(t *testing.T) {
	for _, data := range []string{"", "synthetic-private-value\x00", "unknown.synthetic-private-value\x00", "unknown.https://synthetic-private-url.test\n/synthetic-private-path\x00"} {
		if got := localConfigurationFamilies([]byte(data)); got != "unrecognized" {
			t.Fatal("unexpected diagnostic record escaped its static label")
		}
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "fixture" || password != "synthetic-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	certificate := filepath.Join(home, "fixture-ca.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := `!f() { if [ "$1" = get ]; then printf '%s\n' username=fixture password=synthetic-password; fi; }; f`
	localFixtureGit(t, home, "config", "--file", filepath.Join(home, ".gitconfig"), "credential."+server.URL+".helper", helper)
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_SSL_CAINFO=" + certificate, "UNRELATED_SECRET=not-admitted"}
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

func TestLocalGitNetworkOperationsKeepClosedTransport(t *testing.T) {
	home := t.TempDir()
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	for _, operation := range []string{"fetch", "clone", "ls-remote", "status"} {
		t.Run(operation, func(t *testing.T) {
			cmd, err := LocalCommand(t.Context(), environment, "", operation)
			if err != nil {
				t.Fatal(err)
			}
			if localEnvironmentValue(cmd.Env, "GIT_ALLOW_PROTOCOL") != "https:ssh:file" {
				t.Fatal("cleartext or external transports admitted")
			}
			arguments := strings.Join(cmd.Args, "\n")
			if (strings.Contains(arguments, "--upload-pack=git-upload-pack")) != (operation != "status") ||
				strings.Contains(arguments, "fetch.fsckObjects=true") != (operation == "fetch" || operation == "clone") ||
				strings.Contains(arguments, "transfer.fsckObjects=true") != (operation == "clone") ||
				strings.Contains(arguments, "--no-recurse-submodules") != (operation == "fetch" || operation == "clone") {
				t.Fatal("network operation lost its fixed transport/object policy")
			}
			if operation != "fetch" && (localEnvironmentValue(cmd.Env, "HOME") != "" || localEnvironmentValue(cmd.Env, "GIT_CONFIG_COUNT") != "1") {
				t.Fatal("network hardening expanded credential projection beyond fetch")
			}
		})
	}
	source, commit := localFixtureRepository(t)
	checkout := filepath.Join(t.TempDir(), "installer")
	runLocalFixture(t, environment, "", "clone", "--no-hardlinks", "--no-checkout", source, checkout)
	runLocalFixture(t, environment, checkout, "checkout", "--detach", commit)
	if head := strings.TrimSpace(string(runLocalFixture(t, environment, checkout, "rev-parse", "HEAD"))); head != commit {
		t.Fatal("hardened installer clone changed selected bytes")
	}
	if refs := string(runLocalFixture(t, environment, "", "ls-remote", source, "HEAD")); !strings.HasPrefix(refs, commit+"\t") {
		t.Fatal("hardened ref discovery changed selected identity")
	}
}

func TestLocalGitRejectsCleartextBeforeNetworkAcquisition(t *testing.T) {
	for _, protocol := range []string{"http", "git"} {
		t.Run(protocol, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd, err := LocalCommand(ctx, []string{"PATH=" + os.Getenv("PATH")}, "", "ls-remote", protocol+"://127.0.0.1:9/fixture.git")
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			err = ownedcmd.Run(ctx, cmd)
			if err == nil || ctx.Err() != nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 128 || !strings.Contains(output.String(), "transport '"+protocol+"' not allowed") {
				t.Fatal("Git did not refuse the selected cleartext transport before connecting", err)
			}
		})
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

func TestLocalGitRejectsGlobalOptionsBeforeWork(t *testing.T) {
	for _, args := range [][]string{
		nil, {""}, {"-C", "/unselected", "status"}, {"-C/unselected", "status"},
		{"-c", "color.ui=false", "-C", "/unselected", "status"},
		{"--git-dir=/unselected/.git", "status"}, {"--work-tree=/unselected", "status"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			// Invalid PATH and a missing directory must not be inspected first.
			cmd, err := LocalCommand(t.Context(), nil, "/missing-selected-source", args...)
			if cmd != nil || errorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "without caller-supplied global options") {
				t.Fatalf("global option reached another admission boundary: %v", err)
			}
			if strings.Contains(err.Error(), "unselected") {
				t.Fatal("refusal exposed caller-supplied repository selector")
			}
		})
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
