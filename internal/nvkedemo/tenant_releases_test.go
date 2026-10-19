// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func tenantProfilesFixture(t *testing.T, root, network string) *tenantProfilesBinding {
	t.Helper()
	refs := map[string]string{"profiles": "example.invalid/profiles@sha256:" + strings.Repeat("a", 64), "cwe": "example.invalid/controller@sha256:" + strings.Repeat("b", 64)}
	binding, err := bindTenantProfiles(refs, map[string]any{"profiles": map[string]string{"podUID": "profiles-uid", "imageID": refs["profiles"], "path": "/profiles"}})
	if err != nil {
		t.Fatal(err)
	}
	empty := json.RawMessage("{}")
	exit := 0
	var pod sitePod
	pod.Metadata = podMetadata{UID: "profiles-uid", Name: "profiles-pod", Namespace: installerControllerName}
	pod.Spec.Volumes = []profileVolume{{Name: "profiles", EmptyDir: &empty}}
	pod.Spec.InitContainers = []podContainer{{Name: "materialize-profiles", Image: refs["profiles"], VolumeMounts: []profileMount{{"profiles", "/profiles-out", false}}}}
	pod.Spec.Containers = []podContainer{{Name: installerControllerName, Image: refs["cwe"], VolumeMounts: []profileMount{{"profiles", "/profiles", true}}}}
	pod.Status.InitContainerStatuses = []podContainerState{{Name: "materialize-profiles", ImageID: refs["profiles"]}}
	pod.Status.InitContainerStatuses[0].State.Terminated = &struct{ ExitCode *int }{&exit}
	pod.Status.ContainerStatuses = []podContainerState{{Name: installerControllerName, ImageID: refs["cwe"], Ready: true}}
	pod.Status.ContainerStatuses[0].State.Running = &empty
	if err := privateJSON(filepath.Join(root, "profile-pod.json"), podList{Items: []sitePod{pod}}); err != nil {
		t.Fatal(err)
	}
	pod.Metadata.UID = "replacement-uid"
	if err := privateJSON(filepath.Join(root, "profile-replaced.json"), podList{Items: []sitePod{pod}}); err != nil {
		t.Fatal(err)
	}
	metadata, ipxe, userData := tenantReleaseFixtureBytes()
	if network == "upstream-ovn" {
		ipxe = []byte(strings.ReplaceAll(string(ipxe), DiskOutputName, "nvke-node-image-upstream-ovn-arm64"))
	}
	for path, raw := range map[string][]byte{"profile-metadata.yaml": metadata, "profile-ipxe": ipxe, "profile-userdata": userData} {
		writeTenantFixture(t, filepath.Join(root, path), raw, 0o600)
	}
	return binding
}

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

func TestTenantProfilesRequireUnchangedLiveObservation(t *testing.T) {
	for _, scenario := range []string{"exact", "pod replaced", "controller replaced", "controller not ready", "init replaced", "binding absent"} {
		t.Run(scenario, func(t *testing.T) {
			root := privateTestDir(t)
			binding := tenantProfilesFixture(t, root, "kind-calico")
			raw, err := os.ReadFile(filepath.Join(root, "profile-pod.json"))
			if err != nil {
				t.Fatal(err)
			}
			var pods podList
			if err := json.Unmarshal(raw, &pods); err != nil {
				t.Fatal(err)
			}
			pod := pods.Items[0]
			switch scenario {
			case "pod replaced":
				pod.Metadata.UID = "other-pod"
			case "controller replaced":
				pod.Spec.Containers[0].Image = "other-image"
				pod.Status.ContainerStatuses[0].ImageID = "other-image"
			case "controller not ready":
				pod.Status.ContainerStatuses[0].Ready = false
			case "init replaced":
				pod.Status.InitContainerStatuses[0].ImageID = "other-image"
			case "binding absent":
				binding = nil
			}
			err = binding.validate(pod)
			if scenario == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
				t.Fatalf("changed observation admitted: %v", err)
			}
		})
	}
}

func TestTenantReleasesMustCompleteBeforeClusterCreation(t *testing.T) {
	for _, failure := range []string{"machineimage", "clusterrelease", "nodepoolrelease", "profile-replaced", "binding-absent"} {
		t.Run(failure, func(t *testing.T) {
			selected, root := tenantRunFixture(t, "kind-calico", failure, "")
			if failure == "binding-absent" {
				selected.session.tenantProfiles = nil
			}
			err := selected.run(t.Context(), time.Second)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || selected.intent {
				t.Fatalf("failed release preparation admitted tenant: %v", err)
			}
			for _, path := range []string{filepath.Join(root, "created"), filepath.Join(selected.session.Work, "tenant-request.json")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed preparation left cluster intent: %v", err)
				}
			}
			if failure == "profile-replaced" || failure == "binding-absent" {
				if _, err := os.Stat(filepath.Join(root, "events")); !os.IsNotExist(err) {
					t.Fatal("substituted pod created release resources")
				}
			}
		})
	}
}
