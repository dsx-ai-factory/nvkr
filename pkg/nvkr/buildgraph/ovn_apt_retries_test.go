// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

func TestOVNAPTRetryPatch(t *testing.T) {
	var patch string
	for _, path := range []string{"../../../inputs/catalogs/node-images", "../../../inputs/catalogs/ovn/nvidia"} {
		cat, err := catalog.LoadContext(t.Context(), catalog.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		patches := cat.Components["ovn-kube-ubuntu"].Patches
		if len(patches) != 1 || patches[0].Name != "patches/ovn-apt-retries.patch" {
			t.Fatalf("%s lacks its authenticated APT retry patch", path)
		}
		if patch != "" && patch != patches[0].Content {
			t.Fatal("OVN producers differ in retry policy")
		}
		patch = patches[0].Content
	}
	if strings.Count(patch, "+    --mount=type=bind,source=dist/images/nvkr-apt-get,target=/usr/local/bin/apt-get") != 1 ||
		strings.Count(patch, "+RUN --mount=type=bind,source=dist/images/nvkr-apt-get,target=/usr/local/bin/apt-get") != 1 ||
		!strings.Contains(patch, "new file mode 100755") {
		t.Fatal("retry wrapper must be executable and mounted in both APT build steps")
	}
	_, added, ok := strings.Cut(patch, "@@ -0,0 +1,25 @@\n")
	if !ok {
		t.Fatal("missing retry wrapper")
	}
	var body strings.Builder
	for line := range strings.SplitSeq(strings.TrimSuffix(added, "\n"), "\n") {
		if !strings.HasPrefix(line, "+") {
			t.Fatal("unexpected wrapper patch line")
		}
		body.WriteString(strings.TrimPrefix(line, "+") + "\n")
	}
	for _, test := range []struct {
		name, failures string
		calls          int
		status         int
	}{
		{"first success", "0", 1, 0},
		{"HTTP 520 then success", "1", 2, 0},
		{"permanent signature failure", "3", 3, 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			apt := filepath.Join(dir, "apt-get")
			fake := "#!/bin/sh\nset -eu\nn=0\nif [ -f \"$TEST_STATE\" ]; then n=$(cat \"$TEST_STATE\"); fi\nn=$((n + 1))\nprintf '%s' \"$n\" > \"$TEST_STATE\"\nprintf '%s\\n' \"$@\" >> \"$TEST_ARGS\"\nif [ \"$n\" -le \"$TEST_FAILURES\" ]; then echo 'APT fetch or authentication failed' >&2; exit 100; fi\n"
			if err := os.WriteFile(apt, []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$TEST_DELAYS\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(body.String(), "/usr/bin/apt-get", apt)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-c", script, "apt-get", "install", "-y", "pinned-package=1.2.3")
			command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"),
				"TEST_STATE="+filepath.Join(dir, "state"), "TEST_ARGS="+filepath.Join(dir, "args"),
				"TEST_DELAYS="+filepath.Join(dir, "delays"), "TEST_FAILURES="+test.failures)
			output, err := command.CombinedOutput()
			if test.status == 0 {
				if err != nil {
					t.Fatalf("wrapper: %v: %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != test.status {
					t.Fatalf("wrapper: %v: %s", err, output)
				}
			}
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			want := "-o\nAcquire::Retries=3\n-o\nAcquire::http::Timeout=30\n-o\nAcquire::https::Timeout=30\n-o\nAPT::Update::Error-Mode=any\ninstall\n-y\npinned-package=1.2.3\n"
			if string(args) != strings.Repeat(want, test.calls) {
				t.Fatalf("arguments or attempt bound changed: %s", args)
			}
			delays, err := os.ReadFile(filepath.Join(dir, "delays"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			wantDelays := []string{"", "5\n", "5\n10\n"}[test.calls-1]
			if string(delays) != wantDelays {
				t.Fatalf("delays = %q, want %q", delays, wantDelays)
			}
		})
	}
}
