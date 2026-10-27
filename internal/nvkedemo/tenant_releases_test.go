// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func tenantReleaseFixtureBytes() ([]byte, []byte, []byte) {
	return []byte("profiles:\n  " + TenantProfile + ":\n    kubernetes_version: \"\"\n    kubernetes_version_injected: true\n    architecture: arm64\n    build_date: \"2026-10-08\"\n    ubuntu_version: \"26.04\"\n    variant: local-plain-tpm\n"),
		[]byte("#!ipxe\nset image-url http://" + GuestGateway + ":18080/" + DiskOutputName + ".qcow2\nboot\n"),
		[]byte("#cloud-config\nhostname: {{ .Hostname }}\n")
}

func TestTenantReleaseDocumentsBindDiskAndPreserveSourceTemplate(t *testing.T) {
	for _, scenario := range []string{"exact", "upstream exact", "unknown network", "oversized ipxe", "wrong digest", "wrong payload", "wrong output", "wrong arch", "encrypted", "no disk", "wrong URL", "duplicate URL", "foreign metadata", "extra document", "empty template", "oversized template", "invalid UTF-8"} {
		t.Run(scenario, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("1", 64)
			image := lifecycle.Artifact{Name: DiskOutputName, Kind: "disk-image", OS: "linux", Arch: "arm64", Digest: digest, Disk: &delivery.DiskOutput{PayloadDigest: digest, Size: 12345, Format: "qcow2", Encryption: "none"}}
			metadata, ipxe, userData := tenantReleaseFixtureBytes()
			network := "kind-calico"
			switch scenario {
			case "upstream exact":
				network = "upstream-ovn"
				image.Name = "nvke-node-image-upstream-ovn-arm64"
				ipxe = []byte(strings.ReplaceAll(string(ipxe), DiskOutputName, image.Name))
			case "unknown network":
				network = "foreign"
			case "oversized ipxe":
				ipxe = append(ipxe, []byte(strings.Repeat("#", tenantIPXELimit))...)
			case "wrong digest":
				image.Digest = "not-a-digest"
			case "wrong payload":
				image.Disk.PayloadDigest = "sha256:" + strings.Repeat("2", 64)
			case "wrong output":
				image.Name = "foreign"
			case "wrong arch":
				image.Arch = "amd64"
			case "encrypted":
				image.Disk.Encryption = "luks2"
			case "no disk":
				image.Disk = nil
			case "wrong URL":
				ipxe = []byte(strings.ReplaceAll(string(ipxe), GuestGateway, "example.invalid"))
			case "duplicate URL":
				ipxe = append(ipxe, ipxe...)
			case "foreign metadata":
				metadata = []byte(strings.ReplaceAll(string(metadata), "arm64", "amd64"))
			case "extra document":
				metadata = append(metadata, []byte("---\n{}\n")...)
			case "empty template":
				userData = nil
			case "oversized template":
				userData = append(userData, []byte(strings.Repeat("#", tenantUserDataLimit))...)
			case "invalid UTF-8":
				userData = append(userData, 0xff)
			}
			name, documents, err := tenantReleaseDocuments(network, image, metadata, ipxe, userData)
			if scenario != "exact" && scenario != "upstream exact" {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || name != "" || documents != nil {
					t.Fatalf("invalid release input produced documents: %s %+v %v", name, documents, err)
				}
				return
			}
			if err != nil || name != "nvkr-local-"+strings.Repeat("1", 40) || len(documents) != 3 {
				t.Fatalf("disk-bound release documents: %s %+v %v", name, documents, err)
			}
			for i, document := range documents {
				if document.Metadata.Name != name || document.Metadata.Annotations["nvkr.dev/diagnostic-disk-digest"] != digest {
					t.Fatal("document lost exact disk binding")
				}
				spec := document.Spec.(map[string]any)
				if i == 0 {
					if spec["nico"].(map[string]string)["ipxeScript"] != string(ipxe) {
						t.Fatal("iPXE source bytes changed")
					}
				} else if spec["machineImageRef"].(map[string]string)["name"] != name || spec["machineInit"].(map[string]string)["userDataTemplate"] != string(userData) {
					t.Fatal("release lost machine-image reference or source template bytes")
				}
			}
		})
	}
}
