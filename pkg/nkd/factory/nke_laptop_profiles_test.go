// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Exercise the emitted shell, not a duplicate of its GRUB mutation. This models
// the loader's main-file/drop-in order; the real guest reboot remains acceptance.
func TestLaptopProfilesGRUBDropIn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "definitions", "nke-laptop-arm64", "profiles-catalog", "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Components map[string]struct {
			BuildCommand []string `yaml:"buildCommand"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	command := registry.Components["nke-local-profiles"].BuildCommand
	if len(command) != 3 {
		t.Fatal("unexpected profiles producer")
	}
	_, stanza, found := strings.Cut(command[2], "printf '%s\\n' '#cloud-config' 'bootcmd:'")
	if !found {
		t.Fatal("missing cloud-init producer stanza")
	}
	stanza, _, found = strings.Cut(stanza, "\nsed '1d;98d'")
	if !found {
		t.Fatal("missing source-template append boundary")
	}
	stanza = "printf '%s\\n' '#cloud-config' 'bootcmd:'" + stanza
	for _, scenario := range []string{"new-and-repeat", "different", "symlink", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "bin/profiles-context/profiles/osProfiles/nke-local-26-04-arm64-plain")
			if err := os.MkdirAll(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			run := func(script string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "bash", "-euc", script)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "GRUB_CMDLINE_LINUX=build-time-poison", "NKD_GRUB_FIXTURE="+root,
					"PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"))
				cmd.WaitDelay = time.Second
				return cmd.CombinedOutput()
			}
			if out, err := run(stanza); err != nil {
				t.Fatalf("emit bootcmd: %v\n%s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(destination, "userData.tmpl"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Bootcmd []string `yaml:"bootcmd"`
			}
			if err := yaml.Unmarshal(data, &config); err != nil || len(config.Bootcmd) != 1 {
				t.Fatalf("emitted bootcmd differs: %v", err)
			}
			if err := os.Mkdir(filepath.Join(root, "grub.d"), 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"grub":                  "GRUB_CMDLINE_LINUX=old-main-value\n",
				"grub.d/99-console.cfg": "GRUB_CMDLINE_LINUX=\"console=tty0 console=ttyAMA0,115200\"\n",
				"update-grub":           "#!/bin/sh\nset -eu\nprintf 'invoked' > \"$NKD_GRUB_FIXTURE/invoked\"\n. \"$NKD_GRUB_FIXTURE/grub\"\nfor file in \"$NKD_GRUB_FIXTURE\"/grub.d/*.cfg; do . \"$file\"; done\nprintf '%s\\n' \"$GRUB_CMDLINE_LINUX\" > \"$NKD_GRUB_FIXTURE/observed\"\n",
			}
			for path, value := range files {
				if err := os.WriteFile(filepath.Join(root, path), []byte(value), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			dropIn := filepath.Join(root, "grub.d/zz-nkd-local-network.cfg")
			switch scenario {
			case "different":
				err = os.WriteFile(dropIn, []byte("foreign\n"), 0o600)
			case "symlink":
				err = os.Symlink(filepath.Join(root, "grub"), dropIn)
			case "directory":
				err = os.Mkdir(dropIn, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			projected := strings.ReplaceAll(config.Bootcmd[0], "/etc/default/grub.d", filepath.Join(root, "grub.d"))
			for iteration := range 2 {
				out, err := run(projected)
				if scenario != "new-and-repeat" {
					if err == nil {
						t.Fatal("foreign drop-in accepted")
					}
					if _, err := os.Stat(filepath.Join(root, "invoked")); !os.IsNotExist(err) {
						t.Fatal("refusal reached update-grub")
					}
					break
				}
				if err != nil {
					t.Fatalf("bootcmd iteration %d: %v\n%s", iteration, err, out)
				}
				observed, err := os.ReadFile(filepath.Join(root, "observed"))
				if err != nil || string(observed) != "console=tty0 console=ttyAMA0,115200 net.ifnames=0\n" {
					t.Fatalf("console/naming lost or duplicated: %q, %v", observed, err)
				}
			}
		})
	}
}

// This admits one diagnostic artifact, not image/profile runtime compatibility.
func TestLaptopProfilesArtifactContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	path := filepath.Join("definitions", "nke-laptop-arm64", "definition-profiles.json")
	for _, tc := range []struct {
		name, environment string
		foreign           bool
		wantCode          e.Code
	}{
		{"Docker", "environment-local-docker.json", false, ""},
		{"Podman", "environment-local.json", false, ""},
		{"foreign target", "environment-local-docker.json", true, e.CodeNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := definition.LoadDefinition(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			envPath := filepath.Join(filepath.Dir(path), tc.environment)
			env, err := definition.LoadEnvironment(t.Context(), envPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.foreign {
				doc.Outputs[0].Platform = "linux/amd64"
			}
			plan, err := CompileDocuments(t.Context(), path, doc, envPath, env, "contract-test")
			if tc.wantCode != "" {
				assertFactoryErrorCode(t, err, tc.wantCode)
				if plan != nil {
					t.Fatal("rejected inputs returned a Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 || len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "controller-profiles" || plan.LifecycleOutputs[0].Kind != "oci" {
				t.Fatal("profiles must remain a separate single-output selection")
			}
			fetches, producers := 0, 0
			for _, node := range plan.Builds[0].Graph.Nodes {
				switch node.Kind {
				case ir.KindPull:
					t.Fatal("profiles must not fall back to an acquired fleet image")
				case ir.KindFetch:
					fetches++
					if node.Commit != "8fb2d53368823bd386b4fbd55b4ab6d3c0931345" || node.TreeHash != "36569cf4ec0e2360ee023cd7e53da65b96ce770d" {
						t.Fatal("profiles must use the staged-installer source")
					}
				case ir.KindBuild:
					producers++
					if node.BuilderType != ir.BuilderBuildKit || node.OutputType != "oci-image" || node.WriteDir != "bin" {
						t.Fatalf("unexpected producer: %+v", node)
					}
					command := strings.Join(node.Command, " ")
					for _, required := range []string{
						"sha256sum -c -", "231af133082e6591e55318b1ad23e9a094dc600833bdc81168f1417bcba9e87a",
						"861964b7d4bbc6d4c82c8e77891cf1cd3ceb344666d344708b54e4492f910d09",
						"sed '1d;98d'", "net.ifnames=0", "update-grub", "bootcmd:",
						"/etc/default/grub.d/zz-nkd-local-network.cfg", "${GRUB_CMDLINE_LINUX} net.ifnames=0",
						"set -eu", "test ! -L", "test -f", "= \"$expected\"",
						"FROM alpine@sha256:", "nico-vms-cp.yaml", "nico-vms-worker.yaml",
						"nke-local-26-04-arm64-plain", "ubuntu_version: \"26.04\"", "variant: local-plain-tpm",
						"http://172.30.0.1:18080/nke-node-image-arm64.qcow2", "console=ttyAMA0,115200",
						"--opt platform=linux/arm64", "--opt force-network-mode=none", "nke-local-profiles-arm64.oci.tar",
					} {
						if !strings.Contains(command, required) {
							t.Fatalf("missing bound producer instruction %q", required)
						}
					}
					for _, forbidden := range []string{"luks.key=", "set luks-key", "\nmake ", "\ndocker push", "buildctl-daemonless.sh build --frontend dockerfile.v0 --local context=/src/."} {
						if strings.Contains(command, forbidden) {
							t.Fatalf("unexpected build scope %q", forbidden)
						}
					}
				}
			}
			if fetches != 1 || producers != 1 {
				t.Fatalf("fetches=%d producers=%d", fetches, producers)
			}
			for _, node := range plan.Graph.Nodes {
				if node.Kind == "publish" || node.Kind == "sign" {
					t.Fatal("local profiles do not authorize publication or signing")
				}
			}
		})
	}
}
