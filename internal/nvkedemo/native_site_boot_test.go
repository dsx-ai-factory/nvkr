// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
