// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestNativeIdentityAuthoredEnvironmentIsTheExactGuestInput(t *testing.T) {
	for _, tc := range []struct{ name, gateway string }{{"first admitted network", "172.30.0.1"}, {"another admitted network", "192.168.90.1"}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := startupDescription()
			for i := range cfg.Assets {
				cfg.Assets[i].URL = strings.Replace(cfg.Assets[i].URL, cfg.Gateway, tc.gateway, 1)
			}
			cfg.Gateway = tc.gateway
			raw, err := SiteStartupUserData(startupTestContext(t), cfg)
			if err != nil {
				t.Fatal(err)
			}
			var cloud startupCloudConfig
			if err := yaml.Unmarshal([]byte(strings.TrimPrefix(raw, "#cloud-config\n")), &cloud); err != nil {
				t.Fatal(err)
			}
			var command string
			for _, file := range cloud.WriteFiles {
				if file.Path == "/var/lib/nvke-native/prepare.sh" {
					start := strings.Index(file.Content, "printf '%s' ")
					if start < 0 {
						t.Fatal("missing shared source env renderer")
					}
					tail := file.Content[start:]
					end := strings.Index(tail, " > /etc/nke/spire.env\n")
					if end < 0 {
						t.Fatal("missing source env destination")
					}
					command = tail[:end]
				}
			}
			if command == "" {
				t.Fatal("missing owned preparation script")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			got, err := exec.CommandContext(ctx, "/bin/bash", "-c", command).Output()
			if err != nil {
				t.Fatal(err)
			}
			retained := nativeBootstrapSpireEnvironment(cfg)
			if !bytes.Equal(got, retained) || !bytes.HasSuffix(got, []byte("\n")) {
				t.Fatal("guest environment differs from retained exact authored bytes")
			}
			changed := append(append([]byte{}, got...), []byte("SPIRE_SERVER_PORT=1\n")...)
			if plainSHA(changed) == plainSHA(retained) {
				t.Fatal("modified guest environment did not change the fixed-operation input hash")
			}
		})
	}
}
func TestNativeIdentityHeldInputUsesExistingProcessOwner(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      os.FileMode
		closed    bool
		wantError bool
	}{{"held regular file", 0600, false, false}, {"writable by others", 0666, false, true}, {"closed descriptor", 0600, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, "input")
			if err := os.WriteFile(name, []byte("private exact stdin\n"), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(name, tc.mode); err != nil {
				t.Fatal(err)
			}
			input, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if tc.closed {
				_ = input.Close()
			}
			runner := Runner{Work: root, Env: NativeEnvironment(), input: input}
			raw, err := runner.Capture(t.Context(), []string{"/bin/cat"}, "identity-held-input", 5*time.Second)
			if (err != nil) != tc.wantError {
				t.Fatalf("input error=%v wantError=%v", err, tc.wantError)
			}
			if !tc.wantError && string(raw) != "private exact stdin\n" {
				t.Fatal("owned descriptor bytes changed")
			}
		})
	}
}
func TestNativeIdentityContextRequiresExactArtifactPodAndCSI(t *testing.T) {
	ref := "registry.example.test/controller@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name      string
		change    func(*nativeIdentityPodList)
		wantError bool
	}{{name: "exact held container"}, {"different image", func(v *nativeIdentityPodList) { v.Items[0].Status.ContainerStatuses[0].ImageID = "other" }, true}, {"missing UID", func(v *nativeIdentityPodList) { v.Items[0].Metadata.UID = "" }, true}, {"missing runtime container identity", func(v *nativeIdentityPodList) { v.Items[0].Status.ContainerStatuses[0].ContainerID = "" }, true}, {"duplicate selected Pod", func(v *nativeIdentityPodList) { v.Items = append(v.Items, v.Items[0]) }, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var rows nativeIdentityPodList
			raw := []byte(`{"items":[{"metadata":{"name":"controller-fixture","namespace":"site-fixture","uid":"fixture-uid"},"spec":{"containers":[{"name":"controller","image":"` + ref + `"}]},"status":{"containerStatuses":[{"name":"controller","imageID":"` + ref + `","containerID":"containerd://fixture","ready":true,"state":{"running":{}}}]}}]}`)
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&rows)
			}
			_, err := nativeIdentitySelectedPod(rows, ref)
			if (err != nil) != tc.wantError {
				t.Fatalf("pod error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
	for _, tc := range []struct {
		name      string
		readOnly  bool
		driver    string
		wantError bool
	}{{"admitted CSI", true, "csi.spiffe.io", false}, {"writable mount", false, "csi.spiffe.io", true}, {"other driver", true, "other.example.test", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var pod nativeIdentityPodDocument
			pod.Spec.ServiceAccountName = "fixture-service"
			pod.Spec.Containers = []nativeIdentityContainer{{podContainer: podContainer{Name: "fixture"}, VolumeMounts: []nativeIdentityMount{{profileMount: profileMount{Name: "identity", MountPath: "/spiffe-workload-api", ReadOnly: tc.readOnly}}}}}
			volume := nativeIdentityVolume{profileVolume: profileVolume{Name: "identity"}}
			volume.CSI = &struct {
				Driver   string
				ReadOnly bool
			}{tc.driver, true}
			pod.Spec.Volumes = []nativeIdentityVolume{volume}
			err := nativeIdentityCSI(nativeIdentityPod{pod: pod, container: pod.Spec.Containers[0]})
			if (err != nil) != tc.wantError {
				t.Fatalf("CSI error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}
func TestNativeIdentityConfigProjectionCannotSelectAnotherTransport(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		wantError   bool
	}{{"SPIFFE-only", "", false}, {"TLS JWT", "nodeAgent:\n  tlsJWT:\n    enabled: true\n", true}, {"external MTLS", "nodeAgent:\n  externalMTLSEnabled: true\n", true}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "spire:\n  className: fixture-class\n  serverNamespace: fixture-spire\n  tpmPubhashesConfigMapName: fixture-map\n  serverPort: '32123'\n" + tc.extra
			_, err := nativeIdentityConfigProjection(raw)
			if (err != nil) != tc.wantError {
				t.Fatalf("config error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}
