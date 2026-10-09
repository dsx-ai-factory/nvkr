// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

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

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

func TestProfilesTransportPreservesOriginalBundleIndex(t *testing.T) {
	for _, scenario := range []string{"exact", "foreign-manifest", "corrupt-blob"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			payload := filepath.Join(root, "profiles")
			if err := os.WriteFile(payload, []byte("profiles data"), 0o600); err != nil {
				t.Fatal(err)
			}
			original, imported := filepath.Join(root, "original"), filepath.Join(root, "imported")
			result, err := delivery.BuildBinaryBundle(t.Context(), original, delivery.BinaryBundleRequest{
				OS: "linux", Arch: "arm64", Purpose: "controller-profiles",
				Files: []delivery.BinaryFileInput{{Name: "profiles", Path: payload, InstallPath: "/profiles", Mode: 0o644}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(imported, os.DirFS(original)); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(original, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			// ORAS creates a transport-only index without NKD bootstrap metadata.
			var index struct {
				SchemaVersion int    `json:"schemaVersion"`
				MediaType     string `json:"mediaType"`
				Manifests     []struct {
					MediaType string `json:"mediaType"`
					Digest    string `json:"digest"`
					Size      int64  `json:"size"`
				} `json:"manifests"`
			}
			if err := json.Unmarshal(raw, &index); err != nil {
				t.Fatal(err)
			}
			digest := index.Manifests[0].Digest
			if scenario == "foreign-manifest" {
				index.Manifests[0].Digest = "sha256:" + strings.Repeat("a", 64)
			}
			generic, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(imported, "index.json"), generic, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := delivery.InspectBinaryBundle(t.Context(), imported); err == nil {
				t.Fatal("generic transport index unexpectedly passed the strict owner")
			}
			if scenario == "corrupt-blob" {
				entries, err := os.ReadDir(filepath.Join(imported, "blobs/sha256"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(imported, "blobs/sha256", entries[0].Name()), []byte("corrupt"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err = restoreBundleTransportIndex(t.Context(), original, imported, digest)
			if scenario == "foreign-manifest" {
				if err == nil {
					t.Fatal("foreign manifest reached index restoration")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			restored, err := os.ReadFile(filepath.Join(imported, "index.json"))
			if err != nil || !bytes.Equal(restored, raw) {
				t.Fatal("original bootstrap bytes were not preserved exactly")
			}
			_, checked, err := delivery.InspectBinaryBundle(t.Context(), imported)
			if scenario == "corrupt-blob" {
				if err == nil {
					t.Fatal("restored description concealed corrupt payload bytes")
				}
			} else if err != nil || checked.RootDigest != result.RootDigest || checked.SemanticDigest != result.SemanticDigest {
				t.Fatalf("original bundle identity was not independently verified: %v", err)
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

func TestProfilesVolumeDisablesImageAndLimitsMount(t *testing.T) {
	values := profilesVolumeValues()
	profiles := values["profiles"].(map[string]any)
	if profiles["path"] != "/profiles" || profiles["image"].(map[string]any)["enabled"] != false {
		t.Fatal("data profiles must not enable the image materializer")
	}
	volumes := values["volumes"].([]any)
	mounts := values["volumeMounts"].([]any)
	if len(volumes) != 1 || len(mounts) != 1 {
		t.Fatal("profiles require exactly one volume and mount")
	}
	host := volumes[0].(map[string]any)["hostPath"].(map[string]string)
	mount := mounts[0].(map[string]any)
	if host["path"] != "/var/nkd-local-profiles/profiles" || host["type"] != "Directory" ||
		mount["mountPath"] != "/profiles" || mount["readOnly"] != true ||
		mount["name"] != volumes[0].(map[string]any)["name"] {
		t.Fatal("profiles must use the exact prepared read-only directory in the owned Kind node")
	}
}

// Optional source-owned consumer check. Supply a chart exported from the exact
// InstallerCommit; this check renders it but creates no Kubernetes resources.
func TestProfilesVolumeSourceChart(t *testing.T) {
	chart := os.Getenv("NKD_DEMO_PROFILE_CHART")
	if chart == "" {
		t.Skip("set NKD_DEMO_PROFILE_CHART to the pinned NKE controller chart")
	}
	raw, err := json.Marshal(profilesVolumeValues())
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
		t.Fatalf("source-owned chart rejected profiles volume: %v\n%s", err, rendered)
	}
	value := string(rendered)
	for _, required := range []string{"mountPath: /profiles", "readOnly: true", "path: /var/nkd-local-profiles/profiles", "type: Directory", "name: PROFILES_DIR"} {
		if !strings.Contains(value, required) {
			t.Fatalf("missing source-owned profile volume field %q", required)
		}
	}
	if strings.Contains(value, "materialize-profiles") || strings.Contains(value, "cwe-profiles") {
		t.Fatal("profiles data must not invoke a runnable profiles image")
	}
}

func TestDeployedProfilesVolumeRejectsSubstitution(t *testing.T) {
	for _, scenario := range []string{"exact", "image", "path", "writable", "missing-volume", "duplicate-mount"} {
		t.Run(scenario, func(t *testing.T) {
			var pod sitePod
			pod.Spec.Volumes = []profileVolume{{Name: "local-profiles", HostPath: &struct{ Path, Type string }{"/var/nkd-local-profiles/profiles", "Directory"}}}
			pod.Spec.Containers = []podContainer{{Name: "controller", VolumeMounts: []profileMount{{"local-profiles", "/profiles", true}}}}
			switch scenario {
			case "image":
				pod.Spec.InitContainers = []podContainer{{Name: "materialize-profiles"}}
			case "path":
				pod.Spec.Volumes[0].HostPath.Path = "/var"
			case "writable":
				pod.Spec.Containers[0].VolumeMounts[0].ReadOnly = false
			case "missing-volume":
				pod.Spec.Volumes = nil
			case "duplicate-mount":
				pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, pod.Spec.Containers[0].VolumeMounts[0])
			}
			if err := validateProfilesVolume(pod, "controller"); (err == nil) != (scenario == "exact") {
				t.Fatalf("profiles volume acceptance: %v", err)
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
			script := "#!/bin/sh\ncase \"$1\" in\n cp) exit 0;;\n manifest) printf '{\"digest\":\"%s\"}\\n' \"$DESCRIPTOR\";;\n *) exit 42;;\nesac\n"
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
			session := ManagementSession{Name: "owned", Tools: tools, Work: work, Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "DESCRIPTOR=" + descriptor}}}
			refs, err := copySiteImages(context.Background(), session, "172.18.0.1:15501", outputs)
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
