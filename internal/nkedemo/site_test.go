// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

func TestSiteInstallerFullCheckoutRealGit(t *testing.T) {
	for _, scenario := range []string{"pinned", "canceled", "missing-pin", "sparse-regression", "delegated-worktree"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			source, checkout := filepath.Join(work, "source"), filepath.Join(work, "installer")
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			git := func(dir string, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
				cmd.WaitDelay = time.Second
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
					"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test"}
				raw, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("real fixture Git %v: %v: %s", args, err, raw)
				}
				return strings.TrimSpace(string(raw))
			}
			git(source, "init", "-q")
			members := []string{"deploy/input", "make/input", "controller/deploy/charts/input", "controller/deploy/k8s/capi-spiffe-exec-auth/input", "controller/hack/ci/input", "cli/previously-excluded", "README.md"}
			for _, name := range members {
				path := filepath.Join(source, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("pinned bytes\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			git(source, "add", ".")
			git(source, "-c", "commit.gpgsign=false", "commit", "-qm", "pin")
			pin := git(source, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(source, members[0]), []byte("newer source bytes\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(source, "add", ".")
			git(source, "-c", "commit.gpgsign=false", "commit", "-qm", "newer source")
			original := make(map[string][]byte)
			for _, name := range append(append([]string{}, members...), ".git/config", ".git/index", ".git/HEAD") {
				raw, err := os.ReadFile(filepath.Join(source, name))
				if err != nil {
					t.Fatal(err)
				}
				original[name] = raw
			}
			t.Cleanup(func() {
				for name, want := range original {
					raw, err := os.ReadFile(filepath.Join(source, name))
					if err != nil || !bytes.Equal(raw, want) {
						t.Fatalf("source checkout mutated at %s: %v", name, err)
					}
				}
			})
			runner := Runner{Work: work, Env: []string{"PATH=" + os.Getenv("PATH")}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			if scenario == "sparse-regression" {
				if err := runner.Run(ctx, []string{"git", "clone", "--no-hardlinks", "--no-checkout", source, checkout}, "clone-installer", time.Minute); err != nil {
					t.Fatal(err)
				}
				if err := runner.Run(ctx, []string{"git", "-C", checkout, "sparse-checkout", "set", "deploy", "make", "controller/deploy/charts", "controller/deploy/k8s/capi-spiffe-exec-auth", "controller/hack/ci"}, "installer-membership", time.Minute); err != nil {
					t.Fatal(err)
				}
				if git(checkout, "config", "--local", "--get", "extensions.worktreeConfig") != "true" {
					t.Fatal("sparse fixture did not reproduce delegated configuration")
				}
				err := runner.Run(ctx, []string{"git", "-C", checkout, "checkout", "--detach", pin}, "checkout-installer", time.Minute)
				var coded *nkderr.Error
				if !errors.As(err, &coded) || coded.Code != nkderr.CodeEngineCapability {
					t.Fatalf("sparse checkout no longer reproduces coded rejection: %v", err)
				}
				return
			}
			selected := pin
			if scenario == "missing-pin" {
				selected = strings.Repeat("0", 40)
			}
			err := checkoutSiteInstaller(ctx, runner, source, checkout, selected)
			if scenario == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("checkout cancellation cause=%v", err)
				}
				if _, statErr := os.Stat(checkout); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("canceled checkout created destination", statErr)
				}
				return
			}
			if scenario == "missing-pin" {
				var command *CommandError
				if !errors.As(err, &command) || command.Phase != "checkout-installer" || command.ExitCode != 128 {
					t.Fatalf("unpinned checkout error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range members {
				raw, err := os.ReadFile(filepath.Join(checkout, name))
				if err != nil || string(raw) != "pinned bytes\n" {
					t.Fatalf("full pinned member %s=%q: %v", name, raw, err)
				}
			}
			if _, err := os.Stat(filepath.Join(checkout, ".git/config.worktree")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("full checkout created delegated configuration", err)
			}
			if scenario == "delegated-worktree" {
				git(checkout, "config", "extensions.worktreeConfig", "true")
			}
			raw, err := runner.Capture(ctx, []string{"git", "-C", checkout, "status", "--porcelain=v1"}, "installer-status", time.Minute)
			if scenario == "delegated-worktree" {
				var coded *nkderr.Error
				if !errors.As(err, &coded) || coded.Code != nkderr.CodeEngineCapability {
					t.Fatalf("delegated config policy relaxed: %v", err)
				}
			} else if err != nil || len(raw) != 0 || git(checkout, "rev-parse", "HEAD") != pin {
				t.Fatalf("full pinned checkout not re-admitted: %q %v", raw, err)
			}
		})
	}
}

func TestSiteEndpointAdmission(t *testing.T) {
	for _, value := range []string{"http://172.18.0.1:18089", "http://127.0.0.1:18089", "http://0.0.0.0:18089", "https://172.18.0.1:18089", "http://172.18.0.1:18080", "http://user:password@172.18.0.1:18089", "http://172.18.0.1:18089/private", "http://example.com:18089", "http://8.8.8.8:18089", "http://172.18.0.1:18089?x=1", "http://[::1]:18089"} {
		t.Run(value, func(t *testing.T) {
			_, err := validateEndpoint(value)
			if (err == nil) != (value == "http://172.18.0.1:18089") {
				t.Fatalf("endpoint:%v", err)
			}
		})
	}
}

func TestProfilesImageValuesSelectExactDigest(t *testing.T) {
	reference := "example.invalid/profiles@sha256:" + strings.Repeat("a", 64)
	values := profilesImageValues(reference)
	profiles := values["profiles"].(map[string]any)
	image := profiles["image"].(map[string]any)
	if profiles["path"] != "/profiles" || image["enabled"] != true ||
		image["repository"] != "example.invalid/profiles" || image["digest"] != "sha256:"+strings.Repeat("a", 64) || image["tag"] != "" {
		t.Fatal("profiles must select the exact image through the chart's standard init container")
	}
	if _, found := values["volumes"]; found {
		t.Fatal("profiles must not inject a hostPath")
	}
}

// Optional source-owned consumer check. Supply a chart exported from the exact
// InstallerCommit; this check renders it but creates no Kubernetes resources.
func TestProfilesImageSourceChart(t *testing.T) {
	chart := os.Getenv("NKD_DEMO_PROFILE_CHART")
	if chart == "" {
		t.Skip("set NKD_DEMO_PROFILE_CHART to the pinned NKE controller chart")
	}
	reference := "example.invalid/profiles@sha256:" + strings.Repeat("a", 64)
	raw, err := json.Marshal(profilesImageValues(reference))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := []string{"template", "controller", chart, "--namespace", "nke-controller"}
	if overlay := os.Getenv("NKD_DEMO_PROFILE_OVERLAY"); overlay != "" {
		args = append(args, "-f", overlay)
	}
	args = append(args, "-f", path)
	// Stage 05 applies describe-site's generated values after release values.
	// A real source-reader export can exercise that same precedence without
	// deploying; the default probes the legacy field's repository/digest shape.
	if generated := os.Getenv("NKD_DEMO_PROFILE_SITE_VALUES"); generated != "" {
		args = append(args, "-f", generated)
	} else {
		args = append(args, "--set-string", "profiles.image.repository=example.invalid/profiles",
			"--set-string", "profiles.image.digest=sha256:"+strings.Repeat("a", 64))
	}
	args = append(args, "--show-only", "templates/deployment.yaml")
	command := exec.CommandContext(ctx, "helm", args...)
	command.WaitDelay = time.Second
	rendered, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("source-owned chart rejected profiles image: %v\n%s", err, rendered)
	}
	value := string(rendered)
	for _, required := range []string{"name: materialize-profiles", "cp -a /profiles/. /profiles-out/", "emptyDir: {}", "mountPath: \"/profiles\"", "readOnly: true", "name: PROFILES_DIR"} {
		if !strings.Contains(value, required) {
			t.Fatalf("missing source-owned profiles image field %q", required)
		}
	}
	if strings.Contains(value, "hostPath:") || strings.Contains(value, "/var/nkd-local-profiles") {
		t.Fatal("profiles image must not use the retired host directory")
	}
}

func TestDeployedProfilesImageRejectsSubstitution(t *testing.T) {
	for _, scenario := range []string{"exact", "api-token", "image", "image-id", "failed-init", "missing-exit", "running-init", "missing-init", "duplicate-init", "missing-status", "duplicate-status", "init-mount", "duplicate-init-mount", "masked-source", "masked-destination", "masked-root", "masked-controller", "path", "writable", "host-path", "missing-volume", "duplicate-mount"} {
		t.Run(scenario, func(t *testing.T) {
			reference := "example.invalid/profiles@sha256:" + strings.Repeat("a", 64)
			emptyDir := json.RawMessage("{}")
			exit := 0
			var pod sitePod
			pod.Spec.Volumes = []profileVolume{{Name: "profiles", EmptyDir: &emptyDir}}
			pod.Spec.InitContainers = []podContainer{{Name: "materialize-profiles", Image: reference,
				VolumeMounts: []profileMount{{"profiles", "/profiles-out", false}}}}
			pod.Spec.Containers = []podContainer{{Name: "controller", VolumeMounts: []profileMount{{"profiles", "/profiles", true}}}}
			pod.Status.InitContainerStatuses = []podContainerState{{Name: "materialize-profiles", ImageID: reference}}
			pod.Status.InitContainerStatuses[0].State.Terminated = &struct{ ExitCode *int }{&exit}
			switch scenario {
			case "api-token":
				pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts,
					profileMount{"kube-api-access-test", "/var/run/secrets/kubernetes.io/serviceaccount", true})
			case "image":
				pod.Spec.InitContainers[0].Image = "foreign:latest"
			case "image-id":
				pod.Status.InitContainerStatuses[0].ImageID = "foreign"
			case "failed-init":
				exit = 1
			case "missing-exit":
				pod.Status.InitContainerStatuses[0].State.Terminated.ExitCode = nil
			case "running-init":
				pod.Status.InitContainerStatuses[0].State.Terminated = nil
			case "missing-init":
				pod.Spec.InitContainers = nil
			case "duplicate-init":
				pod.Spec.InitContainers = append(pod.Spec.InitContainers, pod.Spec.InitContainers[0])
			case "missing-status":
				pod.Status.InitContainerStatuses = nil
			case "duplicate-status":
				pod.Status.InitContainerStatuses = append(pod.Status.InitContainerStatuses, pod.Status.InitContainerStatuses[0])
			case "init-mount":
				pod.Spec.InitContainers[0].VolumeMounts[0].MountPath = "/profiles"
			case "duplicate-init-mount":
				pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts, pod.Spec.InitContainers[0].VolumeMounts[0])
			case "masked-source":
				pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts, profileMount{"foreign", "/profiles/osProfiles", true})
			case "masked-destination":
				pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts, profileMount{"foreign", "/profiles-out/osProfiles", false})
			case "masked-root":
				pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts, profileMount{"foreign", "/", true})
			case "masked-controller":
				pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, profileMount{"foreign", "/profiles/osProfiles", true})
			case "path":
				pod.Spec.Containers[0].VolumeMounts[0].MountPath = "/different"
			case "writable":
				pod.Spec.Containers[0].VolumeMounts[0].ReadOnly = false
			case "host-path":
				pod.Spec.Volumes[0].HostPath = &struct{ Path, Type string }{"/var", "Directory"}
			case "missing-volume":
				pod.Spec.Volumes = nil
			case "duplicate-mount":
				pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, pod.Spec.Containers[0].VolumeMounts[0])
			}
			if err := validateProfilesImage(pod, "controller", reference); (err == nil) != (scenario == "exact" || scenario == "api-token") {
				t.Fatalf("profiles image acceptance: %v", err)
			}
		})
	}
}

func TestRegistryOwnershipRejectsForeignResources(t *testing.T) {
	for _, name := range []string{"exact", "owner", "image", "name", "volume", "bind", "missing", "duplicate", "foreignID"} {
		t.Run(name, func(t *testing.T) {
			row := dockerContainer{ID: "owned-id", Name: "/private-registry", Image: "accepted"}
			row.Config.Labels = map[string]string{"dev.nkd.demo.owner": "private-owner"}
			row.Mounts = []dockerMount{{Type: "tmpfs"}}
			switch name {
			case "owner":
				row.Config.Labels["dev.nkd.demo.owner"] = "foreign"
			case "image":
				row.Image = "foreign"
			case "name":
				row.Name = "/foreign"
			case "volume":
				row.Mounts[0].Type = "volume"
			case "bind":
				row.Mounts[0].Type = "bind"
			case "foreignID":
				row.ID = "foreign"
			}
			rows := []dockerContainer{row}
			if name == "missing" {
				rows = nil
			}
			if name == "duplicate" {
				rows = append(rows, row)
			}
			err := validateRegistryOwnership(rows, []string{"owned-id"}, "private-registry", "private-owner", "accepted")
			if (err == nil) != (name == "exact") {
				t.Fatalf("registry ownership:%v", err)
			}
		})
	}
}

func TestRegistryAddressRequiresOwnedKindIdentity(t *testing.T) {
	for _, scenario := range []string{"exact", "id", "name", "owner", "image", "stopped", "bind", "volume", "missing", "duplicate", "no-kind", "extra-network", "network-id", "missing-network-id", "missing-image-id", "missing-gateway", "no-address", "address", "ipv6", "public", "outside", "gateway", "subnet", "broadcast", "unmasked-subnet"} {
		t.Run(scenario, func(t *testing.T) {
			var rows []dockerContainer
			fixture := `[{"Id":"owned-id","Name":"/private-registry","Image":"accepted","Config":{"Labels":{"dev.nkd.demo.owner":"private-owner"}},"State":{"Running":true},"Mounts":[{"Type":"tmpfs"}],"NetworkSettings":{"Networks":{"kind":{"IPAddress":"172.18.0.2","NetworkID":"kind-id"}}}}]`
			if err := json.Unmarshal([]byte(fixture), &rows); err != nil {
				t.Fatal(err)
			}
			materials := &SiteMaterials{RegistryID: "accepted", KindNetworkID: "kind-id", Gateway: "172.18.0.1", KindCIDR: "172.18.0.0/16"}
			row := &rows[0]
			network := row.NetworkSettings.Networks["kind"]
			switch scenario {
			case "id":
				row.ID = "foreign"
			case "name":
				row.Name = "/foreign"
			case "owner":
				row.Config.Labels["dev.nkd.demo.owner"] = "foreign"
			case "image":
				row.Image = "foreign"
			case "stopped":
				row.State.Running = false
			case "bind", "volume":
				row.Mounts[0].Type = scenario
			case "missing":
				rows = nil
			case "duplicate":
				rows = append(rows, rows[0])
			case "no-kind":
				delete(row.NetworkSettings.Networks, "kind")
			case "extra-network":
				row.NetworkSettings.Networks["bridge"] = network
			case "network-id":
				network.NetworkID = "foreign"
			case "missing-network-id":
				materials.KindNetworkID = ""
			case "missing-image-id":
				materials.RegistryID = ""
			case "missing-gateway":
				materials.Gateway = ""
			case "no-address":
				network.IPAddress = ""
			case "address":
				network.IPAddress = "172.018.0.2"
			case "ipv6":
				network.IPAddress = "fd00::2"
			case "public":
				network.IPAddress = "8.8.8.8"
			case "outside":
				network.IPAddress = "172.19.0.2"
			case "gateway":
				network.IPAddress = materials.Gateway
			case "subnet":
				network.IPAddress = "172.18.0.0"
			case "broadcast":
				network.IPAddress = "172.18.255.255"
			case "unmasked-subnet":
				materials.KindCIDR = "172.18.0.1/16"
			}
			if scenario != "no-kind" {
				row.NetworkSettings.Networks["kind"] = network
			}
			host, err := registryAddress(rows, "owned-id", "private-registry", "private-owner", materials)
			if scenario == "exact" {
				if err != nil || host != "172.18.0.2:5000" {
					t.Fatalf("owned registry address %q: %v", host, err)
				}
			} else if err == nil || host != "" {
				t.Fatal("unadmitted registry address escaped")
			}
		})
	}
}

func TestRegistryStartRejectsIdentityBeforeNodeProbe(t *testing.T) {
	for _, scenario := range []string{"foreign-id", "missing-id", "multiple-ids", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			trace := filepath.Join(work, "commands")
			tool := filepath.Join(work, "docker")
			script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" >> \"$TRACE\"\ncase \"$1\" in\n run) printf '%s\\n' \"$START_ID\";;\n inspect) printf '%s\\n' '[{\"Id\":\"foreign\"}]';;\n *) exit 42;;\nesac\n"
			if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			id := "owned-id"
			switch scenario {
			case "missing-id":
				id = ""
			case "multiple-ids":
				id = "one two"
			}
			runner := Runner{Work: work, Env: []string{"PATH=" + work + ":/usr/bin:/bin", "TRACE=" + trace, "START_ID=" + id}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			host, err := startSiteRegistry(ctx, runner, "private-registry", "private-owner", "owned-node", &SiteMaterials{RegistryID: "accepted", KindNetworkID: "kind-id", KindCIDR: "172.18.0.0/16"})
			if err == nil || host != "" {
				t.Fatal("unadmitted registry can reach image upload")
			}
			if scenario == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
				if _, err := os.Stat(trace); !os.IsNotExist(err) {
					t.Fatal("canceled admission launched a child")
				}
				return
			}
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			commands := string(raw)
			if strings.Contains(commands, "--publish") || strings.Contains(commands, "exec\n") || strings.Contains(commands, "15501") {
				t.Fatal("registry start published a host port or probed an unadmitted node route")
			}
			for _, expected := range []string{"--network\nkind\n", "--read-only\n", "--cap-drop=ALL\n", registryImage} {
				if !strings.Contains(commands, expected) {
					t.Fatalf("registry start lost %q", expected)
				}
			}
		})
	}
}

func TestNodeRegistryProbeUsesBoundedDirectRoute(t *testing.T) {
	for _, scenario := range []string{"exact", "foreign-response", "null", "curl-failure", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			trace := filepath.Join(work, "commands")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TRACE\"\nprintf '%s' \"$RESPONSE\"\nexit \"$STATUS\"\n"
			if err := os.WriteFile(filepath.Join(work, "docker"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			response, status := "{}\n", "0"
			switch scenario {
			case "foreign-response":
				response = `{"foreign":true}`
			case "null":
				response = "null"
			case "curl-failure":
				status = "7"
			}
			runner := Runner{Work: work, Env: []string{"PATH=" + work + ":/usr/bin:/bin", "TRACE=" + trace, "RESPONSE=" + response, "STATUS=" + status}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			err := probeNodeRegistry(ctx, runner, "owned-node", "172.18.0.2:5000")
			if (err == nil) != (scenario == "exact") {
				t.Fatalf("node registry response: %v", err)
			}
			if scenario == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
				return
			}
			if scenario == "curl-failure" {
				var command *CommandError
				if !errors.As(err, &command) || command.Phase != "node-registry-connectivity" || command.ExitCode != 7 {
					t.Fatalf("lost owned node failure: %v", err)
				}
			}
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			want := "exec\nowned-node\ncurl\n--fail\n--silent\n--show-error\n--max-time\n10\nhttp://172.18.0.2:5000/v2/\n"
			if string(raw) != want {
				t.Fatalf("unexpected node probe %q", raw)
			}
		})
	}
}

func TestRegistryCapacityIsMeasuredAndBounded(t *testing.T) {
	for _, name := range []string{"exact", "oversized", "combined", "symlink", "sizeMismatch", "missingInput"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			outputs := make(map[string]SelectedOutput)
			for i, key := range imageKeys() {
				layout := filepath.Join(base, key)
				blobs := filepath.Join(layout, "blobs/sha256")
				if err := os.MkdirAll(blobs, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(blobs, "shared"), []byte("shared"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(blobs, string(rune('a'+i))), []byte("unique"), 0o600); err != nil {
					t.Fatal(err)
				}
				outputs[key] = SelectedOutput{Layout: layout}
			}
			blob := filepath.Join(outputs["cwe"].Layout, "blobs/sha256/shared")
			truncate := func(path string, n int64) {
				held, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err = held.Truncate(n); err != nil {
					t.Fatal(err)
				}
				if err = held.Close(); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "oversized":
				truncate(blob, registryBytes+1)
			case "combined":
				for _, key := range []string{"cwe", "provider"} {
					truncate(filepath.Join(outputs[key].Layout, "blobs/sha256/large"), registryBytes/2)
				}
			case "symlink":
				if err := os.Symlink(blob, filepath.Join(filepath.Dir(blob), "link")); err != nil {
					t.Fatal(err)
				}
			case "sizeMismatch":
				if err := os.WriteFile(blob, []byte("changed-size"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missingInput":
				delete(outputs, "provider")
			}
			bytes, err := registryCapacity(outputs)
			if name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				expected := int64(64<<20) + 6*int64(len(imageKeys())+2)
				if bytes != expected {
					t.Fatalf("capacity:%d != %d", bytes, expected)
				}
			} else if err == nil {
				t.Fatal("unsafe capacity accepted")
			}
		})
	}
}

func runtimeFixture() (podList, map[string]string) {
	refs := make(map[string]string)
	var rows podList
	for _, name := range runtimeImages {
		key := "runtime-" + name
		ref := "172.18.0.1:15501/nkd-local/" + name + "@sha256:" + strings.Repeat("a", 64)
		refs[key] = ref
		repository, digest, _ := strings.Cut(ref, "@")
		specImage := ref
		if tag, ok := temporalTags[name]; ok {
			specImage = repository + ":" + tag + "@" + digest
		}
		binding := runtimeBindings()[name]
		for _, role := range binding.Roles {
			var pod sitePod
			pod.Metadata = podMetadata{UID: name + role.Name, Namespace: binding.Namespace, Labels: map[string]string{"app.kubernetes.io/instance": binding.Instance, "app.kubernetes.io/name": binding.Application, "app.kubernetes.io/component": role.Component}}
			state := podContainerState{Name: role.Name, ImageID: ref, Ready: role.Kind == "containers"}
			if role.Kind == "initContainers" {
				exit := 0
				state.State.Terminated = &struct{ ExitCode *int }{&exit}
				pod.Spec.InitContainers = []podContainer{{Name: role.Name, Image: specImage}}
				pod.Status.InitContainerStatuses = []podContainerState{state}
			} else {
				running := json.RawMessage("{}")
				state.State.Running = &running
				pod.Spec.Containers = []podContainer{{Name: role.Name, Image: specImage}}
				pod.Status.ContainerStatuses = []podContainerState{state}
			}
			pod.Status.Phase = "Running"
			rows.Items = append(rows.Items, pod)
		}
	}
	return rows, refs
}

func TestRuntimeRolesRequireAllExactReadySubjects(t *testing.T) {
	for _, name := range []string{"exact", "foreignSpec", "foreignID", "notReady", "initFailed", "missingImage", "missingStatus", "duplicateStatus", "foreignReplica", "missingTemporalRole", "missingBashRole", "unrelatedReplica", "missingInitExit", "missingUID"} {
		t.Run(name, func(t *testing.T) {
			rows, refs := runtimeFixture()
			for i := range rows.Items {
				pod := &rows.Items[i]
				if pod.Metadata.UID == "postgresqlpostgresql" {
					switch name {
					case "foreignSpec":
						pod.Spec.Containers[0].Image = "foreign"
					case "foreignID":
						pod.Status.ContainerStatuses[0].ImageID = "foreign"
					case "notReady":
						pod.Status.ContainerStatuses[0].Ready = false
					case "missingStatus":
						pod.Status.ContainerStatuses = nil
					case "duplicateStatus":
						pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, pod.Status.ContainerStatuses[0])
					case "missingUID":
						pod.Metadata.UID = ""
					case "foreignReplica", "unrelatedReplica":
						raw, _ := json.Marshal(pod)
						var replica sitePod
						_ = json.Unmarshal(raw, &replica)
						replica.Metadata.UID = "foreign-replica"
						replica.Spec.Containers[0].Image = "foreign.example/postgresql:substitute"
						if name == "unrelatedReplica" {
							replica.Metadata.Namespace = "unrelated"
						}
						rows.Items = append(rows.Items, replica)
					}
				}
				if pod.Metadata.UID == "spire-tpm-serverinit-tpm-direct" {
					switch name {
					case "initFailed":
						exit := 1
						pod.Status.InitContainerStatuses[0].State.Terminated.ExitCode = &exit
					case "missingInitExit":
						pod.Status.InitContainerStatuses[0].State.Terminated.ExitCode = nil
					}
				}
			}
			remove := ""
			switch name {
			case "missingImage":
				remove = "postgresqlpostgresql"
			case "missingTemporalRole":
				remove = "temporal-servertemporal-frontend"
			case "missingBashRole":
				remove = "spire-bashfsgroupfix"
			}
			if remove != "" {
				rows.Items = slicesDeletePod(rows.Items, remove)
			}
			result, err := validateRuntimePods(rows, refs)
			if name == "exact" || name == "unrelatedReplica" {
				if err != nil {
					t.Fatal(err)
				}
				if len(result) != 12 || len(result["runtime-spire-bash"].Containers) != 2 {
					t.Fatal("runtime role coverage incomplete")
				}
			} else if err == nil {
				t.Fatal("unsafe runtime accepted")
			}
		})
	}
}

func slicesDeletePod(rows []sitePod, uid string) []sitePod {
	out := make([]sitePod, 0, len(rows))
	for _, pod := range rows {
		if pod.Metadata.UID != uid {
			out = append(out, pod)
		}
	}
	return out
}

func TestRuntimeValuesUseObservedNativeDigests(t *testing.T) {
	outputs := make(map[string]SelectedOutput)
	for i, key := range imageKeys() {
		outputs[key] = SelectedOutput{Observation: lifecycle.Artifact{Digest: "sha256:" + strings.Repeat(string(rune('a'+i%6)), 64)}}
	}
	outputs["postgresql"] = SelectedOutput{Observation: lifecycle.Artifact{Digest: "chart-pin"}}
	values := runtimeValues("172.18.0.1:15501", outputs)
	postgres := values["postgresql"].(postgresOverrides)
	if postgres.Image.Digest != outputs["runtime-postgresql"].Observation.Digest || outputs["postgresql"].Observation.Digest != "chart-pin" {
		t.Fatal("chart/image namespaces confused")
	}
	spire := values["spire"].(spireOverrides)
	if spire.Agent.SocketAlternate.Image.Tag != outputs["runtime-spire-bash"].Observation.Digest || spire.Agent.FSGroupFix.Image != spire.Agent.SocketAlternate.Image || spire.CSI.NodeDriverRegistrar.Image.Tag != outputs["runtime-csi-node-driver-registrar"].Observation.Digest {
		t.Fatal("SPIRE roles not pinned")
	}
	temporal := values["temporal"].(temporalOverrides)
	if temporal.Server.Image.Tag != "1.30.3@"+outputs["runtime-temporal-server"].Observation.Digest {
		t.Fatal("Temporal native digest missing")
	}
}

func TestRuntimeValueProjectionPreservesFollowingKeys(t *testing.T) {
	source := "chart:\n  name: spire\nvalues:\n  - values.yaml\n  - overlay.yaml\n# preserved comment\nrequires:\n  - gate\n"
	raw, err := appendRuntimeValues([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(source, "requires:\n", "  - nkd-image-inputs.json\nrequires:\n", 1)
	if string(raw) != expected {
		t.Fatalf("unexpected projection:%s", raw)
	}
	for _, source := range []string{"chart:\n name: spire\n", "values:\n  - one\nvalues:\n  - two\n"} {
		if _, err = appendRuntimeValues([]byte(source)); err == nil {
			t.Fatal("ambiguous values form accepted")
		}
	}
}

func TestRuntimePreparedValuesRequireOwningReaderRevalidation(t *testing.T) {
	for _, name := range []string{"exact", "duplicateHeader", "foreignSemantics", "existingOverride", "emptyValues", "readerFailure"} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			outputs := make(map[string]SelectedOutput)
			for _, key := range imageKeys() {
				outputs[key] = SelectedOutput{Observation: lifecycle.Artifact{Digest: "sha256:" + strings.Repeat("a", 64)}}
			}
			source := "chart:\n  name: fixture\nvalues:\n  - values.yaml\nrequires:\n  - gate\n"
			for _, item := range []string{"postgresql", "spire", "temporal"} {
				dir := filepath.Join(work, item)
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				raw := source
				if item == "postgresql" && name == "duplicateHeader" {
					raw += "values:\n  - foreign\n"
				}
				if err := privateWrite(filepath.Join(dir, "release.yaml"), []byte(raw)); err != nil {
					t.Fatal(err)
				}
			}
			if name == "existingOverride" {
				if err := privateWrite(filepath.Join(work, "postgresql/nkd-image-inputs.json"), []byte("foreign")); err != nil {
					t.Fatal(err)
				}
			}
			calls := make(map[string]int)
			capture := func(_ context.Context, args []string, _ string, _ time.Duration) ([]byte, error) {
				if len(args) != 4 || args[0] != "python3" || args[2] != "release" {
					t.Fatalf("wrong owner: %v", args)
				}
				item := filepath.Base(filepath.Dir(args[3]))
				calls[item]++
				if name == "readerFailure" {
					return nil, os.ErrPermission
				}
				values := "values.yaml"
				if name == "emptyValues" {
					values = ""
				}
				if calls[item] == 2 {
					values += ",nkd-image-inputs.json"
				}
				fields := []string{"single", item, "1.2.3", "", "charts/" + item + ".tgz", item, "test", values, "gate"}
				if name == "foreignSemantics" && calls[item] == 2 {
					fields[6] = "foreign"
				}
				return []byte(strings.Join(fields, "\x1f")), nil
			}
			err := prepareRuntimeValues(context.Background(), work, work, "172.18.0.1:15501", outputs, capture)
			if name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range []string{"postgresql", "spire", "temporal"} {
					if calls[item] != 2 {
						t.Fatal("missing full revalidation")
					}
					raw, readErr := os.ReadFile(filepath.Join(work, item, "release.yaml"))
					if readErr != nil || string(raw) != strings.Replace(source, "requires:\n", "  - nkd-image-inputs.json\nrequires:\n", 1) {
						t.Fatal("other semantics changed")
					}
				}
			} else if err == nil {
				t.Fatal("unsafe override accepted")
			}
			if name == "existingOverride" {
				raw, readErr := os.ReadFile(filepath.Join(work, "postgresql/nkd-image-inputs.json"))
				if readErr != nil || string(raw) != "foreign" {
					t.Fatal("replaced foreign override")
				}
			}
		})
	}
}

func TestSiteImageCopyChecksEveryNativeReadback(t *testing.T) {
	for _, name := range []string{"exact", "changed"} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			if err := os.Chmod(work, 0o700); err != nil {
				t.Fatal(err)
			}
			tools := filepath.Join(work, "tools")
			if err := os.Mkdir(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			tool := filepath.Join(tools, "oras")
			script := "#!/bin/sh\nset -eu\ntest \"$NO_PROXY\" = 'existing.example,172.18.0.2'\ntest \"$no_proxy\" = 'lower.example,172.18.0.2'\ntest \"$HTTP_PROXY\" = 'http://proxy.example:8080'\ncase \"$1\" in\n cp) exit 0;;\n manifest) printf '{\"digest\":\"%s\"}\\n' \"$DESCRIPTOR\";;\n *) exit 42;;\nesac\n"
			if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			digest := "sha256:" + strings.Repeat("a", 64)
			descriptor := digest
			if name == "changed" {
				descriptor = "foreign"
			}
			outputs := make(map[string]SelectedOutput)
			for _, key := range imageKeys() {
				outputs[key] = SelectedOutput{Layout: filepath.Join(work, "run/packages", key), Observation: lifecycle.Artifact{Digest: digest}}
			}
			session := ManagementSession{Name: "owned", Tools: tools, Work: work, Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "DESCRIPTOR=" + descriptor, "NO_PROXY=existing.example", "no_proxy=lower.example", "HTTP_PROXY=http://proxy.example:8080"}}}
			refs, err := copySiteImages(t.Context(), session, "172.18.0.2:5000", outputs)
			if environmentValue(session.Runner.Env, "NO_PROXY") != "existing.example" || environmentValue(session.Runner.Env, "no_proxy") != "lower.example" {
				t.Fatal("registry proxy scope escaped the image-copy runner")
			}
			if name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				if len(refs) != 15 {
					t.Fatal("not all site images copied")
				}
				for _, key := range imageKeys() {
					if !strings.HasSuffix(refs[key], "@"+digest) {
						t.Fatal("native manifest not pinned")
					}
					if _, err = os.Stat(filepath.Join(work, "readback-"+key+".log")); err != nil {
						t.Fatal(err)
					}
				}
			} else if err == nil {
				t.Fatal("changed readback accepted")
			}
		})
	}
}
