// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeEnvironmentExcludesForeignTargets(t *testing.T) {
	for key, value := range map[string]string{"DOCKER_CONTEXT": "foreign", "DOCKER_HOST": "tcp://foreign", "KUBECONFIG": "foreign", "GIT_CONFIG_COUNT": "1", "PYTHONPATH": "foreign", "HOME": "foreign", "CAPI_VERSION": "foreign"} {
		t.Setenv(key, value)
	}
	env := NativeEnvironment()
	for _, key := range []string{"DOCKER_CONTEXT", "KUBECONFIG", "GIT_CONFIG_COUNT", "PYTHONPATH", "HOME", "CAPI_VERSION"} {
		if environmentValue(env, key) != "" {
			t.Fatalf("inherited %s", key)
		}
	}
	if environmentValue(env, "DOCKER_HOST") != "unix:///var/run/docker.sock" || environmentValue(env, "KIND_EXPERIMENTAL_PROVIDER") != "docker" {
		t.Fatal("foreign Docker target")
	}
	updated := SetEnv(env, map[string]string{"HOME": "private", "PATH": "/private/tools"})
	if environmentValue(updated, "HOME") != "private" || environmentValue(env, "HOME") != "" {
		t.Fatal("environment mutation")
	}
	if !slices.IsSorted(updated) {
		t.Fatal("environment not sorted")
	}
}

func TestManagementKindImageAdmissionDiagnostics(t *testing.T) {
	for _, row := range []struct {
		name, id, arch string
		exit           int
	}{
		{name: "cached", id: kindNodeID, arch: "arm64"},
		{name: "missing", exit: 1},
		{name: "docker-unavailable", exit: 42},
		{name: "wrong-image", id: "foreign-id", arch: "arm64"},
	} {
		t.Run(row.name, func(t *testing.T) {
			work := privateTestDir(t)
			script := "#!/bin/sh\n[ \"$1 $2\" = 'image inspect' ] && [ \"$3\" = \"$PIN\" ] || exit 99\nprintf '%s\\n' 'private Docker diagnostic' >&2\nprintf '%s\\n' \"$IMAGE\"\nexit \"$EXIT\"\n"
			if err := os.WriteFile(filepath.Join(work, "docker"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal([]dockerImage{{ID: row.id, Architecture: row.arch}})
			if err != nil {
				t.Fatal(err)
			}
			runner := Runner{Work: work, Env: []string{"PATH=" + work + ":/usr/bin:/bin", "PIN=" + kindNodeImage, "IMAGE=" + string(raw), "EXIT=" + strconv.Itoa(row.exit)}}
			err = admitKindImage(t.Context(), runner)
			if row.name == "cached" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "cache") || !strings.Contains(err.Error(), kindNodeImage) || !strings.Contains(err.Error(), "docs/local-nvke-demo.md#4-build-and-run-the-demo") || strings.Contains(err.Error(), "private Docker diagnostic") {
				t.Fatalf("cache prerequisite diagnostic=%v", err)
			}
			if row.exit != 0 {
				var command *CommandError
				if !errors.As(err, &command) || command.Phase != "admit-kind-image" || command.ExitCode != row.exit || !strings.Contains(err.Error(), "could not inspect") {
					t.Fatalf("inspection cause=%v", err)
				}
			}
		})
	}
}

func TestManagementConfigProjectionAdmitsExactSourceAndUsesIPv4(t *testing.T) {
	source, err := os.ReadFile("testdata/pinned-kind.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if plainSHA(source) != kindConfigHash {
		t.Fatal("fixture differs from pinned NVKE source")
	}
	before := string(source)
	for _, name := range []string{"exact", "already-projected", "missing-family", "duplicate-family", "missing-socket", "changed-image", "trailing-content"} {
		t.Run(name, func(t *testing.T) {
			input := before
			switch name {
			case "already-projected":
				input = strings.Replace(input, sourceKindIPFamily, "  ipFamily: ipv4\n", 1)
			case "missing-family":
				input = strings.Replace(input, sourceKindIPFamily, "", 1)
			case "duplicate-family":
				input = strings.Replace(input, sourceKindIPFamily, sourceKindIPFamily+sourceKindIPFamily, 1)
			case "missing-socket":
				input = strings.Replace(input, socketMount, "", 1)
			case "changed-image":
				input = strings.Replace(input, kindNodeImage, "kindest/node:latest", 1)
			case "trailing-content":
				input += "---\n"
			}
			projected, projectErr := projectManagementConfig([]byte(input))
			if name != "exact" {
				if projectErr == nil || projected != nil {
					t.Fatal("changed source accepted or partial projection returned", projectErr)
				}
				return
			}
			want := strings.Replace(strings.Replace(before, socketMount, "", 1), sourceKindIPFamily, "  ipFamily: ipv4\n  disableDefaultCNI: true\n  kubeProxyMode: none\n  podSubnet: "+managementPodCIDR+"\n  serviceSubnet: "+managementServiceCIDR+"\n", 1)
			if projectErr != nil || string(projected) != want || string(source) != before || !strings.Contains(string(projected), kindNodeImage) {
				t.Fatal("projection differs from the admitted IPv4 OVN topology", projectErr)
			}
		})
	}
}

func TestManagementTemporaryKindProjection(t *testing.T) {
	source, err := os.ReadFile("testdata/pinned-kind.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"kind-calico", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			projected, err := projectManagementConfigForNetwork(source, mode)
			if mode == "invalid" {
				if err == nil || projected != nil {
					t.Fatal("unknown network accepted", err)
				}
				return
			}
			want := strings.Replace(strings.Replace(string(source), socketMount, "", 1), sourceKindIPFamily, "  ipFamily: ipv4\n  podSubnet: "+managementPodCIDR+"\n  serviceSubnet: "+managementServiceCIDR+"\n", 1)
			if err != nil || string(projected) != want {
				t.Fatal("Kind defaults not preserved", err)
			}
		})
	}
}

func TestManagementUpstreamOVNDisablesKindCNI(t *testing.T) {
	source, err := os.ReadFile("testdata/pinned-kind.yaml")
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := projectManagementConfigForNetwork(source, "upstream-ovn")
	if err != nil {
		t.Fatal(err)
	}
	maintained, err := projectManagementConfig(source)
	if err != nil || string(upstream) != string(maintained) {
		t.Fatal("explicit OVN options must preserve the admitted topology", err)
	}
}

func TestManagementRejectsUnsupportedHostBeforeOutput(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Skip("unsupported-platform case does not apply")
	}
	out := filepath.Join(t.TempDir(), "new")
	_, err := admitManagement(context.Background(), Config{Out: out})
	if err == nil {
		t.Fatal("accepted unsupported host")
	}
	if _, err = os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created output before admission")
	}
}

func TestOwnedManagementDescriptionRejectsSubstitution(t *testing.T) {
	for _, name := range []string{"exact", "missing", "duplicate", "foreignID", "role", "image", "name", "cluster"} {
		t.Run(name, func(t *testing.T) {
			row := dockerContainer{ID: "captured-id", Name: "/nvkr-test-control-plane", Image: kindNodeID}
			row.Config.Labels = map[string]string{"io.x-k8s.kind.cluster": "nvkr-test", "io.x-k8s.kind.role": "control-plane"}
			switch name {
			case "foreignID":
				row.ID = "foreign"
			case "role":
				row.Config.Labels["io.x-k8s.kind.role"] = "worker"
			case "image":
				row.Image = "foreign"
			case "name":
				row.Name = "/shared-control-plane"
			case "cluster":
				row.Config.Labels["io.x-k8s.kind.cluster"] = "foreign"
			}
			rows := []dockerContainer{row}
			if name == "missing" {
				rows = nil
			}
			if name == "duplicate" {
				rows = append(rows, row)
			}
			err := validateOwnedNodes(rows, []string{"captured-id"}, "nvkr-test")
			if (err == nil) != (name == "exact") {
				t.Fatalf("ownership result:%v", err)
			}
		})
	}
}

func TestRegularProducerMembershipRejectsSubstitution(t *testing.T) {
	for _, name := range []string{"exact", "different", "duplicate", "symlink", "packagesSymlink"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			layout := filepath.Join(base, "packages", "tool")
			if err := os.MkdirAll(layout, 0o700); err != nil {
				t.Fatal(err)
			}
			digest := "sha256:" + strings.Repeat("a", 64)
			digests := []string{digest}
			if name == "different" {
				digests[0] = "sha256:" + strings.Repeat("b", 64)
			}
			if name == "duplicate" {
				digests = append(digests, digest)
			}
			manifests := make([]map[string]string, 0)
			for _, pin := range digests {
				manifests = append(manifests, map[string]string{"digest": pin})
			}
			raw, _ := json.Marshal(map[string]any{"manifests": manifests})
			if err := os.WriteFile(filepath.Join(layout, "index.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "symlink" {
				if err := os.Symlink(filepath.Join(layout, "index.json"), filepath.Join(layout, "foreign")); err != nil {
					t.Fatal(err)
				}
			}
			if name == "packagesSymlink" {
				moved := filepath.Join(base, "external")
				if err := os.Rename(filepath.Join(base, "packages"), moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, filepath.Join(base, "packages")); err != nil {
					t.Fatal(err)
				}
			}
			err := diagnosticIndexMatches(layout, digest)
			if (err == nil) != (name == "exact") {
				t.Fatalf("index acceptance:%v", err)
			}
		})
	}
}

func TestPrivateWriteNeverReplaces(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "value")
	if err := privateWrite(path, []byte("accepted")); err != nil {
		t.Fatal(err)
	}
	if err := privateWrite(path, []byte("foreign")); err == nil {
		t.Fatal("replaced file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "accepted" {
		t.Fatal("accepted bytes changed")
	}
}

func TestCleanupAfterJoinedWorkPreservesFailureAndRetention(t *testing.T) {
	ordinary := errors.New("failed installation")
	cleanupFailure := errors.New("cleanup failed")
	for _, name := range []string{"success", "ordinary", "canceled", "unjoined", "retained", "cleanupFailure"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var primary error
			switch name {
			case "ordinary", "cleanupFailure":
				primary = ordinary
			case "canceled":
				cancel()
				primary = context.Canceled
			case "unjoined":
				primary = errors.Join(ordinary, ErrUnjoined)
			case "retained":
				primary = errors.Join(ordinary, ErrRetainedControls)
			}
			called := false
			err := cleanupAfterJoinedWork(ctx, primary, time.Second, func(cleanupCtx context.Context) error {
				called = true
				if cleanupCtx.Err() != nil {
					t.Fatal("cleanup inherited canceled context")
				}
				if _, bounded := cleanupCtx.Deadline(); !bounded {
					t.Fatal("unbounded cleanup")
				}
				if name == "cleanupFailure" {
					return cleanupFailure
				}
				return nil
			})
			if called != (name != "unjoined" && name != "retained") {
				t.Fatal("cleanup retention changed")
			}
			if primary != nil && !errors.Is(err, primary) {
				t.Fatalf("lost primary failure: %v", err)
			}
			if name == "success" && err != nil {
				t.Fatal(err)
			}
			if name == "cleanupFailure" && !errors.Is(err, cleanupFailure) {
				t.Fatal("lost cleanup failure")
			}
		})
	}
}

func TestManagementVolumeOwnershipRejectsForeignOrSharedStorage(t *testing.T) {
	for _, name := range []string{"exact", "predates", "shared", "named", "destination", "extra", "foreignID", "missingVar", "readonly"} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			row := dockerContainer{ID: "exact-id", Mounts: []dockerMount{{Type: "volume", Name: "owned-volume", Destination: "/var", Driver: "local", RW: true}}}
			row.Config.Volumes = map[string]json.RawMessage{"/var": json.RawMessage("{}")}
			volume := dockerVolume{Name: "owned-volume", Driver: "local", Labels: map[string]string{"com.docker.volume.anonymous": ""}}
			existing := make(map[string]bool)
			consumer := "exact-id"
			switch name {
			case "predates":
				existing["owned-volume"] = true
			case "shared":
				consumer = "foreign-id"
			case "named":
				volume.Labels = nil
			case "destination":
				row.Mounts[0].Destination = "/foreign"
			case "extra":
				row.Mounts = append(row.Mounts, row.Mounts[0])
			case "foreignID":
				row.ID = "foreign-id"
			case "missingVar":
				row.Config.Volumes = nil
			case "readonly":
				row.Mounts[0].RW = false
			}
			for path, value := range map[string]any{"nodes.json": []dockerContainer{row}, "volume.json": []dockerVolume{volume}} {
				if err := privateJSON(filepath.Join(work, path), value); err != nil {
					t.Fatal(err)
				}
			}
			script := "#!/bin/sh\ncase \"$1\" in\n inspect) /bin/cat \"$FIXTURE/nodes.json\";;\n volume) /bin/cat \"$FIXTURE/volume.json\";;\n ps) printf '%s\\n' \"$CONSUMER\";;\n *) exit 42;;\nesac\n"
			if err := os.WriteFile(filepath.Join(work, "docker"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			volumes, err := managementVolumes(context.Background(), Runner{Env: []string{"PATH=" + work + ":/usr/bin:/bin", "FIXTURE=" + work, "CONSUMER=" + consumer}}, []string{"exact-id"}, existing)
			if name == "exact" {
				if err != nil || !slices.Equal(volumes, []string{"owned-volume"}) {
					t.Fatalf("ownership:%v %v", volumes, err)
				}
			} else if err == nil {
				t.Fatal("foreign storage accepted")
			}
		})
	}
}
