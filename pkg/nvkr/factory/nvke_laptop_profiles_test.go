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

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Apply the authenticated patch with Git, not a second patch interpreter.
// The overlay adds local profile data without modifying upstream NVKE files.
func laptopProfilesPatchedFixture(t *testing.T) string {
	return laptopProfilesPatchedFixtureCatalog(t, "nvidia-arm64")
}

func laptopProfilesPatchedFixtureCatalog(t *testing.T, catalogName string) string {
	t.Helper()
	root := t.TempDir()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "inputs/catalogs/controller-profiles", catalogName)))
	if err != nil {
		t.Fatal(err)
	}
	patches := cat.Components["nvke-local-profiles"].Patches
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
	raw, err := os.ReadFile(filepath.Join(patched, "nvkr-local-profiles/osProfiles/nvke-local-26-04-arm64-plain/userData.tmpl"))
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
				cmd.Env = append(os.Environ(), "GRUB_CMDLINE_LINUX=build-time-poison", "NVKR_GRUB_FIXTURE="+root,
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
				"update-grub":           "#!/bin/sh\nset -eu\nprintf 'invoked' > \"$NVKR_GRUB_FIXTURE/invoked\"\n. \"$NVKR_GRUB_FIXTURE/grub\"\nfor file in \"$NVKR_GRUB_FIXTURE\"/grub.d/*.cfg; do . \"$file\"; done\nprintf '%s\\n' \"$GRUB_CMDLINE_LINUX\" > \"$NVKR_GRUB_FIXTURE/observed\"\n",
			}
			for path, value := range files {
				if err := os.WriteFile(filepath.Join(root, path), []byte(value), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			dropIn := filepath.Join(root, "grub.d/zz-nvkr-local-network.cfg")
			linkDropIn := filepath.Join(linkDirectory, "10-nvkr-local-name.conf")
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
	raw, err := os.ReadFile(filepath.Join(root, "nvkr-local-profiles/osProfiles/nvke-local-26-04-arm64-plain/userData.tmpl"))
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
		"nvke-agent nvke-kubeadm-distro-{{ trimPrefix .KubernetesVersion \"v\" }} spire-agent",
		"nvke-cni-calico-images-3.30.1 nvke-cni-calico-manifests-3.30.1",
		"nke-cni-ovn-images-2.4.2 nke-cni-ovn-charts-2.4.2",
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
	source := filepath.Join("..", "..", "..", "inputs/catalogs/controller-profiles/nvidia-arm64")
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

// Execute the authored staging command; BuildKit itself is an external gate.
func TestLaptopProfilesImageContext(t *testing.T) {
	for _, catalogName := range []string{"nvidia-arm64", "upstream-arm64"} {
		t.Run(catalogName, func(t *testing.T) { testLaptopProfilesImageContext(t, catalogName) })
	}
}

// Exact bytes from NVKE 8db5721ae3031bdcd945e559153a740d46ea209e, not a
// Dockerfile grammar or semantic validator. BuildKit owns that interpretation.
const laptopProfilesDockerfile = `# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

ARG BASE_REGISTRY=docker.io

FROM ${BASE_REGISTRY}/alpine:3.24.1

RUN mkdir -p /profiles
COPY profiles/ /profiles/
COPY runbooks/ /profiles/runbooks/
`

func testLaptopProfilesImageContext(t *testing.T, catalogName string) {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "inputs/catalogs/controller-profiles", catalogName)))
	if err != nil {
		t.Fatal(err)
	}
	command := cat.Components["nvke-local-profiles"].BuildCommand
	for _, tc := range []struct {
		name          string
		missing       string
		dockerfile    string
		beforeStaging bool
	}{
		{"complete", "", laptopProfilesDockerfile, false},
		{"missing epoch", "epoch", laptopProfilesDockerfile, true},
		{"missing runbooks", "controller/test/fixtures/runbooks/fixture.yaml", laptopProfilesDockerfile, false},
		{"missing config", "controller/test/fixtures/profiles/configProfiles/nico-vms-worker.yaml", laptopProfilesDockerfile, false},
		{"missing Dockerfile", "controller/profiles/example/Dockerfile", "", true},
		{"base substituted", "", strings.ReplaceAll(laptopProfilesDockerfile, "alpine:3.24.1", "alpine:latest"), true},
		{"registry substituted", "", strings.ReplaceAll(laptopProfilesDockerfile, "BASE_REGISTRY=docker.io", "BASE_REGISTRY=example.invalid"), true},
		{"extra stage", "", laptopProfilesDockerfile + "\nFROM ubuntu:24.04 AS extra\n", true},
		{"Dockerfile symlink", "", laptopProfilesDockerfile, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := laptopProfilesPatchedFixtureCatalog(t, catalogName)
			files := []string{
				"controller/test/fixtures/runbooks/fixture.yaml",
				"controller/test/fixtures/profiles/configProfiles/nico-vms-cp.yaml",
				"controller/test/fixtures/profiles/configProfiles/nico-vms-worker.yaml",
			}
			for _, name := range files {
				if name == tc.missing {
					continue
				}
				path := filepath.Join(fixture, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			dockerfile := filepath.Join(fixture, "controller/profiles/example/Dockerfile")
			if tc.missing != "controller/profiles/example/Dockerfile" {
				if err := os.MkdirAll(filepath.Dir(dockerfile), 0o755); err != nil {
					t.Fatal(err)
				}
				if tc.name == "Dockerfile symlink" {
					target := filepath.Join(fixture, "foreign-Dockerfile")
					if err := os.WriteFile(target, []byte(tc.dockerfile), 0o644); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, dockerfile); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(dockerfile, []byte(tc.dockerfile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			stub := t.TempDir()
			argsFile := filepath.Join(stub, "args")
			if err := os.WriteFile(filepath.Join(stub, "buildctl-daemonless.sh"), []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" > \"$BUILDKIT_ARGS\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			script := strings.ReplaceAll(strings.ReplaceAll(command[2], "/src", fixture), "{{.Arch}}", "arm64")
			cmd := exec.CommandContext(ctx, command[0], command[1], script)
			cmd.Dir, cmd.WaitDelay = fixture, time.Second
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "SOURCE_DATE_EPOCH=") && !strings.HasPrefix(env, "PATH=") && !strings.HasPrefix(env, "BUILDKIT_ARGS=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "BUILDKIT_ARGS="+argsFile)
			if tc.missing != "epoch" {
				cmd.Env = append(cmd.Env, "SOURCE_DATE_EPOCH=1791331916")
			}
			out, err := cmd.CombinedOutput()
			if tc.missing != "" || tc.beforeStaging {
				if err == nil {
					t.Fatal("incomplete source launched image build")
				}
				if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
					t.Fatalf("BuildKit was reached on incomplete source: %v", err)
				}
				if tc.beforeStaging {
					if _, err := os.Stat(filepath.Join(fixture, "bin")); !os.IsNotExist(err) {
						t.Fatalf("Dockerfile/epoch rejection wrote the staging directory: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("stage image context: %v\n%s", err, out)
			}
			contextRoot := filepath.Join(fixture, "bin/profiles-context")
			for _, pair := range []struct{ source, staged string }{
				{"nvkr-local-profiles/osProfiles/metadata.yaml", "profiles/osProfiles/metadata.yaml"},
				{"nvkr-local-profiles/osProfiles/nvke-local-26-04-arm64-plain/userData.tmpl", "profiles/osProfiles/nvke-local-26-04-arm64-plain/userData.tmpl"},
				{files[0], "runbooks/fixture.yaml"},
				{files[1], "profiles/configProfiles/nico-vms-cp.yaml"},
				{files[2], "profiles/configProfiles/nico-vms-worker.yaml"},
			} {
				source, err := os.ReadFile(filepath.Join(fixture, pair.source))
				if err != nil {
					t.Fatal(err)
				}
				staged, err := os.ReadFile(filepath.Join(contextRoot, pair.staged))
				if err != nil || string(source) != string(staged) {
					t.Fatalf("staged %s changed source bytes: %v", pair.staged, err)
				}
			}
			args, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"context=" + contextRoot, "dockerfile=" + fixture + "/controller/profiles/example", "platform=linux/arm64", "build-arg:SOURCE_DATE_EPOCH=1791331916", "dest=" + fixture + "/bin/nvke-local-profiles-arm64.oci.tar"} {
				if !strings.Contains(string(args), required) {
					t.Fatalf("missing BuildKit argument %q: %s", required, args)
				}
			}
		})
	}
}

func TestLaptopProfilesDockerfileAdmissionChangesBuildIdentity(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	path := filepath.Join("inputs", "definitions", "diagnostics", "demo-arm64", "profiles.json")
	envPath := "inputs/environments/local-arm64-tcg.json"
	for _, catalogName := range []string{"nvidia-arm64", "upstream-arm64"} {
		t.Run(catalogName, func(t *testing.T) {
			doc, err := definition.LoadDefinition(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			doc.Catalog.Base = "../../../catalogs/controller-profiles/" + catalogName
			env, err := definition.LoadEnvironment(t.Context(), envPath)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := CompileDocuments(t.Context(), path, doc, envPath, env, "dockerfile-admission")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 {
				t.Fatal("expected one profiles build")
			}
			graph := plan.Builds[0].Graph
			withoutAdmission := *graph
			withoutAdmission.Nodes = append([]ir.Node(nil), graph.Nodes...)
			for index, node := range withoutAdmission.Nodes {
				if node.Kind != ir.KindBuild {
					continue
				}
				prefix, remainder, found := strings.Cut(node.Command[2], "# Exact pinned source bytes")
				_, stage, staged := strings.Cut(remainder, "mkdir -p bin/profiles-context")
				if !found || !staged {
					t.Fatal("profiles command lost exact-byte admission before staging")
				}
				withoutAdmission.Nodes[index].Command = append([]string(nil), node.Command...)
				withoutAdmission.Nodes[index].Command[2] = prefix + "mkdir -p bin/profiles-context" + stage
			}
			before, err := withoutAdmission.Digest()
			if err != nil {
				t.Fatal(err)
			}
			after, err := graph.Digest()
			if err != nil || after != plan.Builds[0].BuildDigest || before == after {
				t.Fatalf("owning build identity did not bind admission: before=%s after=%s err=%v", before, after, err)
			}
			t.Logf("profiles admission changes Build Digest: %s -> %s", before, after)
		})
	}
}

// This admits one diagnostic native image, not profile/disk compatibility.
func TestLaptopProfilesArtifactContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	path := filepath.Join("inputs", "definitions", "diagnostics", "demo-arm64", "profiles.json")
	for _, tc := range []struct {
		name     string
		foreign  bool
		wantCode e.Code
	}{
		{"Docker", false, ""},
		{"foreign target", true, e.CodeNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := definition.LoadDefinition(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			envPath := "inputs/environments/local-arm64-tcg.json"
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
			profilesPin, err := plan.CatalogSnapshot.SourcePin("nvke-profiles")
			if err != nil {
				t.Fatal(err)
			}
			profilesRepository := plan.CatalogSnapshot.Sources["nvke-profiles"].Repo
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
					if node.Repo != profilesRepository || node.Commit != profilesPin.Commit || node.TreeHash != profilesPin.TreeHash {
						t.Fatal("profiles must use the staged-installer source")
					}
				case ir.KindBuild:
					producers++
					if node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || node.OutputType != "oci-image" || node.WriteDir != "bin" ||
						node.ArtifactPath != "bin/nvke-local-profiles-arm64.oci.tar" || !node.SourceReadOnly ||
						node.Toolchain != "moby/buildkit@sha256:f8a833b2de9d68e27f0815e4a737abdfaf8a2e4c615650557df11025101557b4" {
						t.Fatalf("unexpected producer: %+v", node)
					}
					if len(node.Command) != 3 || node.Command[0] != "sh" || node.Command[1] != "-ec" {
						t.Fatalf("expected source-owned Dockerfile image packaging, got %v", node.Command)
					}
					for _, required := range []string{
						"789f7ad1e406b82f0f55a52152eff7b08846b4122582145fade588c7d931cc3e  controller/profiles/example/Dockerfile",
						"--local context=/src/bin/profiles-context", "--local dockerfile=/src/controller/profiles/example",
						"--opt platform=linux/arm64", "build-arg:SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH",
						// BuildKit dockerui.NamedContext uses reference.FamiliarString
						// for lookup even when the source-owned FROM is fully qualified.
						"context:alpine:3.24.1=docker-image://docker.io/library/alpine@sha256:260479a1cfaf304c4c20da7f8405d3ce313513dcd534bb743257bdd2fe0f3e2d",
						"type=oci,dest=/src/bin/nvke-local-profiles-arm64.oci.tar,oci-mediatypes=true,rewrite-timestamp=true",
					} {
						if !strings.Contains(node.Command[2], required) {
							t.Fatalf("missing native image instruction %q", required)
						}
					}
					if len(node.Patches) != 1 || node.Patches[0].Name != "patches/local-profile.patch" ||
						node.Patches[0].SHA256 != "sha256:3928811f56ba0a47011afd16dc066c54189be06c238da43d801e92ae3fd02e22" {
						t.Fatal("Plan must retain the exact authenticated source patch")
					}
					command := node.Patches[0].Content
					for _, required := range []string{
						"package_update: false", "Dir::Etc::sourcelist=/etc/apt/sources.list.d/nke-local.list", "Dir::Etc::sourceparts=-", " || exit 1", "net.ifnames=1", "update-grub", "bootcmd:",
						"/etc/default/grub.d/zz-nvkr-local-network.cfg", "${GRUB_CMDLINE_LINUX} net.ifnames=1",
						"set -eu", "test ! -L", "test -f", "= \"$expected\"",
						"nvke-local-26-04-arm64-plain", "ubuntu_version: \"26.04\"", "variant: local-plain-tpm",
						"http://172.30.0.1:18080/nvke-node-image-arm64.qcow2", "console=ttyAMA0,115200",
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
