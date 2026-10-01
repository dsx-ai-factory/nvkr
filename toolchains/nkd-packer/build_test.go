// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package packer_test

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

func TestToolchainDockerExportAndLoad(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("toolchain script requires jq")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("toolchain supports amd64 and arm64 hosts")
	}
	for _, tc := range []struct {
		name      string
		buildExit string
		loadExit  string
		wantError bool
	}{
		{name: "archive loaded before validation", buildExit: "0", loadExit: "0"},
		{name: "export failure stops validation", buildExit: "42", loadExit: "0", wantError: true},
		{name: "load failure stops validation", buildExit: "0", loadExit: "43", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			fake := filepath.Join(dir, "docker")
			const script = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${TEST_CALLS}"
case "$1 $2" in
  "buildx version"|"version --format") exit 0 ;;
  "buildx build")
    [[ " $* " == *" --output type=docker,dest=-,rewrite-timestamp=true "* ]]
    [[ " $* " == *" --build-arg SOURCE_DATE_EPOCH=0 "* ]]
    printf 'test-image-archive\n'
    exit "${TEST_BUILD_EXIT}"
    ;;
  "image load")
    test "$(cat)" = test-image-archive
    exit "${TEST_LOAD_EXIT}"
    ;;
  "image inspect")
    if [[ "$3" == --format ]]; then
      printf 'sha256:%064d\n' 1
    else
      printf '[{"Os":"linux","Architecture":"%s","Config":{"Env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]}}]\n' "${TEST_ARCH}"
    fi
    ;;
  "run --rm") exit 0 ;;
  *) echo "unexpected runtime command: $*" >&2; exit 99 ;;
esac
`
			if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "build.sh", "--arch", runtime.GOARCH)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_BUILD_EXIT="+tc.buildExit, "TEST_LOAD_EXIT="+tc.loadExit, "TEST_ARCH="+runtime.GOARCH)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("build error = %v, want error %v:\n%s", err, tc.wantError, output)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "image load\n") {
				t.Fatalf("archive was not explicitly loaded:\n%s", calls)
			}
			validated := strings.Contains(string(calls), "image inspect ")
			if validated == tc.wantError {
				t.Fatalf("validation reached = %v, want %v:\n%s", validated, !tc.wantError, calls)
			}
		})
	}
}
