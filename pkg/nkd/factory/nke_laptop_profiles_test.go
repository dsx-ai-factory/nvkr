// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
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

// Exercise the emitted shell, not a duplicate of its configuration mutation.
// The GRUB loader is modeled; udev naming and a real reboot remain acceptance.
func TestLaptopProfilesGRUBDropIn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "definitions", "nke-local-dev-arm64", "profiles-catalog", "registry.yaml"))
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
	stanza, _, found = strings.Cut(stanza, "\nsed '1d;98d;")
	if !found {
		t.Fatal("missing source-template append boundary")
	}
	stanza = "printf '%s\\n' '#cloud-config' 'bootcmd:'" + stanza
	for _, scenario := range []string{"new-and-repeat", "different", "symlink", "directory", "link-different", "link-symlink", "link-directory", "link-parent-symlink"} {
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
			linkDirectory := filepath.Join(root, "network/10-netplan-enp1s0.link.d")
			if err := os.MkdirAll(linkDirectory, 0o700); err != nil {
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
			linkDropIn := filepath.Join(linkDirectory, "10-nkd-local-name.conf")
			switch scenario {
			case "different":
				err = os.WriteFile(dropIn, []byte("foreign\n"), 0o600)
			case "symlink":
				err = os.Symlink(filepath.Join(root, "grub"), dropIn)
			case "directory":
				err = os.Mkdir(dropIn, 0o700)
			case "link-different":
				err = os.WriteFile(linkDropIn, []byte("foreign\n"), 0o600)
			case "link-symlink":
				err = os.Symlink(filepath.Join(root, "grub"), linkDropIn)
			case "link-directory":
				err = os.Mkdir(linkDropIn, 0o700)
			case "link-parent-symlink":
				if err := os.Remove(linkDirectory); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Join(root, "grub.d"), linkDirectory)
			}
			if err != nil {
				t.Fatal(err)
			}
			projected := strings.ReplaceAll(config.Bootcmd[0], "/etc/default/grub.d", filepath.Join(root, "grub.d"))
			projected = strings.ReplaceAll(projected, "/etc/systemd/network", filepath.Join(root, "network"))
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
				if err != nil || string(observed) != "console=tty0 console=ttyAMA0,115200 net.ifnames=1 network-config=disabled\n" {
					t.Fatalf("console/naming lost or duplicated: %q, %v", observed, err)
				}
				link, err := os.ReadFile(linkDropIn)
				if err != nil || string(link) != "[Link]\nNamePolicy=path\n" {
					t.Fatalf("generated Netplan link naming policy lost or changed: %q, %v", link, err)
				}
			}
		})
	}
}

// Run the authored mechanical projection, not a second template transformer.
// The producer's source checksum authenticates the real template before this
// operation; these fixtures cover the emitted policy, not guest APT readiness.
func TestLaptopProfilesLocalAPTProjection(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "definitions", "nke-local-dev-arm64", "profiles-catalog", "registry.yaml"))
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
	projection := ""
	for _, line := range strings.Split(command[2], "\n") {
		if strings.HasPrefix(line, "sed '1d;98d;") {
			if projection != "" {
				t.Fatal("ambiguous source template projection")
			}
			projection = line
		}
	}
	if projection == "" {
		t.Fatal("missing local APT projection")
	}
	for _, scenario := range []string{"update-enabled", "update-disabled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "controller/test/fixtures/profiles/osProfiles/nke-nico-vms-24.04-arm64-fde/userData.tmpl")
			destination := filepath.Join(root, "bin/profiles-context/profiles/osProfiles/nke-local-26-04-arm64-plain/userData.tmpl")
			for _, path := range []string{source, destination} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			lines := make([]string, 98)
			for index := range lines {
				lines[index] = "# unchanged"
			}
			lines[0] = "#cloud-config"
			lines[5] = "package_update: true"
			if scenario == "update-disabled" {
				lines[5] = "package_update: false"
			}
			packages := []string{"nke-agent", "nke-kubeadm-distro-1.34.2", "nke-cni-ovn-images-1.4.0", "teleport-ent=18.6.4"}
			lines[89] = "runcmd:"
			for index, name := range packages {
				lines[90+index] = "  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 " + name
			}
			lines[97] = "  - curl http://foreign-agent/override"
			original := strings.Join(lines, "\n") + "\n"
			if err := os.WriteFile(source, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-euc", projection)
			cmd.Dir, cmd.WaitDelay = root, time.Second
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("project first-boot policy: %v\n%s", err, out)
			}
			data, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			value := string(data)
			if strings.Count(value, "package_update: false") != 1 || strings.Count(value, "Dir::Etc::sourceparts=-") != len(packages) {
				t.Fatal("first-boot package refresh or install projection differs")
			}
			for _, forbidden := range []string{"package_update: true", "apt-get install -y -o DPkg", "--ignore-missing", "foreign-agent", "#cloud-config"} {
				if strings.Contains(value, forbidden) {
					t.Fatalf("unexpected first-boot behavior %q", forbidden)
				}
			}
			for _, name := range packages {
				if !strings.Contains(value, "apt-get install -y -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/nke-local.list -o Dir::Etc::sourceparts=- -o DPkg::Lock::Timeout=600 "+name+" || exit 1") {
					t.Fatalf("lost source-owned package selection %q", name)
				}
			}
			var config struct {
				Runcmd []string `yaml:"runcmd"`
			}
			if err := yaml.Unmarshal(data, &config); err != nil || len(config.Runcmd) != len(packages) {
				t.Fatalf("projected command list differs: %v", err)
			}
			for _, failure := range []bool{false, true} {
				t.Run(fmt.Sprintf("install-failure-%t", failure), func(t *testing.T) {
					fixture := t.TempDir()
					status := "0"
					if failure {
						status = "100"
					}
					if err := os.WriteFile(filepath.Join(fixture, "apt-get"), []byte("#!/bin/sh\nexit "+status+"\n"), 0o700); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					// Match cloud-init's ordinary shell commands, without set -e.
					run := exec.CommandContext(ctx, "sh", "-c", strings.Join(config.Runcmd, "\n")+"\nprintf done > completion-marker\n")
					run.Dir, run.WaitDelay = fixture, time.Second
					run.Env = append(os.Environ(), "PATH="+fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
					out, err := run.CombinedOutput()
					if (err != nil) != failure {
						t.Fatalf("install failure propagation differs: %v\n%s", err, out)
					}
					_, markerErr := os.Stat(filepath.Join(fixture, "completion-marker"))
					if failure && !os.IsNotExist(markerErr) || !failure && markerErr != nil {
						t.Fatal("completion marker does not match installation result")
					}
				})
			}
			unchanged, err := os.ReadFile(source)
			if err != nil || string(unchanged) != original {
				t.Fatal("source template was modified")
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
	path := filepath.Join("definitions", "nke-local-dev-arm64", "definition-profiles.json")
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
						"sed '1d;98d;", "package_update: false", "Dir::Etc::sourcelist=/etc/apt/sources.list.d/nke-local.list", "Dir::Etc::sourceparts=-", " || exit 1", "net.ifnames=1", "update-grub", "bootcmd:",
						"/etc/default/grub.d/zz-nkd-local-network.cfg", "${GRUB_CMDLINE_LINUX} net.ifnames=1",
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
