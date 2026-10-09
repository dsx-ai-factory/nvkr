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

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Apply the authenticated patch with Git, not a second patch interpreter.
// The overlay adds local profile data without modifying upstream NKE files.
func laptopProfilesPatchedFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "definitions", "nke-local-dev-arm64", "profiles-catalog")))
	if err != nil {
		t.Fatal(err)
	}
	patches := cat.Components["nke-local-profiles"].Patches
	if len(patches) != 1 {
		t.Fatal("expected one authenticated profile patch")
	}
	for _, args := range [][]string{
		{"apply", "--no-index", "--check", "--whitespace=nowarn", "-"},
		{"apply", "--no-index", "--whitespace=nowarn", "-"},
		{"apply", "--no-index", "--reverse", "--check", "--whitespace=nowarn", "-"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.WaitDelay = root, time.Second
		cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))
		cmd.Stdin = strings.NewReader(patches[0].Content)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("apply authenticated profile patch: %v\n%s", err, out)
		}
	}
	return root
}

// Exercise the patched shell, not a duplicate of its configuration mutation.
// The GRUB loader is modeled; udev naming and a real reboot remain acceptance.
func TestLaptopProfilesGRUBDropIn(t *testing.T) {
	patched := laptopProfilesPatchedFixture(t)
	raw, err := os.ReadFile(filepath.Join(patched, "nkd-local-profiles/osProfiles/nke-local-26-04-arm64-plain/userData.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	boot, _, found := strings.Cut(string(raw), "\nhostname:")
	if !found {
		t.Fatal("missing cloud-init boot command boundary")
	}
	var config struct {
		Bootcmd []string `yaml:"bootcmd"`
	}
	if err := yaml.Unmarshal([]byte(boot), &config); err != nil || len(config.Bootcmd) != 1 {
		t.Fatalf("patched bootcmd differs: %v", err)
	}
	for _, scenario := range []string{"new-and-repeat", "eth0-new-and-repeat", "missing-uplink", "ambiguous-uplink", "unsupported-uplink", "different", "symlink", "directory", "link-different", "link-symlink", "link-directory", "link-parent-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
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
			if err := os.Mkdir(filepath.Join(root, "grub.d"), 0o700); err != nil {
				t.Fatal(err)
			}
			uplink := "enp1s0"
			if scenario == "eth0-new-and-repeat" {
				uplink = "eth0"
			}
			if scenario != "missing-uplink" {
				physical := uplink
				if scenario == "unsupported-uplink" {
					physical = "eth1"
				}
				if err := os.MkdirAll(filepath.Join(root, "sys/class/net", physical, "device"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "ambiguous-uplink" {
				if err := os.MkdirAll(filepath.Join(root, "sys/class/net/eth0/device"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			linkDirectory := filepath.Join(root, "network", "10-netplan-"+uplink+".link.d")
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
			projected = strings.ReplaceAll(projected, "/sys/class/net", filepath.Join(root, "sys/class/net"))
			for iteration := range 2 {
				out, err := run(projected)
				if scenario != "new-and-repeat" && scenario != "eth0-new-and-repeat" {
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
				if err != nil || string(link) != "[Link]\nNamePolicy=\nName="+uplink+"\n" {
					t.Fatalf("generated Netplan link naming policy lost or changed: %q, %v", link, err)
				}
			}
		})
	}
}

// Inspect and execute the actual patched APT commands. Full cloud-init template
// rendering and real boot remain separate acceptance checks.
func TestLaptopProfilesLocalAPTProjection(t *testing.T) {
	root := laptopProfilesPatchedFixture(t)
	raw, err := os.ReadFile(filepath.Join(root, "nkd-local-profiles/osProfiles/nke-local-26-04-arm64-plain/userData.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(raw)
	for _, forbidden := range []string{"package_update: true", "apt-get install -y -o DPkg", "--ignore-missing", "carbide-pxe"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("unexpected first-boot behavior %q", forbidden)
		}
	}
	if strings.Count(value, "package_update: false") != 1 {
		t.Fatal("first-boot refresh must be disabled exactly once")
	}
	var commands []string
	for _, line := range strings.Split(value, "\n") {
		if strings.Contains(line, "apt-get install -y ") {
			commands = append(commands, strings.TrimPrefix(line, "  - "))
		}
	}
	packages := []string{
		"teleport-ent=18.6.4",
		"nke-agent nke-kubeadm-distro-{{ trimPrefix .KubernetesVersion \"v\" }} spire-agent",
		"nke-cni-calico-images-3.30.1 nke-cni-calico-manifests-3.30.1",
		"nke-cni-ovn-images-1.4.0 nke-cni-ovn-charts-1.4.0",
	}
	if len(commands) != len(packages) {
		t.Fatal("source-owned package selection changed")
	}
	for index, name := range packages {
		want := "DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/nke-local.list -o Dir::Etc::sourceparts=- -o DPkg::Lock::Timeout=600 " + name + " || exit 1"
		if commands[index] != want {
			t.Fatalf("unexpected install command %q; want %q", commands[index], want)
		}
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
			// No set -e: the explicit exits must stop cloud-init's shell list.
			run := exec.CommandContext(ctx, "sh", "-c", strings.Join(commands, "\n")+"\nprintf done > completion-marker\n")
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
}

func TestLaptopProfilesRejectTamperedPatch(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join("..", "..", "..", "definitions", "nke-local-dev-arm64", "profiles-catalog")
	for _, name := range []string{"registry.yaml", "pins.lock.yaml", "patches/local-profile.patch"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "patches/local-profile.patch" {
			data = append(data, []byte("\n# substituted\n")...)
		}
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := catalog.LoadContext(t.Context(), catalog.Dir(dir))
	assertFactoryErrorCode(t, err, e.CodeUnpinnedSource)
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
			if len(plan.Builds) != 1 || len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "controller-profiles" || plan.LifecycleOutputs[0].Kind != "executable" {
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
					if node.BuilderType != ir.BuilderOCIHermetic || node.OutputType != "tarball" || node.WriteDir != "bin" {
						t.Fatalf("unexpected producer: %+v", node)
					}
					if len(node.Command) != 3 || node.Command[0] != "bash" || node.Command[1] != "-euc" ||
						!strings.Contains(node.Command[2], "tar --sort=name") || !strings.Contains(node.Command[2], "gzip -n") ||
						strings.Contains(node.Command[2], "buildctl") {
						t.Fatalf("expected deterministic data packaging, got %v", node.Command)
					}
					if len(node.Patches) != 1 || node.Patches[0].Name != "patches/local-profile.patch" ||
						node.Patches[0].SHA256 != "sha256:1ecaaa68a35103f8b11c56ee036405df24e4816082e46a4644b0ce05347320f4" {
						t.Fatal("Plan must retain the exact authenticated source patch")
					}
					command := node.Patches[0].Content
					for _, required := range []string{
						"package_update: false", "Dir::Etc::sourcelist=/etc/apt/sources.list.d/nke-local.list", "Dir::Etc::sourceparts=-", " || exit 1", "net.ifnames=1", "update-grub", "bootcmd:",
						"/etc/default/grub.d/zz-nkd-local-network.cfg", "${GRUB_CMDLINE_LINUX} net.ifnames=1",
						"set -eu", "test ! -L", "test -f", "= \"$expected\"",
						"nke-local-26-04-arm64-plain", "ubuntu_version: \"26.04\"", "variant: local-plain-tpm",
						"http://172.30.0.1:18080/nke-node-image-arm64.qcow2", "console=ttyAMA0,115200",
					} {
						if !strings.Contains(command, required) {
							t.Fatalf("missing bound producer instruction %q", required)
						}
					}
					for _, forbidden := range []string{"--- a/controller/", "Dockerfile", "build.sh", "luks.key=", "set luks-key", "\nmake ", "\ndocker push", "sed '1d;98d;", "buildctl-daemonless.sh build --frontend dockerfile.v0 --local context=/src/."} {
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
