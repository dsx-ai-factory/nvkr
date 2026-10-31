// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Original69ef9924 ARM64 MachineImage boot script, not a DHCP sentinel.
const nativeMachineImageBootTemplate = `#!ipxe
# nico-sim boots the qcow2 directly and never chains an EFI, so this URL is
# only here because the chain line's grammar needs it. Kept unreachable rather
# than pointing at CloudFront, which nothing would fetch. Same value as
# controller/test/kubeadm/internal/nicovms/launch.go.
set boot-loader http://127.0.0.1/qcow-imager.efi
# The lane rewrites this to the qcow2 it pulled from GHCR. Unreachable here for
# the same reason as boot-loader: this repository names no CloudFront host.
set image-url http://127.0.0.1/nvke-node.qcow2
set disk-strategy {{ .DiskSelectionStrategy }}

# You probably don't need to change these.
# Not a copy of the amd64 line. A virt machine's only UART is a PL011, and
# adaptConsoleForVM rewrites ttyS1 to ttyS0, which does not exist here.
set console-config console=tty0 console=ttyAMA0,115200
# Not a copy of the amd64 line. pci=realloc and reboot=pci are x86-only, and
# intel_iommu=on names a controller no ARM machine has. iommu.passthrough=1 is
# the architecture-neutral spelling of iommu=pt.
set kernel-params loglevel=1 iommu.passthrough=1
set luks-key luks.key=changeit
set cloud-init-ds ds=nocloud-net;s=${cloudinit-url}

chain ${boot-loader} ${console-config} ${kernel-params} ${luks-key} image_url=${image-url} disk_strategy=${disk-strategy} ${cloud-init-ds} update_grub_template=yes update_grub_cfg=yes
boot
`

func TestNativeSiteBootUsesTheActualSourceFixture(t *testing.T) {
	const disk = "http://172.30.0.1:18082/disk.qcow2"
	for _, tc := range []struct {
		name, source, url string
		valid             bool
	}{
		{"source", nativeMachineImageBootTemplate, disk, true},
		{"empty", "", disk, false},
		{"missing image", strings.ReplaceAll(nativeMachineImageBootTemplate, "http://127.0.0.1/nvke-node.qcow2", ""), disk, false},
		{"duplicate", nativeMachineImageBootTemplate + nativeMachineImageBootTemplate, disk, false},
		{"duplicate current", strings.ReplaceAll(nativeMachineImageBootTemplate+nativeMachineImageBootTemplate, "http://127.0.0.1/nvke-node.qcow2", "http://127.0.0.1:18080/files/images/nvke-node.qcow2"), disk, false},
		{"mixed source slots", nativeMachineImageBootTemplate + strings.ReplaceAll(nativeMachineImageBootTemplate, "http://127.0.0.1/nvke-node.qcow2", "http://127.0.0.1:18080/files/images/nvke-node.qcow2"), disk, false},
		{"unknown template", strings.ReplaceAll(nativeMachineImageBootTemplate, ".DiskSelectionStrategy", ".Other"), disk, false},
		{"remote disk", nativeMachineImageBootTemplate, "https://registry.example.test/image.qcow2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boot, err := projectNativeSiteBoot(t.Context(), tc.source, tc.url)
			if (err == nil) != tc.valid {
				t.Fatalf("projection: %v", err)
			}
			if !tc.valid {
				if boot != nil {
					t.Fatal("rejected fixture issued a boot selection")
				}
				return
			}
			want := strings.Replace(strings.Replace(nativeMachineImageBootTemplate, "http://127.0.0.1/nvke-node.qcow2", disk, 1), "{{ .DiskSelectionStrategy }}", "smallest", 1)
			if boot.script != want || boot.diskURL != disk || !strings.Contains(boot.script, "${cloudinit-url}") {
				t.Fatal("source-owned image boot projection changed unrelated bytes")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if boot, err := projectNativeSiteBoot(ctx, nativeMachineImageBootTemplate, disk); err == nil || boot != nil {
		t.Fatal("canceled projection issued a selection")
	}
}

func TestNativeSiteCreateCarriesTheSourceSelectedBootScript(t *testing.T) {
	boot, err := projectNativeSiteBoot(t.Context(), nativeMachineImageBootTemplate, "http://172.30.0.1:18082/disk.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["ipxeScript"] != boot.script || body["userData"] != "#cloud-config\n" || body["phoneHomeEnabled"] != true || body["vpcId"] != VPCID {
			t.Error("site create lost exact source boot/startup/allocator binding")
		}
		_, _ = w.Write([]byte(`{"id":"site"}`))
	}))
	defer server.Close()
	for _, selected := range []*nativeSiteBoot{nil, {}, {script: "", diskURL: boot.diskURL}} {
		if _, err := createNativeGuest(t.Context(), server.URL, "site", "a1b2c3d4-0005-4000-8000-000000000004", []byte("#cloud-config\n"), selected); err == nil {
			t.Fatal("missing site boot reached creation")
		}
	}
	if calls != 0 {
		t.Fatal("invalid site boot reached POST")
	}
	if _, err := createNativeGuest(t.Context(), server.URL, "site", "a1b2c3d4-0005-4000-8000-000000000004", []byte("#cloud-config\n"), boot); err != nil || calls != 1 {
		t.Fatalf("create: calls=%d err=%v", calls, err)
	}
}

func TestNativeSiteBootCarriesDHCPAddressModeThroughReadinessAndSetup(t *testing.T) {
	const disk = "http://172.30.0.1:18082/disk.qcow2"
	boot, err := projectNativeSiteBoot(t.Context(), nativeMachineImageBootTemplate, disk)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reply string
		valid       bool
	}{
		{"confirmed lease", `{"status":"Ready","interfaces":[{"ipAddresses":["172.30.0.2"]}]}`, true},
		{"upper lease", `{"status":"Ready","interfaces":[{"ipAddresses":["172.30.0.254"]}]}`, true},
		{"missing lease", `{"status":"Ready","interfaces":[{"ipAddresses":[]}]}`, false},
		{"gateway", `{"status":"Ready","interfaces":[{"ipAddresses":["172.30.0.1"]}]}`, false},
		{"broadcast", `{"status":"Ready","interfaces":[{"ipAddresses":["172.30.0.255"]}]}`, false},
		{"foreign", `{"status":"Ready","interfaces":[{"ipAddresses":["192.168.64.2"]}]}`, false},
		{"malformed", `{"status":"Ready","interfaces":[{"ipAddresses":["not-an-ip"]}]}`, false},
		{"duplicate", `{"status":"Ready","interfaces":[{"ipAddresses":["172.30.0.2","172.30.0.2"]}]}`, false},
		{"extra interface", `{"status":"Ready","interfaces":[{"ipAddresses":["172.30.0.2"]},{"ipAddresses":["172.30.0.3"]}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := linuxNativeBridge()
			attempt := nativeSiteAttempt{boot: boot}
			network, err := attempt.selectBootNetwork(original, disk)
			if err != nil {
				t.Fatal(err)
			}
			if original.dhcp || !network.dhcp || network != attempt.bridge || network.prefix != original.prefix || network.gateway != original.gateway || network.last != original.last {
				t.Fatal("site boot changed bridge bounds or failed to share its address mode")
			}
			var row nativeInstance
			if err := json.Unmarshal([]byte(tc.reply), &row); err != nil {
				t.Fatal(err)
			}
			_, err = nativePrimaryAddress(row, network)
			if (err == nil) != tc.valid {
				t.Fatalf("Ready response admission: %v", err)
			}
			// Cancellation stops before bootstrap work, after setup's real address
			// check. A valid lease must reach that next owner unchanged.
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = attempt.setup(ctx, Runner{}, row, "", "", nil)
			if tc.valid != errors.Is(err, context.Canceled) {
				t.Fatalf("setup address admission: %v", err)
			}
			if tc.valid {
				if _, err := nativePrimaryAddress(row, original); err == nil {
					t.Fatal("standalone Linux static guest accepted a singleton lease")
				}
			}
		})
	}
}

func TestNativeSiteBootNetworkRejectsMissingOrStaleSelection(t *testing.T) {
	const disk = "http://172.30.0.1:18082/disk.qcow2"
	boot, err := projectNativeSiteBoot(t.Context(), nativeMachineImageBootTemplate, disk)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		boot *nativeSiteBoot
		url  string
	}{
		{"missing", nil, disk},
		{"empty", &nativeSiteBoot{}, disk},
		{"stale", boot, disk + ".other"},
		{"missing disk", boot, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := linuxNativeBridge()
			attempt := nativeSiteAttempt{bridge: original, boot: tc.boot}
			if _, err := attempt.selectBootNetwork(original, tc.url); err == nil || attempt.bridge != original {
				t.Fatal("invalid boot selection changed site address mode")
			}
		})
	}
}

// These unmodified Apache-2.0 NVKE fixtures are from commit
// 890784eba904ef0da6aa9d8cc3d7bd491b2ee32e, controller/test/fixtures/releases/.
// Keep both architectures: their console/kernel settings belong to NVKE.
func TestNativeSiteBootUsesCurrentNVKESourceFixtures(t *testing.T) {
	const sourceURL = "http://127.0.0.1:18080/files/images/nvke-node.qcow2"
	const disk = "http://172.30.0.1:18082/disk.qcow2"
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "nvke-machineimage-"+arch+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var image struct {
				Spec struct {
					Architecture string `yaml:"architecture"`
					NICo         struct {
						Script string `yaml:"ipxeScript"`
					} `yaml:"nico"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal(raw, &image); err != nil {
				t.Fatal(err)
			}
			if image.Spec.Architecture != arch || strings.Count(image.Spec.NICo.Script, sourceURL) != 1 {
				t.Fatal("fixture does not carry its source architecture and single local image slot")
			}
			boot, err := projectNativeSiteBoot(t.Context(), image.Spec.NICo.Script, disk)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(image.Spec.NICo.Script, sourceURL, disk, 1)
			want = strings.Replace(want, "{{ .DiskSelectionStrategy }}", "smallest", 1)
			if boot.script != want || boot.diskURL != disk {
				t.Fatal("boot projection changed unrelated source bytes or lost the selected local disk")
			}
		})
	}
}
