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
)

// Exercise the declared fetch command against a local Git repository. This
// covers the missing chart in current NVKE without requiring a hosted build.
func TestNightlyInstallerVendorsPinnedNKTP(t *testing.T) {
	tools, err := LoadOverlayContext(t.Context(), "../../../inputs/catalogs/tools/variants/nightly-go.json")
	if err != nil {
		t.Fatal(err)
	}
	site, err := LoadOverlayContext(t.Context(), "../../../inputs/catalogs/nvke/variants/nightly-go.json")
	if err != nil {
		t.Fatal(err)
	}
	installer := tools.Components["nvke-installer-sources"].Component
	nktp := site.Components["nktp-chart"].Component
	if nktp == nil || installer == nil || len(installer.VendorCommand) != 3 {
		t.Fatal("nightly installer cannot supply the chart removed from NVKE: missing independent NKTP pin or vendor command")
	}
	if nktp.Repo != "https://github.com/dsx-ai-factory/nvke-telemetry-pipeline.git" || nktp.Tag != "nktp/v0.2.0" || nktp.ArtifactPath != "charts/nktp-0.2.0.tgz" {
		t.Fatal("nightly chart must select the release required by NVKE's deployment")
	}
	for _, pin := range []string{nktp.Repo, nktp.Commit, nktp.TreeHash} {
		if pin == "" || !strings.Contains(installer.VendorCommand[2], pin) {
			t.Fatal("installer vendor source differs from the independently pinned chart producer")
		}
	}
	for _, scenario := range []string{"current", "in-tree", "unexpected version", "unexpected repository", "duplicate chart", "missing release", "wrong tree", "wrong chart version", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			source := t.TempDir()
			write := func(root, name, text string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			chartVersion := "0.2.0"
			if scenario == "wrong chart version" {
				chartVersion = "0.3.0"
			}
			write(source, "nktp/Chart.yaml", "apiVersion: v2\nname: nktp\nversion: "+chartVersion+"\n")
			write(source, "nvcb/chart/Chart.yaml", "apiVersion: v2\nname: nvcb\nversion: 0.1.0\n")
			git := func(args ...string) string {
				t.Helper()
				command := exec.CommandContext(t.Context(), "git", append([]string{"-C", source}, args...)...)
				out, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("git fixture: %v: %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "--quiet")
			git("add", ".")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
			commit, tree := git("rev-parse", "HEAD"), git("rev-parse", "HEAD^{tree}")
			if scenario == "wrong tree" {
				tree = strings.Repeat("a", 40)
			}
			root := t.TempDir()
			release := "chart:\n  name: nktp\n  version: \"0.2.0\"\n  repository: https://helm.ngc.nvidia.com/nvidia\n"
			switch scenario {
			case "unexpected version":
				release = strings.ReplaceAll(release, "0.2.0", "0.3.0")
			case "unexpected repository":
				release = strings.ReplaceAll(release, "helm.ngc.nvidia.com", "example.invalid")
			case "duplicate chart":
				release += "chart:\n  name: unreviewed\n"
			case "in-tree":
				release = "chart:\n  name: nktp\n  path: ../../nktp/deploy/charts/nktp\n"
				write(root, "nktp/deploy/charts/nktp/Chart.yaml", "original chart\n")
			}
			if scenario != "missing release" {
				write(root, "deploy/nico/nktp/release.yaml", release)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			command := append([]string(nil), installer.VendorCommand...)
			command[2] = strings.NewReplacer(nktp.Commit, commit, nktp.TreeHash, tree).Replace(command[2])
			child := exec.CommandContext(ctx, command[0], command[1:]...)
			child.Dir = root
			trace := filepath.Join(root, "git-trace")
			child.Env = append(os.Environ(), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=url."+source+".insteadOf", "GIT_CONFIG_VALUE_0="+nktp.Repo,
				"GIT_CONFIG_KEY_1=protocol.file.allow", "GIT_CONFIG_VALUE_1=always", "GIT_TRACE="+trace)
			out, err := child.CombinedOutput()
			wantOK := scenario == "current" || scenario == "in-tree"
			if (err == nil) != wantOK {
				t.Fatalf("vendor command: %v: %s", err, out)
			}
			chart := filepath.Join(root, "nktp/deploy/charts/nktp/Chart.yaml")
			if !wantOK {
				if _, err := os.Stat(chart); !os.IsNotExist(err) {
					t.Fatal("failed vendoring retained an installer chart")
				}
				return
			}
			raw, err := os.ReadFile(chart)
			if err != nil {
				t.Fatal("installer still lacks the chart at its archived path", err)
			}
			if scenario == "in-tree" {
				if string(raw) != "original chart\n" {
					t.Fatal("released NVKE's in-tree chart was changed")
				}
				if _, err := os.Stat(trace); !os.IsNotExist(err) {
					t.Fatal("legacy source unexpectedly fetched an external chart")
				}
			} else if _, err := os.Stat(filepath.Join(root, "nktp/deploy/charts/nvcb/chart/Chart.yaml")); err != nil {
				t.Fatal("NKTP's source-owned NVCB dependency was omitted", err)
			}
			raw, err = os.ReadFile(filepath.Join(root, "deploy/nico/nktp/release.yaml"))
			if err != nil || string(raw) != release {
				t.Fatal("vendoring changed NVKE's deployment declaration")
			}
		})
	}
}
