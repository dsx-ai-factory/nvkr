// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
)

func TestM1RejectsOccupiedRegistryEndpointBeforeBuilding(t *testing.T) {
	for _, test := range []struct {
		name     string
		running  string
		response int
		wantExit int
	}{
		{"another registry", "missing", http.StatusOK, 2},
		{"different running container", "other", http.StatusOK, 2},
		{"similar running container name", "similar", http.StatusOK, 2},
		{"stopped selected container", "false", http.StatusOK, 2},
		{"authenticated service", "missing", http.StatusUnauthorized, 2},
		{"unhealthy service", "missing", http.StatusServiceUnavailable, 2},
		{"selected running container", "true", http.StatusOK, 42},
		{"no HTTP response", "missing", 0, 42},
		{"Docker query failed", "error", 0, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.response == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.WriteHeader(test.response)
			}))
			defer server.Close()
			root := t.TempDir()
			scripts := filepath.Join(root, "scripts")
			if err := os.Mkdir(scripts, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"demo-m1-local.sh", "_demo-common.sh"} {
				raw, err := os.ReadFile(demoScript(t, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(scripts, name), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tools := filepath.Join(root, "tools")
			if err := os.Mkdir(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, source := range map[string]string{
				"docker": "#!/bin/sh\ncase \"$1:$2\" in info:) exit 0;; container:ls) case \"$REGISTRY_STATE\" in error) exit 55;; true) printf '%s\\n' nvkr-m1-registry;; other) printf '%s\\n' other-registry;; similar) printf '%s\\n' nvkr-m1-registry-other;; esac;; *) exit 99;; esac\n",
				"make":   "#!/bin/sh\nprintf build > \"$BUILD_MARKER\"\nexit 42\n",
			} {
				if err := os.WriteFile(filepath.Join(tools, name), []byte(source), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "build-started")
			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(scripts, "demo-m1-local.sh"))
			cmd.Env = []string{"PATH=" + tools + string(os.PathListSeparator) + os.Getenv("PATH"),
				"REGISTRY_STATE=" + test.running, "BUILD_MARKER=" + marker,
				"REGISTRY_HOSTPORT=" + strings.TrimPrefix(server.URL, "http://")}
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != test.wantExit {
				t.Fatalf("exit=%v, want %d: %s", err, test.wantExit, output)
			}
			_, statErr := os.Stat(marker)
			if test.wantExit == 2 {
				wantDiagnostic := "already in use"
				if test.running == "error" {
					wantDiagnostic = "cannot read running registry containers"
				}
				if !os.IsNotExist(statErr) || !strings.Contains(string(output), wantDiagnostic) {
					t.Fatalf("occupied endpoint reached the build or lacked a diagnostic: %v %s", statErr, output)
				}
			} else if statErr != nil {
				t.Fatalf("permitted endpoint did not reach the build: %v %s", statErr, output)
			}
		})
	}
}

func TestCatalogChangeScriptEmitsAdmittedAbsoluteSourceRedirect(t *testing.T) {
	temp := t.TempDir()
	captured := filepath.Join(temp, "redirect")
	recorder := filepath.Join(temp, "nvkr")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nprintf '%s\\n' \"GIT_CONFIG_COUNT=$GIT_CONFIG_COUNT\" \"GIT_CONFIG_KEY_0=$GIT_CONFIG_KEY_0\" \"GIT_CONFIG_VALUE_0=$GIT_CONFIG_VALUE_0\" > \"$CAPTURE_REDIRECT\"\nexit 42\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(temp, "work")
	cmd := exec.CommandContext(t.Context(), "bash", demoScript(t, "demo-catalog-change.sh"), "--work", work)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NVKR_BIN=" + recorder, "CAPTURE_REDIRECT=" + captured,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 42 {
		t.Fatalf("catalog fixture failed before captured invocation: %v %s", err, output)
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	environment := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	projected, err := sourcegit.LocalRedirectEnvironment(environment)
	if err != nil || strings.Join(projected, "\n") != strings.Join(environment, "\n") {
		t.Fatalf("actual catalog script emitted an unsupported redirect: %v", err)
	}
	if environment[1] != "GIT_CONFIG_KEY_0=url."+filepath.Join(work, "cri-tools.git")+".insteadOf" {
		t.Fatal("catalog script did not preserve its exact absolute fixture repository")
	}
}

func TestMaintainedDemoScriptsAdvertiseRunnableHonestJourneys(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"demo-catalog-change.sh", []string{"local Git", "preview bump", "UNQUALIFIED"}},
		{"demo-tamper-detection.sh", []string{"changed artifact digests", "escaping symlinks", "UNQUALIFIED"}},
		{"demo-release-consumer.sh", []string{"digest-pinned lifecycle publication", "Verification Policy", "Release capability"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.Command("bash", demoScript(t, test.name), "--help").CombinedOutput()
			if err != nil {
				t.Fatalf("--help failed: %v\n%s", err, output)
			}
			for _, want := range test.want {
				if !strings.Contains(string(output), want) {
					t.Errorf("help is missing %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestReleaseConsumerRequiresOneReferenceAndExplicitPolicy(t *testing.T) {
	raw, err := os.ReadFile(demoScript(t, "demo-release-consumer.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{"release verify", "--reference", "--lane pipeline-attested", "--scope artifact-publication", "--identity", "--issuer", "--cosign-digest"} {
		if !strings.Contains(script, required) {
			t.Errorf("consumer omits %q", required)
		}
	}
	for _, legacy := range []string{"publish pull", "publish extract", "ReleaseSet", "ClusterDefinition"} {
		if strings.Contains(script, legacy) {
			t.Errorf("consumer retains legacy workflow %q", legacy)
		}
	}
}

func TestReleaseConsumerRejectsUnsafeInputsBeforeExecution(t *testing.T) {
	script := demoScript(t, "demo-release-consumer.sh")
	validDigest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"path escape", []string{"--work", ".nvkr/../outside", "--cosign-digest", validDigest}, "unsafe path component"},
		{"nonhex digest", []string{"--work", ".nvkr/release-input-test", "--cosign-digest", "sha256:" + strings.Repeat("g", 64)}, "64 lowercase hex"},
		{"partial exact inputs", []string{"--work", ".nvkr/release-input-test", "--cosign-digest", validDigest, "--definition", "definition.yaml"}, "must be supplied together"},
		{"empty exact inputs", []string{"--work", ".nvkr/release-input-test", "--cosign-digest", validDigest,
			"--definition", "", "--environment", "", "--plan", ""}, "requires a non-empty path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--reference", "registry.test/release@" + validDigest, "--identity", "identity", "--issuer", "issuer", "--cosign", "/bin/true"}
			args = append(args, test.args...)
			cmd := exec.Command("bash", append([]string{script}, args...)...)
			cmd.Env = append(os.Environ(), "NVKR_BIN=/bin/true")
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("unsafe input was not rejected before execution: %v\n%s", err, output)
			}
		})
	}
}

func TestReleaseConsumerForwardsExactInputs(t *testing.T) {
	script := demoScript(t, "demo-release-consumer.sh")
	temp := t.TempDir()
	captured := filepath.Join(temp, "args")
	recorder := filepath.Join(temp, "nvkr")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(".nvkr", "release-input-"+filepath.Base(filepath.Dir(temp))+"-"+filepath.Base(temp))
	workPath := filepath.Join(filepath.Dir(filepath.Dir(script)), work)
	if _, err := os.Lstat(workPath); !os.IsNotExist(err) {
		t.Fatalf("test workspace already exists or cannot be checked: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(workPath); err != nil {
			t.Errorf("remove test workspace: %v", err)
		}
	})

	digest := "sha256:" + strings.Repeat("a", 64)
	definition := "selected-definition.yaml"
	environment := "selected-environment.yaml"
	plan := "selected-plan.json"
	callerDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	callerDir, err = filepath.EvalSymlinks(callerDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script,
		"--runtime", "native",
		"--reference", "registry.test/release@"+digest,
		"--identity", "identity",
		"--issuer", "issuer",
		"--cosign", "/bin/true",
		"--cosign-digest", digest,
		"--definition", definition,
		"--environment", environment,
		"--plan", plan,
		"--work", work,
	)
	cmd.Env = append(os.Environ(), "NVKR_BIN="+recorder, "CAPTURE_ARGS="+captured)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("consumer failed: %v\n%s", err, output)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"--definition", filepath.Join(callerDir, definition),
		"--environment", filepath.Join(callerDir, environment),
		"--plan", filepath.Join(callerDir, plan),
	}, "\n") + "\n"
	if !strings.Contains(string(raw), want) {
		t.Fatalf("consumer did not forward the selected exact inputs:\n%s", raw)
	}
}

func TestReleaseConsumerIgnoresGenericInheritedExactInputNames(t *testing.T) {
	script := demoScript(t, "demo-release-consumer.sh")
	temp := t.TempDir()
	captured := filepath.Join(temp, "args")
	recorder := filepath.Join(temp, "nvkr")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(".nvkr", "release-env-"+filepath.Base(filepath.Dir(temp))+"-"+filepath.Base(temp))
	workPath := filepath.Join(filepath.Dir(filepath.Dir(script)), work)
	t.Cleanup(func() {
		if err := os.RemoveAll(workPath); err != nil {
			t.Errorf("remove test workspace: %v", err)
		}
	})
	digest := "sha256:" + strings.Repeat("a", 64)
	cmd := exec.Command("bash", script,
		"--runtime", "native",
		"--reference", "registry.test/release@"+digest,
		"--identity", "identity", "--issuer", "issuer",
		"--cosign", "/bin/true", "--cosign-digest", digest, "--work", work)
	cmd.Env = append(os.Environ(), "NVKR_BIN="+recorder, "CAPTURE_ARGS="+captured,
		"DEFINITION=unrelated", "ENVIRONMENT=production", "PLAN=unrelated")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("consumer inherited generic exact-input names: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "--definition") || strings.Contains(string(raw), "--environment") || strings.Contains(string(raw), "--plan") {
		t.Fatalf("consumer forwarded inherited generic inputs:\n%s", raw)
	}
}

func TestDemoWorkspaceCanBeRerunOnlyByItsOwner(t *testing.T) {
	common, err := filepath.Abs(filepath.Join("..", "..", "scripts", "_demo-common.sh"))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$COMMON"
demo_prepare_work "$WORK" "test-owner"
printf stale > "$WORK/stale-output"
demo_prepare_work "$WORK" "test-owner"
test ! -e "$WORK/stale-output"
printf current > "$WORK/current-output"
`)
	cmd.Env = append(os.Environ(), "COMMON="+common, "WORK="+work)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("owned workspace did not support a clean rerun: %v\n%s", runErr, output)
	}
	cmd = exec.Command("bash", "-c", `
set -euo pipefail
source "$COMMON"
demo_prepare_work "$WORK" "different-owner"
`)
	cmd.Env = append(os.Environ(), "COMMON="+common, "WORK="+work)
	if output, runErr := cmd.CombinedOutput(); runErr == nil || !strings.Contains(string(output), "belongs to demo") {
		t.Fatalf("cross-demo workspace reuse was not rejected: %v\n%s", runErr, output)
	}
}

func demoScript(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}
