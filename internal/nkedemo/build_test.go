// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestBuiltInputsShareCombinedRun(t *testing.T) {
	built := builtInputs("/repo", "fresh")
	want := RunInputs{OS: "/repo/.nkd/runs/fresh", Components: "/repo/.nkd/runs/fresh", Providers: "/repo/.nkd/runs/fresh", Site: "/repo/.nkd/runs/fresh", Tools: "/repo/.nkd/runs/fresh", Profiles: "/repo/.nkd/runs/fresh", Simulator: "/repo/.nkd/runs/fresh"}
	if built.Runs != want || built.NKDCTL != "/repo/.nkd/laptop-bootstrap/fresh/nkdctl" {
		t.Fatalf("combined inputs=%#v", built)
	}
}
