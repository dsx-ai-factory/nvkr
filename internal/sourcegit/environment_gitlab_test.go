// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// gitLabProfile assembles the exact closed five-entry GitLab job-token profile
// with synthetic values only.
func gitLabProfile(path string) []string {
	return []string{
		"PATH=" + path, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=5", "GIT_CONFIG_KEY_0=url.https://mirror.test/source.insteadOf", "GIT_CONFIG_VALUE_0=https://example.test/source",
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=", "GIT_CONFIG_KEY_2=credential.https://mirror.test/source.helper",
		"GIT_CONFIG_VALUE_2=" + CredentialHelperGitLab, "GIT_CONFIG_KEY_3=credential.useHttpPath", "GIT_CONFIG_VALUE_3=true",
		"GIT_CONFIG_KEY_4=http.followRedirects", "GIT_CONFIG_VALUE_4=false", Token + "=synthetic-token",
	}
}

// gitHubProfile assembles the existing exact four-item GitHub credential
// configuration with synthetic values only.
func gitHubProfile(path string) []string {
	return []string{
		"PATH=" + path, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=4", "GIT_CONFIG_KEY_0=url.https://mirror.test/source.insteadOf", "GIT_CONFIG_VALUE_0=https://example.test/source",
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=", "GIT_CONFIG_KEY_2=credential.https://mirror.test/source.helper",
		"GIT_CONFIG_VALUE_2=" + CredentialHelper, "GIT_CONFIG_KEY_3=credential.useHttpPath", "GIT_CONFIG_VALUE_3=true", Token + "=synthetic-token",
	}
}

// sorted copies expected input and sorts it; Admit returns its own sorted result.
func sorted(environment []string) []string {
	expected := slices.Clone(environment)
	slices.Sort(expected)
	return expected
}

func TestGitLabJobTokenAdmitsExactFiveEntryProfile(t *testing.T) {
	path := testPath(t)
	environment := gitLabProfile(path)
	executable, admitted, err := Admit("https://example.test/source", environment)
	if err != nil {
		t.Fatalf("exact GitLab job-token profile not admitted: %v", err)
	}
	if executable != filepath.Join(path, "git") {
		t.Fatalf("selected Git executable changed: %q", executable)
	}
	if !slices.Equal(admitted, sorted(environment)) {
		t.Fatalf("admitted environment is not the exact sorted five-entry GitLab profile: %v", admitted)
	}
}

func TestGitLabJobTokenLeavesBaselineAndGitHubBehaviorUnchanged(t *testing.T) {
	path := testPath(t)
	baseline, err := Baseline(path)
	if err != nil {
		t.Fatal(err)
	}
	expectedBaseline := sorted(baseline)
	executable, admitted, err := Admit("https://example.test/source", slices.Clone(baseline))
	if err != nil || executable != filepath.Join(path, "git") || !slices.Equal(admitted, expectedBaseline) {
		t.Fatalf("public baseline admission changed: %s / %v / %v", executable, admitted, err)
	}

	github := gitHubProfile(path)
	expectedGitHub := sorted(github)
	executable, admitted, err = Admit("https://example.test/source", github)
	if err != nil || executable != filepath.Join(path, "git") || !slices.Equal(admitted, expectedGitHub) {
		t.Fatalf("GitHub four-item profile admission changed: %s / %v / %v", executable, admitted, err)
	}

	for _, test := range []struct {
		name   string
		change func([]string) []string
	}{
		{"GitHub profile with GitLab redirect entry", func(env []string) []string {
			return append(env, "GIT_CONFIG_KEY_4=http.followRedirects", "GIT_CONFIG_VALUE_4=false")
		}},
		{"GitHub helper under the GitLab count", func(env []string) []string {
			return replace(gitLabProfile(path), "GIT_CONFIG_VALUE_2", CredentialHelper)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable, admitted, err := Admit("https://example.test/source", test.change(slices.Clone(github)))
			if executable != "" || admitted != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("GitLab-only configuration entered an old profile: %s / %v / %v", executable, admitted, err)
			}
		})
	}
}

func TestGitLabJobTokenRefusesNonFiveEntryProfiles(t *testing.T) {
	path := testPath(t)
	for _, test := range []struct {
		name, count, helper string
	}{
		{"GitLab helper under count four", "4", CredentialHelperGitLab},
		{"GitHub helper under count five", "5", CredentialHelper},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := replace(gitHubProfile(path), "GIT_CONFIG_COUNT", test.count)
			profile = replace(profile, "GIT_CONFIG_VALUE_2", test.helper)
			executable, admitted, err := Admit("https://example.test/source", profile)
			if executable != "" || admitted != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("non-five-entry GitLab configuration admitted: %s / %v / %v", executable, admitted, err)
			}
		})
	}
}

func TestGitLabJobTokenRequiresExactRedirectRefusal(t *testing.T) {
	path := testPath(t)
	for _, test := range []struct {
		name   string
		change func([]string) []string
	}{
		{"missing key", func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "GIT_CONFIG_KEY_4=") })
		}},
		{"missing value", func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "GIT_CONFIG_VALUE_4=") })
		}},
		{"altered refusal mode", func(env []string) []string { return replace(env, "GIT_CONFIG_VALUE_4", "initial") }},
		{"different key", func(env []string) []string { return replace(env, "GIT_CONFIG_KEY_4", "http.extraHeader") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable, admitted, err := Admit("https://example.test/source", test.change(gitLabProfile(path)))
			if executable != "" || admitted != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("GitLab profile without the exact redirect refusal admitted: %s / %v / %v", executable, admitted, err)
			}
		})
	}
}

func TestGitLabJobTokenRefusesEmptyAndControlToken(t *testing.T) {
	path := testPath(t)
	for _, test := range []struct {
		name, token string
	}{
		{"empty", ""},
		{"carriage return", "synthetic\r-token"},
		{"line feed", "synthetic\n-token"},
		{"nul", "synthetic\x00-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable, admitted, err := Admit("https://example.test/source", replace(gitLabProfile(path), Token, test.token))
			if executable != "" || admitted != nil || errorCode(err) != e.CodeFetchFailed {
				t.Fatalf("GitLab job token with %q state admitted: %s / %v / %v", test.name, executable, admitted, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic") {
				t.Fatalf("token diagnostic exposed the credential value: %v", err)
			}
		})
	}
}

func TestGitLabJobTokenRefusesUnsafeConfigurations(t *testing.T) {
	path := testPath(t)
	for _, test := range []struct {
		name       string
		repository string
		change     func([]string) []string
		code       e.Code
	}{
		{"wrong canonical repository", "https://foreign.test/source", func(env []string) []string { return env }, e.CodeEngineCapability},
		{"credential mirror mismatch", "https://example.test/source", func(env []string) []string {
			return replace(env, "GIT_CONFIG_KEY_2", "credential.https://foreign.test/source.helper")
		}, e.CodeEngineCapability},
		{"non HTTPS mirror", "https://example.test/source", func(env []string) []string {
			return replace(env, "GIT_CONFIG_KEY_0", "url.http://mirror.test/source.insteadOf")
		}, e.CodeEngineCapability},
		{"mirror with credentials", "https://example.test/source", func(env []string) []string {
			return replace(env, "GIT_CONFIG_KEY_0", "url.https://user:pass@mirror.test/source.insteadOf")
		}, e.CodeEngineCapability},
		{"mirror with query", "https://example.test/source", func(env []string) []string {
			return replace(env, "GIT_CONFIG_KEY_0", "url.https://mirror.test/source?query=1.insteadOf")
		}, e.CodeEngineCapability},
		{"mirror with fragment", "https://example.test/source", func(env []string) []string {
			return replace(env, "GIT_CONFIG_KEY_0", "url.https://mirror.test/source#fragment.insteadOf")
		}, e.CodeEngineCapability},
		{"arbitrary helper", "https://example.test/source", func(env []string) []string {
			return replace(env, "GIT_CONFIG_VALUE_2", "!arbitrary-secret-code")
		}, e.CodeEngineCapability},
		{"arbitrary username helper", "https://example.test/source", func(env []string) []string {
			return replace(env, "GIT_CONFIG_VALUE_2", `!f() { if [ "$1" = get ]; then printf '%s\n' 'username=foreign-account' "password=$NVKR_SOURCE_TOKEN"; fi; }; f`)
		}, e.CodeEngineCapability},
		{"missing helper reset", "https://example.test/source", func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "GIT_CONFIG_KEY_1=") })
		}, e.CodeEngineCapability},
		{"missing useHttpPath", "https://example.test/source", func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "GIT_CONFIG_KEY_3=") })
		}, e.CodeEngineCapability},
		{"missing token", "https://example.test/source", func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, Token+"=") })
		}, e.CodeFetchFailed},
		{"extra environment field", "https://example.test/source", func(env []string) []string {
			return append(env, "HOME=/secret/home")
		}, e.CodeEngineCapability},
		{"duplicate environment field", "https://example.test/source", func(env []string) []string {
			return append(env, "GIT_CONFIG_VALUE_3=true")
		}, e.CodeEngineCapability},
		{"count six", "https://example.test/source", func(env []string) []string {
			return append(replace(env, "GIT_CONFIG_COUNT", "6"), "GIT_CONFIG_KEY_5=credential.helper", "GIT_CONFIG_VALUE_5=")
		}, e.CodeEngineCapability},
		{"host Git SSH field", "https://example.test/source", func(env []string) []string {
			return append(env, "GIT_SSH_COMMAND=ssh -o SyntheticPrivateOption=1")
		}, e.CodeEngineCapability},
		{"host askpass field", "https://example.test/source", func(env []string) []string {
			return append(env, "GIT_ASKPASS=/secret/askpass")
		}, e.CodeEngineCapability},
		{"host HTTP proxy", "https://example.test/source", func(env []string) []string {
			return append(env, "HTTP_PROXY=http://proxy.invalid")
		}, e.CodeEngineCapability},
		{"host HTTPS proxy", "https://example.test/source", func(env []string) []string {
			return append(env, "HTTPS_PROXY=http://proxy.invalid")
		}, e.CodeEngineCapability},
		{"host all proxy", "https://example.test/source", func(env []string) []string {
			return append(env, "ALL_PROXY=socks5://proxy.invalid")
		}, e.CodeEngineCapability},
		{"host no proxy", "https://example.test/source", func(env []string) []string {
			return append(env, "NO_PROXY=mirror.test")
		}, e.CodeEngineCapability},
		{"registry credential field", "https://example.test/source", func(env []string) []string {
			return append(env, "CI_REGISTRY_PASSWORD=synthetic-registry-credential")
		}, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable, admitted, err := Admit(test.repository, test.change(slices.Clone(gitLabProfile(path))))
			if executable != "" || admitted != nil || errorCode(err) != test.code {
				t.Fatalf("unsafe GitLab configuration admitted: %s / %v / %v", executable, admitted, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-token") {
				t.Fatal("refusal exposed the credential value")
			}
		})
	}
}

// realGitLabEnvironment admits the exact closed GitLab profile against the real
// native Git executable's own directory. A missing Git is a failed prerequisite,
// never a skip.
func realGitLabEnvironment(t *testing.T) (string, []string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("real Git executable is a required prerequisite for the helper protocol: %v", err)
	}
	executable, admitted, err := Admit("https://example.test/source", gitLabProfile(filepath.Dir(git)))
	if err != nil || executable != git {
		t.Fatalf("real GitLab job-token profile not admitted: %s / %v / %v", executable, admitted, err)
	}
	return executable, admitted
}

// runCredentialProtocol runs one real Git credential operation with the admitted
// environment and the supplied synthetic request. It reports only exit status
// and never prints captured credential output.
func runCredentialProtocol(t *testing.T, operation, request string) (string, error) {
	t.Helper()
	git, environment := realGitLabEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "credential", operation)
	cmd.Env = environment
	cmd.Stdin = strings.NewReader(request)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := ownedcmd.Run(ctx, cmd)
	return stdout.String(), err
}

// credentialFields extracts the username and password lines without echoing them.
func credentialFields(output string) (username, password string, complete bool) {
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "username="):
			username = strings.TrimPrefix(line, "username=")
		case strings.HasPrefix(line, "password="):
			password = strings.TrimPrefix(line, "password=")
		}
	}
	return username, password, username != "" && password != ""
}

// boundarySnapshot captures the complete file state of the isolated test-owned
// working directory: every path with its mode and, for regular files, the
// SHA-256 digest of its exact bytes. It refuses token-content matching, so any
// new or modified state is detected regardless of its content.
type boundarySnapshot map[string]string

func snapshotBoundary(t *testing.T, dir string) boundarySnapshot {
	t.Helper()
	snapshot := boundarySnapshot{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative] = info.Mode().String()
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		snapshot[relative] = info.Mode().String() + ":" + hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatalf("cannot snapshot the isolated test-owned working directory: %v", err)
	}
	return snapshot
}

// boundaryChangedError compares two snapshots of the same isolated test-owned
// working directory and reports any new, removed or modified entry. Diagnostics
// name boundary paths only; they never contain credential values.
func boundaryChangedError(before, after boundarySnapshot) error {
	var changed []string
	for path, state := range after {
		if previous, present := before[path]; !present || previous != state {
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, present := after[path]; !present {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	if len(changed) > 0 {
		return fmt.Errorf("%d unexpected working-directory state change(s): %v", len(changed), changed)
	}
	return nil
}

func TestGitLabJobTokenRealHelperFillsSyntheticCredential(t *testing.T) {
	output, err := runCredentialProtocol(t, "fill", "url=https://mirror.test/source\n\n")
	if err != nil {
		t.Fatalf("real Git credential fill failed without printing credential output: %v", err)
	}
	username, password, complete := credentialFields(output)
	if !complete || username != "gitlab-ci-token" || password != "synthetic-token" {
		t.Fatalf("credential fill returned unexpected fields: fixed-username=%t synthetic-password=%t complete=%t",
			username == "gitlab-ci-token", password == "synthetic-token", complete)
	}
}

func TestGitLabJobTokenRealHelperStoreErasePersistNothing(t *testing.T) {
	git, environment := realGitLabEnvironment(t)
	request := "url=https://mirror.test/source\nusername=gitlab-ci-token\npassword=synthetic-token\n\n"
	for _, operation := range []string{"approve", "reject"} {
		t.Run(operation, func(t *testing.T) {
			// The real child runs inside one isolated test-owned working
			// directory through cmd.Dir, with the exact admitted environment:
			// no HOME or other field is appended and the admitted production
			// environment remains exactly as Admit returned it.
			state := t.TempDir()
			before := snapshotBoundary(t, state)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, git, "credential", operation)
			cmd.Dir = state
			cmd.Env = environment
			cmd.Stdin = strings.NewReader(request)
			if err := ownedcmd.Run(ctx, cmd); err != nil {
				t.Fatalf("real Git credential %s failed without printing credential output: %v", operation, err)
			}
			// Independently, immediately after this single operation, refuse
			// any new, removed or modified state of this boundary so a
			// store-then-erase regression cannot cancel out unnoticed.
			after := snapshotBoundary(t, state)
			if err := boundaryChangedError(before, after); err != nil {
				t.Fatalf("real Git credential %s changed the test-owned working directory: %v", operation, err)
			}
		})
	}
	// A sibling repository must not receive the selected helper credential: the
	// helper is repository-scoped. This sibling-isolation control is separate
	// from the no-write assertions above.
	output, err := runCredentialProtocol(t, "fill", "url=https://mirror.test/sibling\n\n")
	if err == nil {
		t.Fatal("sibling repository unexpectedly received a credential")
	}
	if username, _, complete := credentialFields(output); complete || username != "" {
		t.Fatal("sibling fill exposed a credential")
	}
}

func TestGitLabJobTokenPersistenceAssertionDetectsDeliberateMutation(t *testing.T) {
	git, environment := realGitLabEnvironment(t)
	// The deliberate write runs through the same owned-command harness and the
	// same isolated test-owned working-directory boundary. Only this private
	// test copy of the child environment is mutated so the real Git
	// credential-store helper aims its persistence at a file inside that
	// boundary through its documented --file argument with a quoted target;
	// the admitted production environment is never altered and no HOME value
	// is appended.
	state := t.TempDir()
	mutated := replace(slices.Clone(environment), "GIT_CONFIG_VALUE_1",
		`store --file="`+filepath.Join(state, "deliberate.git-credentials")+`"`)
	before := snapshotBoundary(t, state)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "credential", "approve")
	cmd.Dir = state
	cmd.Env = mutated
	cmd.Stdin = strings.NewReader("url=https://mirror.test/source\nusername=gitlab-ci-token\npassword=synthetic-token\n\n")
	if err := ownedcmd.Run(ctx, cmd); err != nil {
		t.Fatalf("deliberately mutated credential store did not run: %v", err)
	}
	// First prove the deliberate write really occurred inside this boundary;
	// otherwise the detection check below would be vacuous.
	if _, err := os.Stat(filepath.Join(state, "deliberate.git-credentials")); err != nil {
		t.Fatalf("deliberate persistence mutation did not create state inside the boundary: %v", err)
	}
	after := snapshotBoundary(t, state)
	// The exact same state-comparison assertion used after approve/reject must
	// detect that deliberate write, proving the no-write assertion is not a no-op.
	if err := boundaryChangedError(before, after); err == nil {
		t.Fatal("the no-write assertion failed to detect the deliberate persistence mutation")
	}
}
