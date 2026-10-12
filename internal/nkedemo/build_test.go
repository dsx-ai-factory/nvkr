// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	"github.com/NVIDIA/nke-recipes/internal/sourcegit"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestBuildOnlyPreservesDeveloperGitHome(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, home := range []string{"", "/developer/home"} {
		t.Run("HOME="+home, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("GH_TOKEN", "not-forwarded")
			build, err := localBuildEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			if got := environmentValue(build, "HOME"); got != home {
				t.Fatalf("build HOME=%q, want %q", got, home)
			}
			for _, item := range build {
				if item == "HOME=" {
					t.Fatal("empty HOME must remain absent")
				}
			}
			if environmentValue(build, "CONTAINER_RUNTIME") != "docker" || environmentValue(build, "GH_TOKEN") != "" {
				t.Fatal("build runtime or credential isolation changed")
			}
			if environmentValue(NativeEnvironment(), "HOME") != "" {
				t.Fatal("deployment inherited developer Git HOME")
			}
		})
	}
}

func TestBuildTransportSettingsReachOnlyGitFetch(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_SSH_COMMAND", "unselected-callback")
	keys := []string{"https_proxy", "http_proxy", "no_proxy", "ALL_PROXY", "all_proxy",
		"GIT_SSL_CAINFO", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	for _, key := range keys {
		t.Setenv(key, "synthetic-transport-setting")
	}
	build, err := localBuildEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"fetch", "init", "checkout", "status"} {
		t.Run(operation, func(t *testing.T) {
			cmd, err := sourcegit.LocalCommand(t.Context(), build, "", operation)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range keys {
				want := ""
				if operation == "fetch" {
					want = "synthetic-transport-setting"
				}
				if got := environmentValue(cmd.Env, key); got != want {
					t.Fatalf("%s received %s=%q, want %q", operation, key, got, want)
				}
			}
			if environmentValue(cmd.Env, "GIT_SSH_COMMAND") != "" {
				t.Fatal("unselected SSH command reached Git")
			}
		})
	}
	deployment := NativeEnvironment()
	for _, key := range append(keys, "GIT_SSH_COMMAND") {
		if environmentValue(deployment, key) != "" {
			t.Fatalf("deployment inherited source acquisition setting %s", key)
		}
	}
}

func TestBuildLocalSourceRedirectsAreValidatedAndFetchOnly(t *testing.T) {
	for _, test := range []struct {
		name, count, key string
		valid            bool
	}{
		{"local OVN", "1", "url./local/ovn.git.insteadOf", true},
		{"none", "0", "", true},
		{"callback", "1", "credential.helper", false},
		{"relative", "1", "url.relative.insteadOf", false},
		{"unbounded", "17", "", false},
		{"malformed", "invalid", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("GIT_CONFIG_COUNT", test.count)
			t.Setenv("GIT_CONFIG_KEY_0", test.key)
			t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/dsx-ai-factory/ovn-kubernetes")
			t.Setenv("GIT_CONFIG_KEY_9", "credential.helper")
			t.Setenv("GIT_CONFIG_VALUE_9", "!unselected-callback")
			build, err := localBuildEnvironment()
			if !test.valid {
				var coded *nkderr.Error
				if build != nil || !errors.As(err, &coded) || coded.Code != nkderr.CodeEngineCapability {
					t.Fatalf("invalid redirect admitted: %v", err)
				}
				return
			}
			if err != nil || environmentValue(build, "GIT_CONFIG_COUNT") != test.count || environmentValue(build, "GIT_CONFIG_VALUE_9") != "" {
				t.Fatalf("redirect projection changed: %v", err)
			}
			for _, operation := range []string{"fetch", "init", "checkout", "status"} {
				cmd, err := sourcegit.LocalCommand(t.Context(), build, "", operation)
				if err != nil {
					t.Fatal(err)
				}
				want := ""
				if operation == "fetch" && test.count == "1" {
					want = test.key
				}
				if got := environmentValue(cmd.Env, "GIT_CONFIG_KEY_1"); got != want {
					t.Fatalf("%s redirect=%q, want %q", operation, got, want)
				}
			}
			if environmentValue(NativeEnvironment(), "GIT_CONFIG_COUNT") != "" {
				t.Fatal("deployment inherited build source redirects")
			}
		})
	}
}

func TestOnlyBuildLauncherStreamsOutput(t *testing.T) {
	for _, stream := range []bool{false, true} {
		var progress, output bytes.Buffer
		runner := Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: &progress}
		if stream {
			runner.output = &output
		}
		if err := runner.Run(t.Context(), []string{"sh", "-c", "printf safe-build-output"}, "stream-contract", 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(progress.Bytes(), []byte("safe-build-output")) {
			t.Fatal("private command diagnostics streamed through phasewriter")
		}
		if stream && output.String() != "safe-build-output" || !stream && output.Len() != 0 {
			t.Fatal("build stream opt-in was not respected")
		}
	}
}

func TestBuildInvokesOneCombinedRun(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			dir := privateTestDir(t)
			script, log := filepath.Join(dir, "launcher.sh"), filepath.Join(dir, "calls")
			body := "#!/bin/sh\nset -eu\nprintf '%s:%s:%s:%s\\n' \"$1\" \"$2\" \"$3\" \"$4\" >>\"$CALLS\"\n[ \"$FAIL\" = false ]\n"
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			failValue := "false"
			if fail {
				failValue = "true"
			}
			cfg := BuildConfig{Repository: dir, AttemptPrefix: "fresh", Jobs: 4}
			bootstrap := filepath.Join(dir, ".nkd/laptop-bootstrap/fresh")
			if err := os.MkdirAll(bootstrap, 0700); err != nil {
				t.Fatal(err)
			}
			writeInvocationFixture(t, cfg, preparedInvocationFixture(cfg))
			if err := os.WriteFile(filepath.Join(bootstrap, "nkdctl"), []byte("#!/bin/sh\nprintf 'final:%s\\n' \"$*\" >>\"$CALLS\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			runner := Runner{Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + log, "FAIL=" + failValue}}
			err := buildLocalArtifacts(t.Context(), runner, script, cfg)
			if (err != nil) != fail {
				t.Fatalf("error=%v failure=%t", err, fail)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := "--prepare-only:fresh:4:all\n"
			if !fail {
				want += "final:" + strings.Join(append(preparedInvocationFixture(cfg)[1:], "--build-activity-fd", "3"), " ") + "\n"
			}
			if string(calls) != want {
				t.Fatalf("calls=%q, want preparation then one exact combined Run: %q", calls, want)
			}
		})
	}
}

func TestBuildCanceledPreparationCannotLaunchFinalRun(t *testing.T) {
	dir := privateTestDir(t)
	cfg := BuildConfig{Repository: dir, AttemptPrefix: "fresh", Jobs: 4}
	writeInvocationFixture(t, cfg, preparedInvocationFixture(cfg))
	log := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "launcher.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf prepare >>\"$CALLS\"\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, ".nkd/laptop-bootstrap/fresh/nkdctl")
	if err := os.WriteFile(final, []byte("#!/bin/sh\nprintf final >>\"$CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := buildLocalArtifacts(ctx, Runner{Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + log}}, script, cfg)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("preparation cancellation lost: %v", err)
	}
	raw, readErr := os.ReadFile(log)
	if readErr != nil || string(raw) != "prepare" {
		t.Fatalf("canceled preparation launched final child: %q %v", raw, readErr)
	}
}

func TestBuildNetworkFlags(t *testing.T) {
	for _, test := range []struct {
		mode string
		want []string
		fail bool
	}{
		{"", nil, false},
		{"ovn", nil, false},
		{"kind-calico", nil, false},
		{"upstream-ovn", []string{"--network", "upstream-ovn"}, false},
		{"unknown", nil, true},
	} {
		t.Run(test.mode, func(t *testing.T) {
			flags, err := buildNetworkFlags(test.mode)
			if test.fail {
				var coded *nkderr.Error
				if !errors.As(err, &coded) || coded.Code != nkderr.CodeUsage || flags != nil {
					t.Fatalf("invalid mode admitted: %v %v", flags, err)
				}
			} else if err != nil || !slices.Equal(flags, test.want) {
				t.Fatalf("mode flags=%v err=%v, want %v", flags, err, test.want)
			}
		})
	}
}

func TestPreparedInvocationUsesSelectedAuthoredDefinition(t *testing.T) {
	for _, test := range []struct {
		mode, definition string
		valid            bool
	}{
		{"ovn", "definition.json", true},
		{"kind-calico", "definition.json", true},
		{"upstream-ovn", "definition-upstream-ovn.json", true},
		{"upstream-ovn", "definition.json", false},
		{"ovn", "definition-upstream-ovn.json", false},
	} {
		t.Run(test.mode+"/"+test.definition, func(t *testing.T) {
			cfg := BuildConfig{Repository: privateTestDir(t), AttemptPrefix: "fresh", Jobs: 4, Network: test.mode}
			args := preparedInvocationFixture(cfg)
			args[4] = "definitions/nke-local-dev-arm64/" + test.definition
			writeInvocationFixture(t, cfg, args)
			got, err := preparedBuildInvocation(cfg)
			if test.valid {
				if err != nil || !slices.Equal(got, args) {
					t.Fatalf("selected authored definition lost: %v", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("other network's authored definition admitted")
			}
		})
	}
}

func TestUpstreamBuildPreparationKeepsExplicitMode(t *testing.T) {
	dir := privateTestDir(t)
	cfg := BuildConfig{Repository: dir, AttemptPrefix: "fresh", Jobs: 4, Network: "upstream-ovn"}
	args := preparedInvocationFixture(cfg)
	args[4] = "definitions/nke-local-dev-arm64/definition-upstream-ovn.json"
	writeInvocationFixture(t, cfg, args)
	log, script := filepath.Join(dir, "calls"), filepath.Join(dir, "launcher.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'prepare:%s\\n' \"$*\" >>\"$CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, args[0])
	if err := os.WriteFile(final, []byte("#!/bin/sh\nprintf 'final:%s\\n' \"$*\" >>\"$CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := buildLocalArtifacts(t.Context(), Runner{Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + log}}, script, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	want := "prepare:--prepare-only --network upstream-ovn fresh 4 all\nfinal:" + strings.Join(append(args[1:], "--build-activity-fd", "3"), " ") + "\n"
	if err != nil || string(raw) != want {
		t.Fatalf("upstream selection changed: %v %q", err, raw)
	}
}

func preparedInvocationFixture(cfg BuildConfig) []string {
	root := ".nkd/laptop-bootstrap/" + cfg.AttemptPrefix
	return []string{root + "/nkdctl", "run", "local", "--definition", root + "/definition.json", "--environment", "definitions/nke-local-dev-arm64/environment-local-docker.json", "--attempt", cfg.AttemptPrefix, "--attempts-root", ".nkd/runs", "--runner-source", ".", "--jobs", "4", "--runtime", "docker"}
}

func writeInvocationFixture(t *testing.T, cfg BuildConfig, args []string) {
	t.Helper()
	path := filepath.Join(cfg.Repository, ".nkd/laptop-bootstrap", cfg.AttemptPrefix, "build-invocation.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedInvocationRejectsSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		edit   func([]string) []string
		unsafe string
	}{
		{"valid", func(args []string) []string { return args }, ""},
		{"other executable", func(args []string) []string { args[0] = "/bin/sh"; return args }, ""},
		{"other definition", func(args []string) []string { args[4] = "/private/other.json"; return args }, ""},
		{"different jobs", func(args []string) []string { args[14] = "8"; return args }, ""},
		{"extra flags", func(args []string) []string { return append(args, "--publish") }, ""},
		{"short argv", func(args []string) []string { return args[:4] }, ""},
		{"world readable", func(args []string) []string { return args }, "mode"},
		{"linked file", func(args []string) []string { return args }, "hardlink"},
		{"symlink", func(args []string) []string { return args }, "symlink"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := BuildConfig{Repository: privateTestDir(t), AttemptPrefix: "fresh", Jobs: 4}
			args := test.edit(preparedInvocationFixture(cfg))
			writeInvocationFixture(t, cfg, args)
			path := filepath.Join(cfg.Repository, ".nkd/laptop-bootstrap/fresh/build-invocation.json")
			switch test.unsafe {
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".link"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			}
			got, err := preparedBuildInvocation(cfg)
			if test.name == "valid" {
				if err != nil || !slices.Equal(got, args) {
					t.Fatalf("valid invocation lost: %v", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("substituted private invocation admitted")
			}
		})
	}
}

func TestPreparedBuildPreservesExactFetchRedirects(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "NKE_LOCAL_CLONE=/local/nke.git", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url./local/ovn.git.insteadOf", "GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/ovn-kubernetes.git"}
	cfg := BuildConfig{Repository: "/local/recipes", AttemptPrefix: "fresh"}
	got, err := preparedBuildEnvironment(env, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if environmentValue(got, "GIT_CONFIG_COUNT") != "3" || environmentValue(got, "GIT_CONFIG_KEY_0") != environmentValue(env, "GIT_CONFIG_KEY_0") || environmentValue(got, "TMPDIR") != "/local/recipes/.nkd/laptop-bootstrap/fresh/tmp" {
		t.Fatalf("redirect or temporary directory lost: %v", got)
	}
	for i, upstream := range []string{"https://github.com/dsx-ai-factory/nke.git", "https://github.com/dsx-ai-factory/nke"} {
		suffix := strconv.Itoa(i + 1)
		if environmentValue(got, "GIT_CONFIG_KEY_"+suffix) != "url./local/nke.git.insteadOf" || environmentValue(got, "GIT_CONFIG_VALUE_"+suffix) != upstream {
			t.Fatal("NKE redirect changed")
		}
	}
	for _, count := range []string{"15", "17", "-1", "invalid"} {
		if _, err := preparedBuildEnvironment(SetEnv(env, map[string]string{"GIT_CONFIG_COUNT": count}), cfg); err == nil {
			t.Fatal("invalid redirect count accepted")
		}
	}
}

func TestBuildPublicStreamBeyondDiagnosticBound(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"public stream", "capture", "private log", "output error"} {
		t.Run(mode, func(t *testing.T) {
			var output bytes.Buffer
			runner := Runner{Env: []string{"NKD_SUPPORT_HELPER=oversized"}, output: &output}
			failure := errors.New("stream write failed")
			if mode == "private log" {
				runner.Work = privateTestDir(t)
			}
			if mode == "output error" {
				reader, writer := io.Pipe()
				if err := reader.CloseWithError(failure); err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				runner.output = writer
			}
			args := []string{program, "-test.run=TestSupportHelperProcess"}
			var captured []byte
			var err error
			if mode == "capture" {
				captured, err = runner.Capture(t.Context(), args, "large", 10*time.Second)
			} else {
				err = runner.Run(t.Context(), args, "large", 10*time.Second)
			}
			if errors.Is(err, ErrUnjoined) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("output did not drain and join: %v", err)
			}
			if mode == "public stream" {
				const marker = "\n[done] after diagnostic limit\n"
				if err != nil || output.Len() != diagnosticLimit+1024+len(marker) || !bytes.HasSuffix(output.Bytes(), []byte(marker)) {
					t.Fatalf("full public stream lost: bytes=%d error=%v", output.Len(), err)
				}
			} else if err == nil {
				t.Fatal("diagnostic bound or output failure was ignored")
			}
			if mode == "capture" && (len(captured) != diagnosticLimit || output.Len() != diagnosticLimit) {
				t.Fatal("public output opt-in bypassed capture bounds")
			}
			if mode == "private log" {
				info, err := os.Stat(filepath.Join(runner.Work, "large.log"))
				if err != nil || info.Size() != diagnosticLimit || output.Len() != diagnosticLimit {
					t.Fatalf("private log bounds changed: %v", err)
				}
			}
			if mode == "output error" && !errors.Is(err, failure) {
				t.Fatalf("output failure was lost: %v", err)
			}
		})
	}
}

func TestBuiltInputsShareCombinedRun(t *testing.T) {
	built := builtInputs("/repo", "fresh")
	want := RunInputs{OS: "/repo/.nkd/runs/fresh", Components: "/repo/.nkd/runs/fresh", Providers: "/repo/.nkd/runs/fresh", Site: "/repo/.nkd/runs/fresh", Tools: "/repo/.nkd/runs/fresh", Profiles: "/repo/.nkd/runs/fresh", Simulator: "/repo/.nkd/runs/fresh"}
	if built.Runs != want || built.NKDCTL != "/repo/.nkd/laptop-bootstrap/fresh/nkdctl" {
		t.Fatalf("combined inputs=%#v", built)
	}
}

func TestBuildAttemptDiagnostics(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Skip("artifact build admission requires native Linux ARM64")
	}
	for _, root := range []string{"runs", "laptop-bootstrap"} {
		for _, blockedParent := range []bool{false, true} {
			t.Run(root+map[bool]string{false: "/existing", true: "/not-directory"}[blockedParent], func(t *testing.T) {
				repo := privateTestDir(t)
				if err := os.Mkdir(filepath.Join(repo, "scripts"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "scripts/nke-full-arm64-local.sh"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
					t.Fatal(err)
				}
				parent := filepath.Join(repo, ".nkd", root)
				if err := os.MkdirAll(filepath.Dir(parent), 0700); err != nil {
					t.Fatal(err)
				}
				owned := filepath.Join(parent, "retained")
				keep := owned
				if blockedParent {
					keep = parent
				} else if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(keep, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				result, err := BuildArtifacts(t.Context(), BuildConfig{Repository: repo, AttemptPrefix: "retained", Jobs: 2})
				var coded *nkderr.Error
				if result != (BuiltInputs{}) || !errors.As(err, &coded) || coded.Code != nkderr.CodeBuildFailed || !strings.Contains(err.Error(), owned) {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				if blockedParent {
					if !errors.Is(err, unix.ENOTDIR) || !strings.Contains(err.Error(), "cannot inspect") || strings.Contains(err.Error(), "already exists") {
						t.Fatalf("inspection cause obscured: %v", err)
					}
				} else if !strings.Contains(err.Error(), "already exists; choose a fresh --attempt") {
					t.Fatalf("existing path guidance absent: %v", err)
				}
				if raw, err := os.ReadFile(keep); err != nil || string(raw) != "original" {
					t.Fatal("preflight modified retained input")
				}
			})
		}
	}
}

// Positive fixtures select their invocation environment explicitly. Production
// admission still rejects this unselected override; negative tests keep their
// own vectors rather than inheriting a blanket package-wide reset.
func TestPositiveBuildFixturesDoNotInheritUnselectedGitOverrides(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	selected := "^Test(BuildOnlyPreservesDeveloperGitHome|BuildTransportSettingsReachOnlyGitFetch|BuildAttemptDiagnostics|PrepareInstallerPinnedAttempt|PrepareInstallerAttemptDiagnostics|ProgressInstallerAdmissionFailure)$"
	command := exec.CommandContext(ctx, os.Args[0], "-test.run="+selected, "-test.count=1", "-test.timeout=80s")
	command.Env = SetEnv(os.Environ(), map[string]string{
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "safe.directory", "GIT_CONFIG_VALUE_0": "/unselected-fixture",
	})
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := ownedcmd.Run(ctx, command); err != nil {
		t.Fatalf("positive fixtures inherited ambient Git configuration: %v\n%s", err, &output)
	}
}
