// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestBuildSharesBootstrapOnlyAfterFirstLaneSucceeds(t *testing.T) {
	for _, failedLane := range []string{"", "os", "site"} {
		t.Run("failure-"+failedLane, func(t *testing.T) {
			dir := privateTestDir(t)
			script, log := filepath.Join(dir, "launcher.sh"), filepath.Join(dir, "calls")
			body := "#!/bin/sh\nset -eu\nprintf '%s:%s\\n' \"$3\" \"${NKD_LOCAL_BOOTSTRAP:-}\" >>\"$CALLS\"\n[ \"$3\" != \"$FAIL_LANE\" ]\n"
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			runner := Runner{Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + log, "FAIL_LANE=" + failedLane}}
			lanes := []string{"os", "components", "site", "tools", "profiles", "simulator"}
			err := buildLocalLanes(t.Context(), runner, script, BuildConfig{AttemptPrefix: "fresh", Jobs: 1}, lanes)
			if (err != nil) != (failedLane != "") {
				t.Fatalf("error=%v failure lane=%q", err, failedLane)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"os:"}
			if failedLane != "os" {
				for _, lane := range lanes[1:] {
					want = append(want, lane+":fresh-os")
					if lane == failedLane {
						break
					}
				}
			}
			if string(calls) != strings.Join(want, "\n")+"\n" {
				t.Fatalf("calls=%q, want=%v", calls, want)
			}
		})
	}
}
