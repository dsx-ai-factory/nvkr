// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		sdk          string
		wantMessage  string
	}{
		{name: "docker unavailable", stage: "docker", status: 1},
		{name: "old git", stage: "git", status: 1},
		{name: "compiler failure", stage: "compiler", status: 66, compilerUsed: true},
		{name: "nvkr failure", stage: "nvkr", status: 73, compilerUsed: true},
		{name: "mismatched compiler needs consent", stage: "consent", status: 1, compilerUsed: true},
		{name: "checksum failure never activates SDK", stage: "checksum", args: []string{"--install-go"}, status: 74, download: true, compilerUsed: true},
		{name: "matching compiler", stage: "matching", compilerUsed: true},
		{name: "install consent reuses matching compiler", stage: "matching", args: []string{"--install-go"}, compilerUsed: true},
		{name: "cached SDK overrides mismatched system Go", stage: "cached", sdk: "cached", compilerUsed: true},
		{name: "incomplete SDK is preserved", stage: "incomplete", sdk: "incomplete", args: []string{"--install-go"}, status: 1, compilerUsed: true, wantMessage: "An incomplete local SDK exists"},
		{name: "wrong cached SDK is refused", stage: "wrong-cached", sdk: "cached", args: []string{"--install-go"}, status: 1, compilerUsed: true, wantMessage: "An incomplete local SDK exists"},
		{name: "verified download installs SDK", stage: "install", sdk: "install", args: []string{"--install-go"}, download: true, compilerUsed: true},
		{name: "wrong downloaded SDK fails final version gate", stage: "wrong-install", sdk: "install", args: []string{"--install-go"}, status: 1, download: true, compilerUsed: true, wantMessage: "does not match pinned go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
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
			version, err := os.ReadFile(filepath.Join(root, ".go-version"))
			if err != nil {
				t.Fatal(err)
			}
			sdk := filepath.Join(".nvkr/quickstart-sdk", "go"+strings.TrimSpace(string(version))+"."+runtime.GOOS+"-"+runtime.GOARCH)
			sdkExecutable := filepath.Join(root, sdk, "go/bin/go")
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
case "$FAIL_STAGE" in
  install|wrong-install)
    # A synthetic verified-download result exercises the real host tar and mv.
    stage=""
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --volume ]; then
        shift
        case "$1" in *:/download:rw) stage="${1%:/download:rw}" ;; esac
      fi
      shift
    done
    [ -n "$stage" ] || exit 75
    tar -czf "$stage/$SDK_ARCHIVE" -C "$SDK_FIXTURE" go
    exit $? ;;
esac
# Model a failed download/checksum command before extraction or SDK activation.
exit 74
`), 0o700)
			goTool := []byte(`#!/bin/sh
printf 'GOTOOLCHAIN=%s EXEC=%s %s\n' "${GOTOOLCHAIN:-}" "$0" "$*" >> "$GO_LOG"
sdk_go=0
case "$0" in */.nvkr/quickstart-sdk/*/go/bin/go) sdk_go=1 ;; esac
if [ "$1" = env ]; then
  case "$FAIL_STAGE" in
    consent|checksum|incomplete)
      # A switching probe would hide the mismatch and bypass consent.
      if [ "${GOTOOLCHAIN:-}" = local ]; then echo go0.0.0; else printf 'go%s\n' "$PINNED_GO"; fi ;;
    cached|install)
      if [ "$sdk_go" = 1 ]; then printf 'go%s\n' "$PINNED_GO"; else echo go0.0.0; fi ;;
    wrong-cached|wrong-install) echo go0.0.0 ;;
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
`)
			write("tools/go", goTool, 0o700)
			switch tc.sdk {
			case "cached":
				write(filepath.Join(sdk, "go/bin/go"), goTool, 0o700)
			case "incomplete":
				write(filepath.Join(sdk, "keep.txt"), []byte("retained evidence"), 0o600)
			case "install":
				write("download-fixture/go/bin/go", goTool, 0o700)
			}
			write("tools/git", []byte(`#!/bin/sh
if [ "$1" = ls-files ] && [ "$FAIL_STAGE" = git ]; then exit 64; fi
printf 'synthetic-version\n'
`), 0o700)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			args := append([]string{filepath.Join(root, "scripts/nvkr-quickstart.sh")}, tc.args...)
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(),
				"PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAIL_STAGE="+tc.stage, "PINNED_GO="+strings.TrimSpace(string(version)),
				"GO_LOG="+filepath.Join(root, "go.log"), "DOCKER_LOG="+filepath.Join(root, "docker.log"),
				"SDK_FIXTURE="+filepath.Join(root, "download-fixture"),
				"SDK_ARCHIVE=go"+strings.TrimSpace(string(version))+"."+runtime.GOOS+"-"+runtime.GOARCH+".tar.gz",
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
			if tc.wantMessage != "" && !strings.Contains(string(out), tc.wantMessage) {
				t.Fatalf("missing diagnostic %q:\n%s", tc.wantMessage, out)
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
				if tc.sdk == "cached" || tc.sdk == "install" {
					if !strings.Contains(string(goLog), "EXEC="+sdkExecutable+" ") {
						t.Fatalf("checkout SDK was not used:\n%s", goLog)
					}
				}
				for _, line := range strings.Split(strings.TrimSpace(string(goLog)), "\n") {
					if !strings.HasPrefix(line, "GOTOOLCHAIN=local ") {
						t.Fatalf("implicit toolchain switching enabled: %s", goLog)
					}
				}
			}
			installed, err := filepath.Glob(filepath.Join(root, ".nvkr/quickstart-sdk/go*"))
			expectedSDKs := 0
			if tc.sdk != "" {
				expectedSDKs = 1
			}
			if err != nil || len(installed) != expectedSDKs {
				t.Fatalf("unexpected SDK inventory: %v, %v", installed, err)
			}
			if tc.sdk == "incomplete" {
				retained, err := os.ReadFile(filepath.Join(root, sdk, "keep.txt"))
				if err != nil || string(retained) != "retained evidence" {
					t.Fatalf("incomplete SDK evidence changed: %q, %v", retained, err)
				}
			}
			if tc.stage == "consent" {
				if _, err := os.Stat(filepath.Join(root, ".nvkr")); !os.IsNotExist(err) {
					t.Fatalf("consent refusal created workspace: %v", err)
				}
			}
		})
	}
}
