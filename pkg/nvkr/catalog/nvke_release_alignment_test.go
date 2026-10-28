// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNVKEReleasePinsStayAligned(t *testing.T) {
	for _, directory := range []string{
		"nvke/release", "nvke/release-components", "nvke/module-tags", "nvke/module-cli-arm64",
		"nvke/local-site-arm64", "nvke/native-site-arm64", "nvke/nightly-site-arm64",
		"node-images/release", "node-images/diagnostic",
		"controller-profiles/nvidia-arm64", "controller-profiles/upstream-arm64", "controller-profiles/native-nvidia-arm64",
		"nicosim", "tools/native", "runtime-dependencies",
	} {
		t.Run(directory, func(t *testing.T) {
			root := filepath.Join("../../../inputs/catalogs", directory)
			registry, err := readRegistry(Dir(root))
			if err != nil {
				t.Fatal(err)
			}
			lock, err := LoadLockContext(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			for name, source := range registry.Sources {
				if strings.TrimSuffix(source.Repo, ".git") != "https://github.com/dsx-ai-factory/nvke" {
					continue
				}
				pin := lock.Pins[name]
				if source.Constraint != "v0.22.0" || source.TagPrefix != "" || pin.Tag != "v0.22.0" || pin.Commit != "400c24fb6465b042be883d6b62472528520d6291" || pin.TreeHash != "f3afc02f5c92f4aacda3a1655836a91bef093bfe" || pin.SourceDateEpoch != 1792548398 {
					t.Fatalf("%s must select the complete NVKE v0.22.0 identity: %+v", name, pin)
				}
				for component, producer := range registry.Components {
					if producer.Source == name && producer.Prebuilt != nil && producer.Prebuilt.Image != "" && lock.Artifacts[component].Tag != pin.Tag {
						t.Fatalf("%s artifact selection must follow source %s at %s", component, name, pin.Tag)
					}
				}
			}
		})
	}
}

func TestNVKEOverlayPinsStayAligned(t *testing.T) {
	paths, err := filepath.Glob("../../../inputs/catalogs/node-images/variants/*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("enumerate overlays: %v", err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			overlay, err := LoadOverlayContext(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			for name, replacement := range overlay.Components {
				c := replacement.Component
				if c == nil || strings.TrimSuffix(c.Repo, ".git") != "https://github.com/dsx-ai-factory/nvke" {
					continue
				}
				if c.Tag != "v0.22.0" || c.Commit != "400c24fb6465b042be883d6b62472528520d6291" || c.TreeHash != "f3afc02f5c92f4aacda3a1655836a91bef093bfe" || c.SourceDate != 1792548398 {
					t.Fatalf("%s replaces the v0.22.0 source identity", name)
				}
			}
		})
	}
}

func TestNVKERetainedCompilerRejectsBadAcquisition(t *testing.T) {
	for _, directory := range []string{"ubuntu24"} {
		for _, arch := range []string{"x86_64", "unsupported"} {
			t.Run(directory+"/"+arch, func(t *testing.T) {
				overlay, err := LoadOverlayContext(t.Context(), "../../../inputs/catalogs/node-images/variants/ubuntu24.json")
				if err != nil {
					t.Fatal(err)
				}
				command := overlay.Components["nvke-agent"].Component.VendorCommand
				_, compiler, found := strings.Cut(command[2], "# Retain the source-required compiler;")
				if !found {
					t.Fatal("missing retained compiler")
				}
				_, compiler, _ = strings.Cut(compiler, "\n")
				command[2] = compiler
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				if err := os.Mkdir(bin, 0700); err != nil {
					t.Fatal(err)
				}
				for name, body := range map[string]string{
					"uname": "#!/bin/sh\necho " + arch + "\n",
					"curl":  "#!/bin/sh\nprintf corrupt > .nvkr-tools/go.tar.gz\n",
					"tar":   "#!/bin/sh\ntouch extracted\nexit 99\n",
				} {
					if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.CommandContext(t.Context(), command[0], command[1:]...)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("invalid compiler acquisition succeeded: %s", output)
				}
				if _, err := os.Stat(filepath.Join(root, "extracted")); !os.IsNotExist(err) {
					t.Fatal("extracted compiler before verifying its checksum")
				}
				if arch == "unsupported" && !strings.Contains(string(output), "Unsupported Go execution architecture") {
					t.Fatalf("unexpected failure: %s", output)
				}
				if arch == "x86_64" && !strings.Contains(string(output), "did NOT match") {
					t.Fatalf("checksum was not checked: %s", output)
				}
			})
		}
	}
}
