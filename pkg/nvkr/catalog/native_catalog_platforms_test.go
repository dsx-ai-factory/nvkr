// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNativeNKTPDependencyArchiveChecks(t *testing.T) {
	cat, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/nvke/native-site"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "set -- nktp/deploy/charts/nktp/charts/*.tgz"
	_, checks, found := strings.Cut(cat.Components["nktp-chart"].VendorCommand[2], marker)
	if !found {
		t.Fatal("native chart source omits its archive admission checks")
	}
	for _, tc := range []struct {
		name, version, digest         string
		extra, badChecksum, wantError bool
	}{
		{name: "tagged release", version: "0.22.0", digest: "df7d63a331d9d164a276f5b4395c6383c00fe801abcc6b2ea969de182914dc32"},
		{name: "reviewed main", version: "0.25.1", digest: "e3460a7572a2b9c98ff421a29501dc26cdd09d83859f0556f6c3f5443793afda"},
		{name: "unknown version", version: "0.25.2", wantError: true},
		{name: "extra archive", version: "0.25.1", extra: true, wantError: true},
		{name: "release checksum failure", version: "0.22.0", badChecksum: true, wantError: true},
		{name: "main checksum failure", version: "0.25.1", badChecksum: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const charts = "nktp/deploy/charts/nktp/charts/"
			if err := os.MkdirAll(filepath.Join(root, charts), 0o700); err != nil {
				t.Fatal(err)
			}
			archives := []string{"metrics-server-3.13.1.tgz", "prometheus-node-exporter-4.56.1.tgz", "opentelemetry-kube-stack-" + tc.version + ".tgz"}
			if tc.extra {
				archives = append(archives, "unreviewed.tgz")
			}
			for _, archive := range archives {
				if err := os.WriteFile(filepath.Join(root, charts, archive), []byte("synthetic archive"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			// Capture the actual checksum-owner request; synthetic archives do not
			// establish upstream artifact integrity. A checksum failure must propagate.
			stub := "#!/bin/sh\nwhile read -r digest file; do\n [ -f \"$file\" ] || exit 1\n printf '%s  %s\\n' \"$digest\" \"$file\" >> \"$NVKR_CHECK_REQUEST\"\ndone\n[ \"$NVKR_BAD_CHECKSUM\" != true ]\n"
			if err := os.WriteFile(filepath.Join(bin, "sha256sum"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			request := filepath.Join(root, "checksums")
			command := exec.CommandContext(t.Context(), "sh", "-euc", marker+checks)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "NVKR_CHECK_REQUEST="+request)
			if tc.badChecksum {
				command.Env = append(command.Env, "NVKR_BAD_CHECKSUM=true")
			}
			output, err := command.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("archive checks: %v: %s", err, output)
			}
			if tc.wantError {
				return
			}
			raw, err := os.ReadFile(request)
			if err != nil {
				t.Fatal(err)
			}
			want := "084e6edb680cf4e2acc30bd496568c53fdf663cbacf6e17876b25785c35b7a13  " + charts + archives[0] + "\n" + tc.digest + "  " + charts + archives[2] + "\n" + "70fab291e00a34c58fd943bb075fecc2e2e73fa95db1bcbbe7cdfe008795354a  " + charts + archives[1] + "\n"
			if string(raw) != want {
				t.Fatalf("checksum requests = %q, want %q", raw, want)
			}
		})
	}
}

func TestNativeCatalogsShareLinuxArchitectures(t *testing.T) {
	for _, tc := range []struct {
		directory  string
		components []string
	}{
		{"nvke/native-site", []string{"nvkectl", "nvke-controller", "cluster-api-provider-provisioned", "nvke-controller-chart", "nktp-chart"}},
		{"capnico/local", []string{"capnico-controller"}},
		{"controller-profiles/native-nvidia", []string{"nvke-local-profiles"}},
		{"tools/native", []string{"nerdctl", "nico-sim", "nico-simctl", "nvke-installer-sources", "nvkectl", "nvkr-site-identity-helper", "native-capi-exec-init-image", "native-capi-exec-init-image-main", "native-capi-exec-plugin-image", "native-workload-image"}},
	} {
		t.Run(tc.directory, func(t *testing.T) {
			cat, err := LoadContext(t.Context(), Dir(filepath.Join("../../../inputs/catalogs", tc.directory)))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.components {
				component, ok := cat.Components[name]
				if !ok {
					t.Fatalf("missing shared producer %s", name)
				}
				for _, arch := range []string{"amd64", "arm64"} {
					if !slices.Contains(component.Platforms, "linux/"+arch) {
						t.Fatalf("%s omits linux/%s", name, arch)
					}
					if component.FromSource && component.ToolchainImage == "" && component.ToolchainImages[arch] == "" {
						t.Fatalf("%s omits the %s compiler", name, arch)
					}
					if component.Prebuilt != nil && component.Prebuilt.Digests[arch] == "" {
						t.Fatalf("%s omits %s acquired bytes", name, arch)
					}
				}
			}
		})
	}
}

func TestNativeAMD64AcquisitionsKeepReleaseAndPlatform(t *testing.T) {
	cat, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/tools/native"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"clusterctl", "helm", "kind", "kubectl", "oras"} {
		t.Run(tool, func(t *testing.T) {
			arm, amd := cat.Components[tool+"-linux"], cat.Components[tool+"-linux-amd64"]
			if arm.Tag != amd.Tag || arm.Commit != amd.Commit || !slices.Equal(amd.Platforms, []string{"linux/amd64"}) {
				t.Fatal("native architecture selection changed the acquired release")
			}
			if amd.Prebuilt == nil || amd.Prebuilt.Executable == nil || amd.Prebuilt.Executable.Platform != "linux/amd64" || !strings.Contains(amd.Prebuilt.Executable.URL, "amd64") {
				t.Fatal("AMD64 acquisition lacks exact native executable metadata")
			}
			if amd.Prebuilt.Executable.ExecutableDigest == arm.Prebuilt.Executable.ExecutableDigest {
				t.Fatal("different architecture executables share an unexpected digest")
			}
		})
	}
	legacy, current := cat.Components["native-capi-exec-init-image"], cat.Components["native-capi-exec-init-image-main"]
	if legacy.Prebuilt.ImageParentDigest != "sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6" || current.Prebuilt.ImageParentDigest != "sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a" {
		t.Fatal("reviewed CAPI init parents changed")
	}
}

func TestNativeHelmVendorPinsMatchTheSharedTools(t *testing.T) {
	site, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/nvke/native-site"))
	if err != nil {
		t.Fatal(err)
	}
	tools, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/tools/native"))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"nvke-controller-chart", "nktp-chart"} {
		t.Run(component, func(t *testing.T) {
			command := site.Components[component].VendorCommand
			if len(command) != 3 {
				t.Fatal("expected the source-owned vendor script")
			}
			for _, tool := range []string{"helm-linux", "helm-linux-amd64"} {
				selected := tools.Components[tool]
				if selected.Prebuilt == nil || selected.Prebuilt.Executable == nil {
					t.Fatal("shared Helm acquisition is absent")
				}
				pin := selected.Prebuilt.Executable
				_, arch, _ := strings.Cut(selected.Platforms[0], "/")
				function, _, found := strings.Cut(command[2], `case "$(uname -m)" in`)
				if !found {
					t.Fatal("vendor Helm digest function is absent")
				}
				probe := strings.ReplaceAll(function, "{{.Arch}}", arch) + `helm_digest "$1"`
				out, err := exec.CommandContext(t.Context(), "sh", "-eu", "-c", probe, "helm-digest-test", arch).CombinedOutput()
				if err != nil || string(out) != strings.TrimPrefix(pin.ArchiveDigest, "sha256:") || !strings.Contains(command[2], "helm-"+selected.Tag+"-linux-") {
					t.Fatalf("vendor Helm digest for %s diverged from the shared tool pin: %s %v", tool, out, err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "sh", "-n", "-c", strings.ReplaceAll(command[2], "{{.Arch}}", "amd64"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("vendor shell syntax: %s %v", out, err)
			}
		})
	}
}

func TestNativeHelmVendorsExecuteHostFromWorkspace(t *testing.T) {
	site, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/nvke/native-site"))
	if err != nil {
		t.Fatal(err)
	}
	realCopy, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"nvke-controller-chart", "nktp-chart"} {
		for _, tc := range []struct{ name, host, target, failure string }{
			{"native amd64", "amd64", "amd64", ""},
			{"native arm64", "arm64", "arm64", ""},
			{"amd64 host arm64 target", "amd64", "arm64", ""},
			{"arm64 host amd64 target", "arm64", "amd64", ""},
			{"dependency failure cleans host", "arm64", "amd64", "helm"},
			{"archive rejection precedes execution", "arm64", "arm64", "checksum"},
		} {
			t.Run(component+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				bin, fixtures := filepath.Join(root, "bin"), filepath.Join(root, "fixtures")
				write := func(name, body string, mode os.FileMode) {
					t.Helper()
					if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, []byte(body), mode); err != nil {
						t.Fatal(err)
					}
				}
				const chart = "synthetic owning chart\n"
				for _, name := range []string{"controller/deploy/charts/controller/Chart.yaml", "nktp/deploy/charts/nktp/Chart.yaml", "nktp/deploy/charts/nktp/Chart.lock"} {
					write(filepath.Join(root, name), chart, 0o600)
				}
				for _, arch := range []string{"amd64", "arm64"} {
					// This executable models only Helm's invocation and output files.
					// Archive digests below are request stubs, not artifact integrity proof.
					helper := `#!/bin/sh
set -eu
workspace=$(pwd -P)
case "$0" in .nvkr-helm-host.*/helm|"$workspace"/.nvkr-helm-host.*/helm) ;; *) exit 44 ;; esac
[ "$NVKR_HOST_ARCH" = @ARCH@ ] || exit 45
printf '%s %s\n' "$0" "$*" >> "$NVKR_HELM_CALLS"
[ "$NVKR_FAILURE" != helm ] || exit 42
if [ "$1" = dependency ] && [ "$3" = nktp/deploy/charts/nktp ]; then
  mkdir -p nktp/deploy/charts/nktp/charts
  for archive in metrics-server-3.13.1 prometheus-node-exporter-4.56.1 opentelemetry-kube-stack-0.25.1; do
    printf 'synthetic chart\n' > "nktp/deploy/charts/nktp/charts/$archive.tgz"
  done
fi
`
					write(filepath.Join(fixtures, "linux-"+arch, "helm"), strings.ReplaceAll(helper, "@ARCH@", arch), 0o700)
				}
				machine := "aarch64"
				if tc.host == "amd64" {
					machine = "x86_64"
				}
				stubs := map[string]string{
					"uname":   "printf '%s\\n' " + machine + "\n",
					"curl":    "exit 0\n",
					"tar":     "exit 0\n",
					"timeout": "shift\nshift\nexec \"$@\"\n",
					"cp": `case "$1" in /tmp/linux-*/helm) source="$NVKR_HELM_FIXTURES/${1#/tmp/}" ;; *) exit 46 ;; esac
exec "$NVKR_REAL_COPY" "$source" "$2"
`,
					"sha256sum": `if [ "$1" = -c ]; then
  request=$(cat)
  printf '%s\n' "$request" >> "$NVKR_CHECK_REQUESTS"
  [ "$NVKR_FAILURE" != checksum ] || exit 43
else
  for file in "$@"; do printf 'synthetic-pin  %s\n' "$file"; done
fi
`,
				}
				for name, body := range stubs {
					write(filepath.Join(bin, name), "#!/bin/sh\nset -eu\n"+body, 0o700)
				}
				calls := filepath.Join(root, "helm-calls")
				command := site.Components[component].VendorCommand
				child := exec.CommandContext(t.Context(), command[0], command[1], strings.ReplaceAll(command[2], "{{.Arch}}", tc.target))
				child.Dir = root
				child.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
					"NVKR_HOST_ARCH="+tc.host, "NVKR_FAILURE="+tc.failure, "NVKR_HELM_FIXTURES="+fixtures,
					"NVKR_REAL_COPY="+realCopy, "NVKR_HELM_CALLS="+calls, "NVKR_CHECK_REQUESTS="+filepath.Join(root, "checks"))
				output, vendorErr := child.CombinedOutput()
				if (vendorErr != nil) != (tc.failure != "") {
					t.Fatalf("vendor execution: %v: %s", vendorErr, output)
				}
				leftovers, err := filepath.Glob(filepath.Join(root, ".nvkr-helm-host.*"))
				if err != nil || len(leftovers) != 0 {
					t.Fatalf("temporary host tools survived vendor completion: %v %v", leftovers, err)
				}
				if tc.failure != "" {
					wantExit := 42
					if tc.failure == "checksum" {
						wantExit = 43
					}
					if exit, ok := vendorErr.(*exec.ExitError); !ok || exit.ExitCode() != wantExit {
						t.Fatalf("vendor did not propagate the selected failure: %v: %s", vendorErr, output)
					}
					if _, err := os.Stat(filepath.Join(root, ".nvkr-tools", "helm")); !os.IsNotExist(err) {
						t.Fatal("failed vendoring captured target Helm", err)
					}
					if tc.failure == "checksum" {
						if _, err := os.Stat(calls); !os.IsNotExist(err) {
							t.Fatal("Helm executed before archive checksum admission", err)
						}
					}
					return
				}
				if raw, err := os.ReadFile(calls); err != nil || len(raw) == 0 {
					t.Fatal("host Helm did not execute from the source workspace", err)
				}
				got, err := os.ReadFile(filepath.Join(root, ".nvkr-tools", "helm"))
				if err != nil {
					t.Fatal(err)
				}
				want, err := os.ReadFile(filepath.Join(fixtures, "linux-"+tc.target, "helm"))
				if err != nil || string(got) != string(want) {
					t.Fatal("captured Helm differs from the selected target bytes", err)
				}
				members, err := os.ReadDir(filepath.Join(root, ".nvkr-tools"))
				if err != nil || len(members) != 1 || members[0].Name() != "helm" {
					t.Fatal("captured tools contain temporary host artifacts", err)
				}
			})
		}
	}
}
