// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"sigs.k8s.io/yaml"
)

// These tests cover the supplied Ubuntu 24 inputs and generic compilation. They
// do not establish an image bake, guest ABI compatibility, boot or qualification.
func TestNVKEUbuntu24NativeDiskSelections(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	identities := make(map[string]bool)
	for _, tc := range []struct {
		arch, installer, patched, plain, isoURL, isoDigest string
	}{
		{"amd64", "ubuntu-installer-iso-amd64", "nvke-patched-installer-iso-amd64", "nvke-node-image-amd64",
			"https://releases.ubuntu.com/24.04.5/ubuntu-24.04.5-live-server-amd64.iso",
			"sha256:97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8"},
		{"arm64", "ubuntu-installer-iso", "nvke-patched-installer-iso", "nvke-node-image",
			"https://cdimage.ubuntu.com/ubuntu/releases/24.04.5/release/ubuntu-24.04.5-live-server-arm64.iso",
			"sha256:f12484464c7d4d73e2167e0a7dbda1e669f8fae9bcf778dc0944937034b320cb"},
	} {
		for _, variant := range []struct{ output, source, encryption string }{{"image", "non-fde", "none"}, {"fde", "fde", "luks2"}} {
			name := "nvke-node-" + variant.output + "-ubuntu24-" + tc.arch
			t.Run(name, func(t *testing.T) {
				plan, baseline := nvkeUbuntu24Plans(t, "inputs/definitions/diagnostics/ubuntu24/definition-github-"+variant.output+"-"+tc.arch+".yaml", tc.arch)
				build := nvkeUbuntu24Build(t, plan, name)
				original := nvkeUbuntu24Build(t, baseline, name)
				root := tc.plain
				if variant.output == "fde" {
					root = "nvke-node-fde-" + tc.arch
				}
				wantComponents := []string{"nvke-agent", "nvke-cni-calico", "nvke-cni-ovn", "nvke-image-inputs", "nvke-kubeadm-distro",
					root, tc.patched, "nvke-spire-agent", "ovn-kube-ubuntu", "ovn-kubernetes-chart", tc.installer}
				if build.Platform != "linux/"+tc.arch || !slices.Equal(build.Components, wantComponents) || len(build.ResolvedComponents) != 11 {
					t.Fatalf("unexpected native material closure: %s / %v", build.Platform, build.Components)
				}
				if build.BuildDigest == original.BuildDigest || identities[build.BuildDigest] {
					t.Fatal("Ubuntu release, architecture and encryption selections must have distinct Build identities")
				}
				identities[build.BuildDigest] = true
				iso := build.CatalogSnapshot.Components[tc.installer]
				if iso.Prebuilt == nil || iso.Prebuilt.URL != tc.isoURL || iso.Prebuilt.Digests[tc.arch] != tc.isoDigest || iso.Tag != "24.04.5" {
					t.Fatalf("Ubuntu 24 installer is not bound to the exact native ISO: %+v", iso)
				}
				for component, producer := range build.CatalogSnapshot.Components {
					if producer.Packer != nil {
						if producer.Packer.Variables["ubuntu_series"] != "24.04" || original.CatalogSnapshot.Components[component].Packer.Variables["ubuntu_series"] != "26.04" {
							t.Fatal("disk Ubuntu series must match the selected installer")
						}
						packer := *producer.Packer
						packer.Variables = maps.Clone(packer.Variables)
						packer.Variables["ubuntu_series"] = "26.04"
						producer.Packer = &packer
					}
					if component != "nvke-agent" && component != "ubuntu-installer-iso" && component != "ubuntu-installer-iso-amd64" &&
						!reflect.DeepEqual(producer, original.CatalogSnapshot.Components[component]) {
						t.Fatalf("Ubuntu selection unexpectedly changed unrelated producer %s", component)
					}
				}
				sourcePin, err := build.CatalogSnapshot.SourcePin("nvke-os")
				if err != nil {
					t.Fatal(err)
				}
				roots := 0
				for _, node := range build.Graph.Nodes {
					if node.Kind == ir.KindPull && node.Component == tc.installer && node.Digest != tc.isoDigest {
						t.Fatal("installer pin did not reach the acquisition node")
					}
					if node.BuilderType != ir.BuilderVMImage {
						continue
					}
					roots++
					if node.Component != root || node.SourceCommit != sourcePin.Commit || node.SourceTreeHash != sourcePin.TreeHash || node.Packer == nil ||
						node.Disk == nil || node.ExecutionBinding == nil || node.Requirements == nil {
						t.Fatal("Ubuntu 24 disk lost its source-owned native Packer contract")
					}
					if node.Packer.BuildName != "nvke-node-"+variant.source+".qemu.nvke-"+variant.source ||
						node.ArtifactPath != variant.source+"/nvke-node.qcow2" || node.Packer.AcquisitionArchive != variant.source+"/apt-acquisitions.tar" ||
						node.Disk.Encryption != variant.encryption || node.ToolchainPlatform != build.Platform {
						t.Fatal("Ubuntu 24 changed the pinned source's disk output contract")
					}
					if node.ExecutionBinding.Class.RunsOn != "arc-"+tc.arch+"-kvm" ||
						!slices.Equal(node.Requirements.Capabilities, []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}}) {
						t.Fatal("Ubuntu 24 must retain the explicit native required-KVM route")
					}
					if err := pipeline.ValidateVMImagePackerDiskExecution(node); err != nil {
						t.Fatal(err)
					}
				}
				if roots != 1 {
					t.Fatalf("selected %d disk roots, want one", roots)
				}

				// Each independently changed input must affect identity: the ISO
				// selects the guest OS; the agent compiler selects its native ABI.
				for _, component := range []string{tc.installer, "nvke-agent"} {
					t.Run("restore-"+component, func(t *testing.T) {
						producer := original.CatalogSnapshot.Components[component]
						stack, err := catalog.ApplyStack(build.CatalogSnapshot, catalog.Overlay{
							APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "restore-input",
							RequiresCatalogDigest: build.CatalogDigest,
							Components:            map[string]catalog.ComponentReplacement{component: {Component: &producer}},
						})
						if err != nil {
							t.Fatal(err)
						}
						selected, err := buildgraph.Resolve(stack.Catalog, buildgraph.Selection{
							Components: build.Components, Platform: build.Platform, OutputFormat: definition.OutputDiskImage,
						})
						if err != nil {
							t.Fatal(err)
						}
						graph, err := buildgraph.CompileForHostPlatforms(selected, map[string]execution.HostPlatform{root: execution.HostPlatform(build.Platform)})
						if err != nil {
							t.Fatal(err)
						}
						digest, err := graph.Digest()
						if err != nil || digest == build.BuildDigest {
							t.Fatalf("restoring %s must change Build identity: %s / %v", component, digest, err)
						}
					})
				}
				nvkeUbuntu24GeneratedBundle(t, plan, tc.arch)
			})
		}
	}
}

func nvkeUbuntu24GeneratedBundle(t *testing.T, plan *factory.Plan, arch string) {
	t.Helper()
	bundle, err := factory.RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			RunsOn string                `json:"runs-on"`
			Steps  []struct{ ID string } `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(bundle.Files[bundle.WorkflowPath], &workflow); err != nil {
		t.Fatal(err)
	}
	diskJobs := map[string]int{}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.ID == "run-disk" {
				diskJobs[job.RunsOn]++
			}
		}
	}
	if !reflect.DeepEqual(diskJobs, map[string]int{"arc-" + arch + "-kvm": 1}) {
		t.Fatalf("generated native disk jobs = %v", diskJobs)
	}
	root := t.TempDir()
	if err := factory.WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	checked, err := factory.RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.CheckGeneratedBundle(t.Context(), root, checked); err != nil {
		t.Fatal(err)
	}
}

func nvkeUbuntu24Plans(t *testing.T, definitionPath, arch string) (*factory.Plan, *factory.Plan) {
	t.Helper()
	environmentPath := "inputs/environments/github-ubuntu24-" + arch + ".yaml"
	authored, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definitionPath, authored, environmentPath, environment, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.LifecycleOutputs) != 1 || len(plan.Builds) != 1 || plan.Qualification != factory.TierStaticUnqualified ||
		plan.Environment.Publication != nil || plan.Environment.Signing != nil {
		t.Fatal("Ubuntu 24 must select one build-only native disk per workflow without publication or qualification")
	}
	// Remove only the final overlay to compare the same named output and native
	// toolchains against the retained Ubuntu 26 source/material selection.
	prior := authored.Clone()
	stack := prior.Outputs[0].Catalog
	if stack == nil || stack.Base != "../../../catalogs/node-images/release" ||
		!slices.Equal(stack.Overlays, []string{"../../../catalogs/node-images/variants/native-toolchains.yaml", "../../../catalogs/node-images/variants/ubuntu24.json"}) {
		t.Fatalf("output %s lost its exact ordered catalog stack: %+v", prior.Outputs[0].Name, stack)
	}
	stack.Overlays = stack.Overlays[:1]
	baseline, err := Compile(t.Context(), definitionPath, prior, environmentPath, environment, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	return plan, baseline
}

func TestNVKEUbuntu24LocalDiskSelections(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			hosted, err := CompileFiles(t.Context(), "inputs/definitions/diagnostics/ubuntu24/definition-github-image-"+arch+".yaml",
				"inputs/environments/github-ubuntu24-"+arch+".yaml", "test (deadbeef)")
			if err != nil {
				t.Fatal(err)
			}
			definitionPath := "inputs/definitions/diagnostics/ubuntu24/definition-" + arch + ".yaml"
			environmentPath := "inputs/environments/local-linux-" + arch + "-kvm.yaml"
			plan, err := CompileFiles(t.Context(), definitionPath, environmentPath, "test (deadbeef)")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 || len(plan.LifecycleOutputs) != 1 || plan.Qualification != factory.TierStaticUnqualified {
				t.Fatal("local Ubuntu 24 selection must contain one unqualified disk")
			}
			build := nvkeUbuntu24Build(t, plan, "nvke-node-image-ubuntu24-"+arch)
			if build.BuildDigest != nvkeUbuntu24Build(t, hosted, build.Name).BuildDigest {
				t.Fatal("local and hosted Ubuntu 24 selections must retain identical logical Build inputs")
			}
			if build.Platform != "linux/"+arch || len(build.Components) != 11 {
				t.Fatalf("local native closure = %s / %v", build.Platform, build.Components)
			}
			for _, node := range build.Graph.Nodes {
				if node.BuilderType == ir.BuilderVMImage && (node.ExecutionBinding == nil ||
					!strings.HasPrefix(node.ExecutionBinding.Class.RunsOn, "local-linux-"+arch) || node.ToolchainPlatform != build.Platform) {
					t.Fatal("local selection lost its native execution class")
				}
			}
			otherArch := "amd64"
			if arch == otherArch {
				otherArch = "arm64"
			}
			crossed, err := CompileFiles(t.Context(), definitionPath,
				"inputs/environments/local-linux-"+otherArch+"-kvm.yaml", "test (deadbeef)")
			assertCode(t, err, e.CodeEngineCapability)
			if crossed != nil {
				t.Fatal("crossed native environment returned a Plan")
			}
		})
	}
}

func nvkeUbuntu24Build(t *testing.T, plan *factory.Plan, name string) factory.BuildPlan {
	t.Helper()
	index := slices.IndexFunc(plan.Builds, func(build factory.BuildPlan) bool { return build.Name == name })
	if index < 0 {
		t.Fatal("missing Ubuntu 24 disk build", name)
	}
	return plan.Builds[index]
}

func TestNVKEUbuntu24AgentRejectsUnsupportedLibcBeforeWork(t *testing.T) {
	overlay, err := catalog.LoadOverlayContext(t.Context(), "../../../inputs/catalogs/node-images/variants/ubuntu24.json")
	if err != nil {
		t.Fatal(err)
	}
	agent := overlay.Components["nvke-agent"].Component
	if agent == nil || !agent.CGO {
		t.Fatal("Ubuntu 24 agent must retain its source-owned CGO build")
	}
	for _, phase := range []struct {
		name, firstTool string
		command         []string
	}{{"acquisition", "mkdir", agent.VendorCommand}, {"build", "make", agent.BuildCommand}} {
		for _, tc := range []struct {
			name, libc string
			missing    bool
			wantWork   bool
		}{
			{name: "pinned builder", libc: "glibc 2.36", wantWork: true},
			{name: "newer builder", libc: "glibc 2.42"},
			{name: "unrecognized libc", libc: "musl"},
			{name: "missing libc probe", missing: true},
		} {
			t.Run(phase.name+"/"+tc.name, func(t *testing.T) {
				if len(phase.command) != 3 || phase.command[0] != "bash" || phase.command[1] != "-ec" {
					t.Fatal("expected one guarded authored shell invocation")
				}
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				for _, directory := range []string{bin, filepath.Join(root, "agent")} {
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if !tc.missing {
					if err := os.WriteFile(filepath.Join(bin, "getconf"), []byte("#!/bin/sh\nprintf '%s\\n' \"$NVKR_TEST_LIBC\"\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/sh\nprintf x86_64\n"), 0700); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(root, "work-started")
				if err := os.WriteFile(filepath.Join(bin, phase.firstTool), []byte("#!/bin/sh\nprintf 'work started\\n' > \"$NVKR_TEST_MARKER\"\nexit 73\n"), 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "/bin/bash", phase.command[1:]...)
				cmd.Dir = root
				cmd.Env = []string{"PATH=" + bin, "NVKR_TEST_LIBC=" + tc.libc, "NVKR_TEST_MARKER=" + marker}
				cmd.WaitDelay = time.Second
				output, runErr := cmd.CombinedOutput()
				if runErr == nil || ctx.Err() != nil {
					t.Fatalf("expected bounded guard/sentinel failure, got %v / %s", runErr, output)
				}
				_, markerErr := os.Stat(marker)
				if tc.wantWork {
					if markerErr != nil || cmd.ProcessState.ExitCode() != 73 {
						t.Fatalf("pinned builder must reach the first work command: %v / %s", runErr, output)
					}
				} else if !os.IsNotExist(markerErr) || !strings.Contains(string(output), "requires the pinned Bookworm glibc 2.36 builder") {
					t.Fatalf("unsupported libc must fail before any acquisition or build: marker=%v / %s", markerErr, output)
				}
			})
		}
	}
}

func TestNVKEUbuntu24AgentPreservesDynamicAgentAndStaticUpdater(t *testing.T) {
	overlay, err := catalog.LoadOverlayContext(t.Context(), "../../../inputs/catalogs/node-images/variants/ubuntu24.json")
	if err != nil {
		t.Fatal(err)
	}
	agent := overlay.Components["nvke-agent"].Component
	if agent == nil || len(agent.BuildCommand) != 3 || agent.BuildCommand[0] != "bash" || agent.BuildCommand[1] != "-ec" {
		t.Fatal("expected the guarded source-owned agent build")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, tc := range []struct {
			name, checkerExit, readelfExit, headers string
			missingUpdater, wantReadelf             bool
			wantExit                                int
		}{
			{name: "dynamic agent and static updater", checkerExit: "0", readelfExit: "0", headers: "Program Headers:\n LOAD\n", wantReadelf: true},
			{name: "agent ABI rejected", checkerExit: "73", readelfExit: "0", wantExit: 73},
			{name: "dynamic updater rejected", checkerExit: "0", readelfExit: "0", headers: "Program Headers:\n INTERP\n", wantReadelf: true, wantExit: 1},
			{name: "unreadable updater rejected", checkerExit: "0", readelfExit: "74", wantReadelf: true, wantExit: 74},
			{name: "missing updater rejected", checkerExit: "0", readelfExit: "0", missingUpdater: true, wantExit: 1},
		} {
			t.Run(arch+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "tools")
				binaryDirectory := filepath.Join(root, "agent", "bin", "linux_"+arch)
				for _, directory := range []string{bin, binaryDirectory} {
					if err := os.MkdirAll(directory, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if !tc.missingUpdater {
					if err := os.WriteFile(filepath.Join(binaryDirectory, "nvke-agent-updater"), []byte("updater fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				for name, script := range map[string]string{
					"getconf": "#!/bin/sh\nprintf 'glibc 2.36\\n'\n",
					"make":    "#!/bin/sh\nexit 0\n",
					"bash": `#!/bin/sh
set -eu
test "$#" = 4
test "$1" = /src/os-image/nix/check-ubuntu-elf.sh
test "$2" = --arch
test "$3" = "$NVKR_TEST_ARCH"
test "$4" = "$NVKR_TEST_SOURCE/agent/bin/linux_${NVKR_TEST_ARCH}/nvke-agent"
printf 'checked agent\n' > "$NVKR_TEST_SOURCE/agent-check"
exit "$NVKR_TEST_CHECK_EXIT"
`,
					"readelf": `#!/bin/sh
set -eu
test "$#" = 2
test "$1" = -lW
test "$2" = "$NVKR_TEST_SOURCE/agent/bin/linux_${NVKR_TEST_ARCH}/nvke-agent-updater"
printf 'checked updater\n' > "$NVKR_TEST_SOURCE/updater-check"
printf '%s\n' "$NVKR_TEST_HEADERS"
exit "$NVKR_TEST_READELF_EXIT"
`,
				} {
					if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := strings.NewReplacer("{{.Arch}}", arch, "/src/agent/bin/", root+"/agent/bin/").Replace(agent.BuildCommand[2])
				cmd := exec.CommandContext(ctx, "/bin/bash", "-ec", command)
				cmd.Dir = root
				cmd.Env = []string{"PATH=" + bin, "NVKR_TEST_ARCH=" + arch, "NVKR_TEST_SOURCE=" + root,
					"NVKR_TEST_CHECK_EXIT=" + tc.checkerExit, "NVKR_TEST_READELF_EXIT=" + tc.readelfExit, "NVKR_TEST_HEADERS=" + tc.headers}
				cmd.WaitDelay = time.Second
				output, runErr := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatal("authored build/check command exceeded its test bound")
				}
				if _, err := os.Stat(filepath.Join(root, "agent-check")); err != nil {
					t.Fatalf("authored build did not check the exact dynamic agent binary: %v / %s", runErr, output)
				}
				_, updaterErr := os.Stat(filepath.Join(root, "updater-check"))
				if tc.wantReadelf && updaterErr != nil || !tc.wantReadelf && !os.IsNotExist(updaterErr) {
					t.Fatalf("unexpected static updater check: %v / %s", runErr, output)
				}
				if cmd.ProcessState.ExitCode() != tc.wantExit {
					t.Fatalf("ELF checks returned %d, want %d: %v / %s", cmd.ProcessState.ExitCode(), tc.wantExit, runErr, output)
				}
			})
		}
	}
}
