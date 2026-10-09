// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestBuildOnlyPreservesDeveloperGitHome(t *testing.T) {
	for _, home := range []string{"", "/developer/home"} {
		t.Run("HOME="+home, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("GH_TOKEN", "not-forwarded")
			build := localBuildEnvironment()
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
			body := "#!/bin/sh\nset -eu\nprintf '%s:%s:%s:%s\\n' \"$1\" \"$2\" \"$3\" \"${NKD_LOCAL_BOOTSTRAP:-}\" >>\"$CALLS\"\n[ \"$FAIL\" = false ]\n"
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			failValue := "false"
			if fail {
				failValue = "true"
			}
			runner := Runner{Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + log, "FAIL=" + failValue}}
			err := buildLocalArtifacts(t.Context(), runner, script, BuildConfig{AttemptPrefix: "fresh", Jobs: 4})
			if (err != nil) != fail {
				t.Fatalf("error=%v failure=%t", err, fail)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if string(calls) != "fresh:4:all:\n" {
				t.Fatalf("calls=%q, want one combined Run with no previous bootstrap", calls)
			}
		})
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
