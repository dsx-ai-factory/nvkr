// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOSDockerBootstrapUsesPrivateSharedTemporaryDirectory(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("OS wrapper requires jq")
	}
	source, err := os.ReadFile(demoScript(t, "nke-os-local.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"scripts/nke-os-local.sh":                                             string(source),
				"definitions/nke-os/definition-" + arch + "-kvm.yaml":                 "fixture",
				"definitions/nke-os/environment-local-linux-" + arch + "-docker.yaml": "fixture",
				"bin/docker":                     "#!/bin/sh\nexit 99\n",
				"bin/uname":                      "#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo \"$TEST_MACHINE\";; *) exit 99;; esac\n",
				"toolchains/nkd-packer/build.sh": "#!/bin/sh\nprintf '%s' \"$TMPDIR\" >\"$TEST_TMP_RECORD\"\nexit 42\n",
			}
			for path, contents := range files {
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			machine := "aarch64"
			if arch == "amd64" {
				machine = "x86_64"
			}
			record := filepath.Join(root, "tmpdir-record")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts/nke-os-local.sh"), "test-attempt")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"),
				"CONTAINER_RUNTIME=docker", "NKE_OS_ARCH="+arch, "NKE_LOCAL_CLONE=",
				"TEST_MACHINE="+machine, "TEST_TMP_RECORD="+record)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 42 {
				t.Fatalf("did not reach toolchain preparation: %v\n%s", err, output)
			}
			raw, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(root, ".nkd/bootstrap/test-attempt/tmp")
			if strings.TrimSpace(string(raw)) != want {
				t.Fatalf("TMPDIR = %q, want %q", raw, want)
			}
			info, err := os.Stat(want)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("temporary directory is not private: info=%v, err=%v", info, err)
			}
		})
	}
}
