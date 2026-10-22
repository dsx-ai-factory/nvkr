// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuickstartFailureKeepsCallerShellAlive(t *testing.T) {
	for _, tc := range []struct{ name, status string }{{"docker", "1"}, {"compiler", "66"}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			scripts := filepath.Join(root, "scripts")
			if err := os.Mkdir(scripts, 0o700); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile("nvkr-quickstart.sh")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(scripts, "nvkr-quickstart.sh"), body, 0o700); err != nil {
				t.Fatal(err)
			}
			version, err := os.ReadFile("../.go-version")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".go-version"), version, 0o600); err != nil {
				t.Fatal(err)
			}
			tools := filepath.Join(root, "tools")
			if err := os.Mkdir(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			docker := "#!/bin/sh\nif [ \"$1\" = info ] && [ \"$FAIL_STAGE\" = compiler ]; then exit 0; fi\nexit 66\n"
			if err := os.WriteFile(filepath.Join(tools, "docker"), []byte(docker), 0o700); err != nil {
				t.Fatal(err)
			}
			goTool := "#!/bin/sh\nif [ \"$1\" = env ]; then printf 'go%s\\n' \"$PINNED_GO\"; exit 0; fi\nexit 66\n"
			if err := os.WriteFile(filepath.Join(tools, "go"), []byte(goTool), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tools, "git"), []byte("#!/bin/sh\nprintf 'synthetic-version\\n'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c",
				`bash "$1"; result=$?; printf 'CALLER_ALIVE child_status=%s\n' "$result"`,
				"caller", filepath.Join(scripts, "nvkr-quickstart.sh"))
			cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "FAIL_STAGE="+tc.name, "PINNED_GO="+strings.TrimSpace(string(version)))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("caller shell exited: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "CALLER_ALIVE child_status="+tc.status) {
				t.Fatalf("caller did not report the child failure:\n%s", out)
			}
			if strings.Contains(string(out), "Build complete.") {
				t.Fatalf("failed build reported success:\n%s", out)
			}
		})
	}
}
