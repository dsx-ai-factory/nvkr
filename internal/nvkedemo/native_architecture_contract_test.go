// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDefaultDemoInputsBindHostAndGuestArchitecture(t *testing.T) {
	for _, platform := range []string{"linux/arm64", "linux/amd64", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			path, environmentPath, err := LocalArtifactInputsForPlatform("ovn", false, platform)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := definition.LoadDefinition(t.Context(), filepath.Join("../..", path))
			if err != nil {
				t.Fatal(err)
			}
			environment, err := definition.LoadEnvironment(t.Context(), filepath.Join("../..", environmentPath))
			if err != nil {
				t.Fatal(err)
			}
			if len(environment.ExecutionClasses) == 0 || string(environment.ExecutionClasses[0].HostPlatform) != platform {
				t.Fatalf("host %s selected a foreign Environment", platform)
			}
			_, arch, _ := strings.Cut(platform, "/")
			disk, err := selectedDiskOutputForArchitecture("ovn", arch)
			if err != nil {
				t.Fatal(err)
			}
			foundDisk := false
			for _, output := range doc.Outputs {
				if output.Platform != "linux/"+arch {
					t.Fatalf("%s selected foreign output %s/%s", platform, output.Name, output.Platform)
				}
				foundDisk = foundDisk || output.Name == disk && output.Format == definition.OutputDiskImage
			}
			if !foundDisk {
				t.Fatalf("%s omitted its selected disk %s", platform, disk)
			}
			if platform == "linux/amd64" && !definitionIncludesNativeTools(doc, platform) {
				t.Fatal("default AMD64 build would rebuild or omit native tools")
			}
		})
	}
}

func TestNativeArchitectureDomainObservation(t *testing.T) {
	if err := nativeLinuxDomainPlatform(`<domain type="kvm"><os><type arch="x86_64">hvm</type></os><devices><tpm/></devices></domain>`, "amd64"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, arch, machine, kind, tpm, cpu string
		want                                nvkrerr.Code
	}{
		{"ARM native", "arm64", "aarch64", "kvm", "<tpm/>", "4", ""},
		{"AMD native", "amd64", "x86_64", "kvm", "<tpm/>", "4", ""},
		{"wrong architecture", "amd64", "aarch64", "kvm", "<tpm/>", "4", nvkrerr.CodeEngineCapability},
		{"emulation", "amd64", "x86_64", "qemu", "<tpm/>", "4", nvkrerr.CodeEngineCapability},
		{"missing TPM", "amd64", "x86_64", "kvm", "", "4", nvkrerr.CodeEngineCapability},
		{"ambiguous TPM", "amd64", "x86_64", "kvm", "<tpm/><tpm/>", "4", nvkrerr.CodeEngineCapability},
		{"wrong CPU allocation", "amd64", "x86_64", "kvm", "<tpm/>", "6", nvkrerr.CodeVerifyFailed},
		{"missing CPU allocation", "amd64", "x86_64", "kvm", "<tpm/>", "0", nvkrerr.CodeVerifyFailed},
		{"unsupported architecture", "386", "i686", "kvm", "<tpm/>", "4", nvkrerr.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`<domain type=%q><os><type arch=%q>hvm</type></os><vcpu>%s</vcpu><devices>%s</devices></domain>`, tc.kind, tc.machine, tc.cpu, tc.tpm)
			err := nativeLinuxDomainResources(raw, tc.arch, 4)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != tc.want {
				t.Fatalf("observation: %v", err)
			}
		})
	}
}

func TestNativeArchitectureImportsAndNodeReadiness(t *testing.T) {
	if command := workloadJob(workloadFixtureImage).Spec.Template.Spec.Containers[0].Command; !strings.Contains(strings.Join(command, " "), "aarch64") {
		t.Fatal("legacy workload default changed")
	}
	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			reference := "docker.io/nvkr-local/selected@sha256:" + strings.Repeat("a", 64)
			script, err := nativeImageImportScriptForArchitecture("selected.tar", reference, arch)
			if err != nil || !strings.Contains(script, "--platform linux/"+arch+" ") || !strings.Contains(script, operatorQuote(reference)) {
				t.Fatalf("import: %s %v", script, err)
			}
			nodes := readyTenantFixture()
			for i := range nodes {
				nodes[i].Status.NodeInfo.Architecture = arch
			}
			if ready, err := readyNodesForCountVersionArchitecture(nodes, "1.34.2", 2, arch); !ready || err != nil {
				t.Fatalf("native nodes: %v %v", ready, err)
			}
			nodes[1].Status.NodeInfo.Architecture = map[string]string{"arm64": "amd64", "amd64": "arm64"}[arch]
			if ready, err := readyNodesForCountVersionArchitecture(nodes, "1.34.2", 2, arch); ready || err == nil {
				t.Fatal("foreign architecture accepted")
			}
		})
	}
	if script, err := nativeImageImportScriptForArchitecture("selected.tar", "unused", "386"); script != "" || err == nil {
		t.Fatal("unsupported architecture returned executable import")
	}
	if accelerator, err := nativeAccelerator("linux/amd64", ""); accelerator != "kvm" || err != nil {
		t.Fatalf("AMD default accelerator: %q %v", accelerator, err)
	}
	if definition, _, bundle, _, err := nativeToolSelection("linux/amd64"); err != nil || definition != "inputs/definitions/demo/native-linux-amd64/definition-tools.json" || bundle != "native-tools-linux-amd64" {
		t.Fatalf("explicit local AMD tools unavailable: %v", err)
	}
	if members, err := nativeRuntimeToolMembers("linux/amd64"); len(members) == 0 || err != nil {
		t.Fatalf("local AMD runtime tools unavailable: %v", err)
	}
}

func TestNativeSimulatorUsesSelectedCPUAndMemory(t *testing.T) {
	for _, tc := range []struct {
		name        string
		resources   NativeResources
		cpu, memory string
	}{
		{"default", NativeResources{}, "6", "12288"},
		{"explicit", NativeResources{6, 3, 1024, 4}, "4", "3072"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := nativeSimulatorEnvironment(startupTestContext(t), NativeGuestConfig{site: &nativeSiteAttempt{resources: tc.resources}}, linuxNativeBridge(), "/disk.qcow2", "/seed.yaml", "/sim")
			if err != nil || env["NICO_VMS_VM_VCPUS"] != tc.cpu || env["NICO_VMS_VM_MEMORY_MIB"] != tc.memory {
				t.Fatalf("selected guest resources: %v %v", env, err)
			}
		})
	}
}

func TestNativeArchitectureBootstrapPins(t *testing.T) {
	for _, tc := range []struct{ arch, machine, image, archive string }{
		{"arm64", "aarch64", "63a93bd5a8d76e33b15ceb5daa3657bd79be804748051ab178e643b0f5da22e7", "2deffe14625f93866f90a8f265d0f59d6fa476c7dc54b98adb327b1b4613efd4"},
		{"amd64", "x86_64", "8800651811af9a85465ad1d552add729947bb16488dddb4a9b5305a3d97332b2", "ff018f11ac2f1354bf7b3aaaca6e0b52d81139c746a89134744d0b7be3a0e62f"},
	} {
		t.Run(tc.arch, func(t *testing.T) {
			version, args, err := bootstrapLimaPlatformForArchitecture("linux", tc.arch)
			raw := strings.Join(args, " ")
			if err != nil || version != bootstrapLinuxLimaVersion {
				t.Fatalf("bootstrap: %v", err)
			}
			for _, exact := range []string{"--arch=" + tc.machine, "ubuntu-26.04-server-cloudimg-" + tc.arch + ".img", "nerdctl-full-2.4.1-linux-" + tc.arch + ".tar.gz", "sha256:" + tc.image, "sha256:" + tc.archive} {
				if !strings.Contains(raw, exact) {
					t.Fatalf("bootstrap missing exact %s", exact)
				}
			}
		})
	}
	for _, platform := range []string{"darwin", "windows"} {
		if _, _, err := bootstrapLimaPlatformForArchitecture(platform, "amd64"); err == nil {
			t.Fatalf("unsupported %s AMD bootstrap admitted", platform)
		}
	}
}

func TestNativeArchitectureHostToolAliases(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			bundle := delivery.BinaryBundle{OS: "linux", Arch: arch, Entries: []delivery.BinaryEntry{{Name: "kubectl-linux", Mode: 0755}}}
			bindings, err := nativeHostToolBindings(bundle, "linux/"+arch, []string{"kubectl"})
			if err != nil || bindings["kubectl"].Name != "kubectl-linux" {
				t.Fatalf("native tool alias: %v %v", bindings, err)
			}
			foreign := map[string]string{"arm64": "amd64", "amd64": "arm64"}[arch]
			if _, err := nativeHostToolBindings(bundle, "linux/"+foreign, []string{"kubectl"}); err == nil {
				t.Fatal("foreign tool architecture accepted")
			}
		})
	}
}

func TestNativeArchitecturePublishedGuestsBindObservedCPUAndDisk(t *testing.T) {
	for _, tc := range []struct {
		name, cpu, machine string
		pass               bool
	}{
		{"selected", "4", "x86_64", true},
		{"wrong CPU", "6", "x86_64", false},
		{"wrong platform", "4", "aarch64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, root, digest, work, names := tenantVMFixture(t)
			for _, name := range names {
				path := filepath.Join(root, name+".xml")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				xml := strings.ReplaceAll(string(raw), `arch="aarch64"`, `arch="`+tc.machine+`"`)
				xml = strings.Replace(xml, "<os>", "<vcpu>"+tc.cpu+"</vcpu><os>", 1)
				if err := os.WriteFile(path, []byte(xml), 0600); err != nil {
					t.Fatal(err)
				}
			}
			attempt := nativeSiteAttempt{guard: &NetworkGuard{}, assets: NativeAssets{arch: "amd64"}, simulatorWork: work, diskDigest: digest, resources: NativeResources{6, 3, 1024, 4}}
			domains, err := attempt.observeGuestDomains(t.Context(), runner)
			if (err == nil) != tc.pass || tc.pass && len(domains) != 3 {
				t.Fatalf("guest observation: %v %v", domains, err)
			}
			for _, domain := range domains {
				if domain.BackingDigest != digest {
					t.Fatal("selected disk binding lost")
				}
			}
		})
	}
}

func TestNativeSourceTagAliasRequiresExactReadback(t *testing.T) {
	reference := "ghcr.io/example/provider/controller@sha256:" + strings.Repeat("a", 64)
	alias := "ghcr.io/example/provider/controller:v1"
	for _, tc := range []struct {
		name, observed, exit string
		pass                 bool
	}{
		{"selected manifest", reference, "0", true},
		{"sibling manifest", strings.Replace(reference, strings.Repeat("a", 64), strings.Repeat("b", 64), 1), "0", false},
		{"missing manifest", "", "0", false},
		{"failed inspection with output", reference, "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for tool, body := range map[string]string{"ctr": "#!/bin/sh\nexit 0\n", "crictl": "#!/bin/sh\nprintf '%s\\n' \"$SELECTED_DIGEST\"\nexit \"$INSPECT_EXIT\"\n"} {
				if err := os.WriteFile(filepath.Join(dir, tool), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			script, err := nativeImageTagScriptForArchitecture(reference, alias, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "/bin/bash", "-ec", script)
			command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SELECTED_DIGEST="+tc.observed, "INSPECT_EXIT="+tc.exit)
			if raw, err := command.CombinedOutput(); (err == nil) != tc.pass {
				t.Fatalf("alias readback: %s %v", raw, err)
			}
		})
	}
	for _, foreign := range []string{"ghcr.io/example/foreign/controller:v1", "provider", reference} {
		if script, err := nativeImageTagScriptForArchitecture(reference, foreign, "amd64"); script != "" || err == nil {
			t.Fatalf("unsafe tag admitted: %q", foreign)
		}
	}
}

func TestNativeStartupUsesObservedArchitectureAndProviderAlias(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			cfg := startupDescription()
			cfg.arch = arch
			cfg.imageTags = map[string]string{"capnico": "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller:source-selected"}
			raw, err := SiteStartupUserData(startupTestContext(t), cfg)
			if err != nil || !strings.Contains(raw, "--platform linux/"+arch) || !strings.Contains(raw, "images tag --force") || !strings.Contains(raw, "source-selected") {
				t.Fatalf("startup: %v", err)
			}
		})
	}
}
