// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseConsumerDockerTransportsAdmittedPlan(t *testing.T) {
	for _, refusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "packed export", true: "load refusal"}[refusal], func(t *testing.T) {
			root := t.TempDir()
			for _, directory := range []string{"scripts", "bin"} {
				if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(name, body string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"demo-release-consumer.sh", "_demo-common.sh"} {
				raw, err := os.ReadFile(demoScript(t, name))
				if err != nil {
					t.Fatal(err)
				}
				write("scripts/"+name, string(raw), 0o700)
			}
			for _, name := range []string{"definition", "environment", "plan", "cosign"} {
				write(name, "fixture", 0o600)
			}
			for _, name := range []string{"go", "make"} {
				write("bin/"+name, "#!/bin/sh\nexit 0\n", 0o700)
			}
			write("bin/docker", "#!/bin/sh\n[ \"$1\" != info ] || exit 0\nprintf '%s\\n' \"$@\" > docker-args\n", 0o700)
			body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > loader-args\nprintf '{\"packed\":true}'\n"
			if refusal {
				body = "#!/bin/sh\nexit 42\n"
			}
			write("bin/host-nvkr", body, 0o700)
			command := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts/demo-release-consumer.sh"),
				"--runtime", "docker", "--reference", "registry.invalid/release@sha256:"+strings.Repeat("a", 64),
				"--identity", "fixture", "--issuer", "fixture", "--cosign", "cosign", "--cosign-digest", "sha256:"+strings.Repeat("b", 64),
				"--definition", "definition", "--environment", "environment", "--plan", "plan", "--work", ".nvkr/consumer")
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "NVKR_BIN="+filepath.Join(root, "bin/host-nvkr"))
			output, err := command.CombinedOutput()
			if refusal {
				if err == nil {
					t.Fatal("Plan load refusal did not stop verification")
				}
				if _, err := os.Stat(filepath.Join(root, "docker-args")); !os.IsNotExist(err) {
					t.Fatal("Plan load refusal reached container verification", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("demo failed: %v\n%s", err, output)
			}
			physical, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(filepath.Join(root, "loader-args"))
			if err != nil || string(args) != "run\nplan\n--plan\n"+filepath.Join(physical, "plan")+"\n--json\n" {
				t.Fatalf("wrong Plan admission: %s %v", args, err)
			}
			packed := filepath.Join(root, ".nvkr/consumer/plan.json")
			raw, err := os.ReadFile(packed)
			if err != nil || string(raw) != `{"packed":true}` {
				t.Fatal("export changed canonical bytes", err)
			}
			args, err = os.ReadFile(filepath.Join(root, "docker-args"))
			if err != nil || !strings.Contains(string(args), packed+":/inputs/plan.json:ro\n") {
				t.Fatalf("container did not receive the packed export: %s %v", args, err)
			}
		})
	}
}
