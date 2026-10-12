// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the real remote shell vector with a controlled VM entrypoint. This
// tests SSH-boundary quoting, not Colima availability or source authorization.
func TestColimaPreservesInvocationRedirects(t *testing.T) {
	for _, row := range []struct {
		name              string
		args              []string
		count, key, value string
		fail              bool
	}{
		{"check", []string{"--check", "providers"}, "1", "url./fixture/source path.insteadOf", "https://example.invalid/source.git", false},
		{"execution with shell metacharacters", []string{"fresh", "2", "providers"}, "1", "url./fixture/source.insteadOf", "/fixture/path with spaces; $(exit 91)", false},
		{"empty vector", []string{"--check", "tools"}, "0", "", "", false},
		{"missing key", []string{"--check", "tools"}, "1", "", "/fixture/source", true},
		{"missing value", []string{"fresh", "2", "providers"}, "1", "url./fixture/source.insteadOf", "", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			tools, vm := filepath.Join(root, "tools"), filepath.Join(root, "vm")
			for _, dir := range []string{tools, vm, filepath.Join(root, "scripts")} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(name, content string) {
				t.Helper()
				if err := os.WriteFile(name, []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			body, err := os.ReadFile("nvke-full-arm64-local.sh")
			if err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(root, "scripts", "nvke-full-arm64-local.sh")
			write(launcher, string(body))
			dirname, err := exec.LookPath("dirname")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dirname, filepath.Join(tools, "dirname")); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(tools, "uname"), "#!/bin/sh\nif [ \"$1\" = -s ]; then echo Darwin; else echo arm64; fi\n")
			write(filepath.Join(tools, "colima"), `#!/bin/sh
if [ "$1" = status ]; then exit 0; fi
[ "$1:$5:$6" = ssh:bash:-c ] || exit 99
export PATH="$TEST_VM_TOOLS"
exec /bin/bash -c "$7"
`)
			write(filepath.Join(vm, "env"), "#!/bin/sh\nexec /usr/bin/env \"$@\"\n")
			write(filepath.Join(vm, "bash"), `#!/bin/sh
printf '%s\000' "$GIT_CONFIG_COUNT" "${GIT_CONFIG_KEY_0-}" "${GIT_CONFIG_VALUE_0-}" "${DOCKER_HOST-unset}" "${DOCKER_CONTEXT-unset}" "$@"
`)
			cmd := exec.CommandContext(t.Context(), "bash", append([]string{launcher}, row.args...)...)
			cmd.Env = append(os.Environ(), "PATH="+tools, "COLIMA_PROFILE=fixture", "CONTAINER_RUNTIME=docker",
				"NVKE_LOCAL_CLONE=", "DOCKER_HOST=must-not-forward", "DOCKER_CONTEXT=must-not-forward",
				"TEST_VM_TOOLS="+vm, "GIT_CONFIG_COUNT="+row.count,
				"GIT_CONFIG_KEY_0="+row.key, "GIT_CONFIG_VALUE_0="+row.value)
			output, err := cmd.CombinedOutput()
			if row.fail {
				if err == nil || !strings.Contains(string(output), "nonempty key and value") {
					t.Fatalf("error=%v output=%q", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("remote vector: %v: %s", err, output)
			}
			want := append([]string{row.count, row.key, row.value, "unset", "unset", "scripts/nvke-full-arm64-local.sh"}, row.args...)
			if string(output) != strings.Join(want, "\x00")+"\x00" {
				t.Fatalf("got %q, want %q", output, want)
			}
		})
	}
}
