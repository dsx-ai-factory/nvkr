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

// TestQuickstartSetupAndFailureHandling uses synthetic tools to check consent, SDK activation and build failures.
func TestQuickstartSetupAndFailureHandling(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stage        string
		args         []string
		status       int
		download     bool
		compilerUsed bool
	}{
		{name: "docker unavailable", stage: "docker", status: 1},
		{name: "old git", stage: "git", status: 1},
		{name: "compiler failure", stage: "compiler", status: 66, compilerUsed: true},
		{name: "nvkr failure", stage: "nvkr", status: 73, compilerUsed: true},
		{name: "mismatched compiler needs consent", stage: "consent", status: 1, compilerUsed: true},
		{name: "checksum failure never activates SDK", stage: "checksum", args: []string{"--install-go"}, status: 74, download: true, compilerUsed: true},
		{name: "matching compiler", stage: "matching", compilerUsed: true},
		{name: "install consent reuses matching compiler", stage: "matching", args: []string{"--install-go"}, compilerUsed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(name string, body []byte, mode os.FileMode) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, mode); err != nil {
					t.Fatal(err)
				}
			}
			for name, source := range map[string]string{
				"scripts/nvkr-quickstart.sh": "nvkr-quickstart.sh",
				".go-version":                "../.go-version",
			} {
				body, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				write(name, body, 0o700)
			}
			checksums, err := filepath.Glob("../toolchains/quickstart/*.sha256")
			if err != nil || len(checksums) != 4 {
				t.Fatalf("SDK checksum inventory: %v, %v", checksums, err)
			}
			for _, path := range checksums {
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				write(filepath.Join("toolchains/quickstart", filepath.Base(path)), body, 0o600)
			}
			write("tools/docker", []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$DOCKER_LOG"
if [ "$1" = info ]; then
  if [ "$FAIL_STAGE" = docker ]; then exit 66; fi
  exit 0
fi
# Model a failed download/checksum command before extraction or SDK activation.
exit 74
`), 0o700)
			write("tools/go", []byte(`#!/bin/sh
printf 'GOTOOLCHAIN=%s %s\n' "${GOTOOLCHAIN:-}" "$*" >> "$GO_LOG"
if [ "$1" = env ]; then
  case "$FAIL_STAGE" in
    consent|checksum)
      # A switching probe would hide the mismatch and bypass consent.
      if [ "${GOTOOLCHAIN:-}" = local ]; then echo go0.0.0; else printf 'go%s\n' "$PINNED_GO"; fi ;;
    *) printf 'go%s\n' "$PINNED_GO" ;;
  esac
  exit 0
fi
if [ "$FAIL_STAGE" = compiler ]; then exit 66; fi
mkdir -p bin
cat > bin/nvkr <<'NVKR'
#!/bin/sh
if [ "$1" = --version ]; then echo synthetic-version; exit 0; fi
if [ "$FAIL_STAGE" = nvkr ]; then exit 73; fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = --attempt ]; then shift; attempt="$1"; fi
  shift
done
for output in nvkectl nvke-controller nvke-controller-chart; do
  mkdir -p ".nvkr/runs/$attempt/$output/out/$output"
  printf 'synthetic payload\n' > ".nvkr/runs/$attempt/$output/out/$output/payload"
done
NVKR
chmod 700 bin/nvkr
`), 0o700)
			write("tools/git", []byte(`#!/bin/sh
if [ "$1" = ls-files ] && [ "$FAIL_STAGE" = git ]; then exit 64; fi
printf 'synthetic-version\n'
`), 0o700)
			version, err := os.ReadFile(filepath.Join(root, ".go-version"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			args := append([]string{filepath.Join(root, "scripts/nvkr-quickstart.sh")}, tc.args...)
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(),
				"PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAIL_STAGE="+tc.stage, "PINNED_GO="+strings.TrimSpace(string(version)),
				"GO_LOG="+filepath.Join(root, "go.log"), "DOCKER_LOG="+filepath.Join(root, "docker.log"),
				"GOTOOLCHAIN=auto")
			out, err := cmd.CombinedOutput()
			status := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					status = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if status != tc.status {
				t.Fatalf("exit %d, want %d: %v\n%s", status, tc.status, err, out)
			}
			if strings.Contains(string(out), "Build complete.") != (tc.status == 0) {
				t.Fatalf("incorrect completion message:\n%s", out)
			}
			if tc.stage == "consent" && !strings.Contains(string(out), "Installation needs confirmation") {
				t.Fatalf("mismatched compiler bypassed consent:\n%s", out)
			}
			dockerLog, err := os.ReadFile(filepath.Join(root, "docker.log"))
			if err != nil && tc.stage != "git" {
				t.Fatal(err)
			}
			if strings.Contains(string(dockerLog), "run ") != tc.download {
				t.Fatalf("unexpected downloader execution: %s", dockerLog)
			}
			if tc.compilerUsed {
				goLog, err := os.ReadFile(filepath.Join(root, "go.log"))
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(strings.TrimSpace(string(goLog)), "\n") {
					if !strings.HasPrefix(line, "GOTOOLCHAIN=local ") {
						t.Fatalf("implicit toolchain switching enabled: %s", goLog)
					}
				}
			}
			installed, err := filepath.Glob(filepath.Join(root, ".nvkr/quickstart-sdk/go*"))
			if err != nil || len(installed) != 0 {
				t.Fatalf("unexpected SDK activation: %v, %v", installed, err)
			}
			if tc.stage == "consent" {
				if _, err := os.Stat(filepath.Join(root, ".nvkr")); !os.IsNotExist(err) {
					t.Fatalf("consent refusal created workspace: %v", err)
				}
			}
		})
	}
}
