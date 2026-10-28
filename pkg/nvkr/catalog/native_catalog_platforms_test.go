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
	cat, err := LoadContext(t.Context(), Dir("../../../inputs/catalogs/nvke/native-site-arm64"))
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
		{"nvke/native-site-arm64", []string{"nvkectl", "nvke-controller", "cluster-api-provider-provisioned", "nvke-controller-chart", "nktp-chart"}},
		{"capnico/local-arm64", []string{"capnico-controller"}},
		{"controller-profiles/native-nvidia-arm64", []string{"nvke-local-profiles"}},
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
