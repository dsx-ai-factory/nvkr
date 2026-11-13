// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the authored release vendor/build pair without acquiring sources or
// tools. Acquisition and checksum stubs do not prove upstream byte integrity.
func TestReleaseNKTPRetainsHelmForPackaging(t *testing.T) {
	cat, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/nvke"))
	if err != nil {
		t.Fatal(err)
	}
	component := cat.Components["nktp-chart-release-components"]
	realCopy, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, machine, failure string
		missing                bool
	}{
		{name: "amd64", machine: "x86_64"},
		{name: "arm64", machine: "aarch64"},
		{name: "missing retained Helm", machine: "x86_64", missing: true},
		{name: "dependency failure", machine: "x86_64", failure: "dependency"},
		{name: "packaging failure", machine: "x86_64", failure: "package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, body string, mode os.FileMode) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			const helm = `#!/bin/sh
set -eu
case "$0" in .nvkr-tools/helm|"$(pwd -P)"/.nvkr-tools/helm) ;; *) exit 44 ;; esac
printf '%s %s\n' "$0" "$*" >> "$NVKR_HELM_CALLS"
case "$1" in
  repo) exit 0 ;;
  dependency)
    [ "$NVKR_FAILURE" != dependency ] || exit 42
    [ "$*" = 'dependency build nktp --skip-refresh' ] || exit 45
    mkdir -p nktp/charts
    for name in metrics-server-3.13.1 opentelemetry-kube-stack-0.25.1 prometheus-node-exporter-4.56.1; do
      printf 'synthetic dependency\n' > "nktp/charts/$name.tgz"
    done ;;
  package)
    [ "$NVKR_FAILURE" != package ] || exit 43
    [ "$*" = 'package nktp --destination=charts/' ] || exit 46
    mkdir -p charts
    printf 'synthetic packaged chart\n' > charts/nktp-0.1.0.tgz ;;
  *) exit 47 ;;
esac
`
			write("fixture/helm", helm, 0o700)
			write("nktp/Chart.yaml", "synthetic chart\n", 0o600)
			write("nktp/Chart.lock", "synthetic lock\n", 0o600)
			for name, body := range map[string]string{
				"uname":   "printf '%s\\n' " + tc.machine + "\n",
				"curl":    "exit 0\n",
				"tar":     "exit 0\n",
				"timeout": "shift\nshift\nexec \"$@\"\n",
				"cp": `case "$1" in /tmp/linux-amd64/helm|/tmp/linux-arm64/helm) ;; *) exit 48 ;; esac
exec "$NVKR_REAL_COPY" "$NVKR_HELM_FIXTURE" "$2"
`,
				"sha256sum": `if [ "$1" = -c ]; then
  cat > /dev/null
else
  for file in "$@"; do
    test -f "$file"
    printf 'synthetic-checksum  %s\n' "$file"
  done
fi
`,
				// An ambient Helm would succeed, so refusal cannot be attributed
				// merely to a missing command on PATH.
				"helm": "touch \"$NVKR_AMBIENT_HELM\"\nexit 0\n",
			} {
				write("bin/"+name, "#!/bin/sh\nset -eu\n"+body, 0o700)
			}
			run := func(command []string) (string, error) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, command[0], command[1:]...)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
					"NVKR_REAL_COPY="+realCopy, "NVKR_HELM_FIXTURE="+filepath.Join(root, "fixture/helm"),
					"NVKR_HELM_CALLS="+filepath.Join(root, "calls"), "NVKR_AMBIENT_HELM="+filepath.Join(root, "ambient"), "NVKR_FAILURE="+tc.failure)
				cmd.WaitDelay = time.Second
				out, err := cmd.CombinedOutput()
				return string(out), err
			}
			out, vendorErr := run(component.VendorCommand)
			if tc.failure == "dependency" {
				if exit, ok := vendorErr.(*exec.ExitError); !ok || exit.ExitCode() != 42 {
					t.Fatalf("dependency failure did not propagate: %v: %s", vendorErr, out)
				}
				return
			}
			if vendorErr != nil {
				t.Fatalf("vendor: %v: %s", vendorErr, out)
			}
			retained := filepath.Join(root, ".nvkr-tools/helm")
			raw, err := os.ReadFile(retained)
			if err != nil || string(raw) != helm {
				t.Fatalf("vendor did not retain the acquired Helm bytes: %v", err)
			}
			if tc.missing {
				if err := os.Rename(retained, filepath.Join(root, "withheld-helm")); err != nil {
					t.Fatal(err)
				}
			}
			out, buildErr := run(component.BuildCommand)
			if tc.missing || tc.failure == "package" {
				// Shells differ in the status for an absent explicit path. An
				// invoked Helm's own failure code must propagate unchanged.
				if exit, ok := buildErr.(*exec.ExitError); !ok || (tc.failure == "package" && exit.ExitCode() != 43) {
					t.Fatalf("packaging failure did not propagate: %v: %s", buildErr, out)
				}
				if _, err := os.Stat(filepath.Join(root, component.ArtifactPath)); !os.IsNotExist(err) {
					t.Fatal("failed packaging emitted an artifact", err)
				}
			} else {
				if buildErr != nil {
					t.Fatalf("package: %v: %s", buildErr, out)
				}
				if _, err := os.Stat(filepath.Join(root, component.ArtifactPath)); err != nil {
					t.Fatal(err)
				}
				calls, err := os.ReadFile(filepath.Join(root, "calls"))
				if err != nil || !strings.Contains(string(calls), ".nvkr-tools/helm package nktp --destination=charts/\n") {
					t.Fatal("packaging did not execute the retained path", err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "ambient")); !os.IsNotExist(err) {
				t.Fatal("packaging fell back to ambient Helm", err)
			}
		})
	}
}
