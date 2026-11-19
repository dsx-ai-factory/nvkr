// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestNativeIdentityStagingRequiresDedicatedBoundedMount(t *testing.T) {
	for _, scenario := range []string{"selected", "missing mount", "missing volume", "wrong path", "wrong name", "read only", "subpath", "subpath expression", "duplicate mount", "duplicate volume", "memory backed", "unbounded", "different bound", "malformed emptyDir", "shadowed child", "host path"} {
		t.Run(scenario, func(t *testing.T) {
			raw := json.RawMessage(`{"sizeLimit":"256Mi"}`)
			volume := nativeIdentityVolume{profileVolume: profileVolume{Name: nativeIdentityStagingVolume, EmptyDir: &raw}}
			mount := nativeIdentityMount{profileMount: profileMount{Name: nativeIdentityStagingVolume, MountPath: nativeIdentityStagingRoot}}
			pod := nativeIdentityPod{container: nativeIdentityContainer{VolumeMounts: []nativeIdentityMount{mount}}}
			pod.pod.Spec.Volumes = []nativeIdentityVolume{volume}
			switch scenario {
			case "missing mount":
				pod.container.VolumeMounts = nil
			case "missing volume":
				pod.pod.Spec.Volumes = nil
			case "wrong path":
				pod.container.VolumeMounts[0].MountPath = "/tmp"
			case "wrong name":
				pod.container.VolumeMounts[0].Name = "another-volume"
			case "read only":
				pod.container.VolumeMounts[0].ReadOnly = true
			case "subpath":
				pod.container.VolumeMounts[0].SubPath = "another-dir"
			case "subpath expression":
				pod.container.VolumeMounts[0].SubPathExpr = "$(POD_NAME)"
			case "duplicate mount":
				pod.container.VolumeMounts = append(pod.container.VolumeMounts, mount)
			case "duplicate volume":
				pod.pod.Spec.Volumes = append(pod.pod.Spec.Volumes, volume)
			case "memory backed":
				raw = json.RawMessage(`{"medium":"Memory","sizeLimit":"256Mi"}`)
			case "unbounded":
				raw = json.RawMessage(`{}`)
			case "different bound":
				raw = json.RawMessage(`{"sizeLimit":"128Mi"}`)
			case "malformed emptyDir":
				raw = json.RawMessage(`{"sizeLimit":256}`)
			case "shadowed child":
				mount.MountPath += "/stage.fixture"
				pod.container.VolumeMounts = append(pod.container.VolumeMounts, mount)
			case "host path":
				pod.pod.Spec.Volumes[0].HostPath = &struct{ Path, Type string }{Path: nativeIdentityStagingRoot}
			}
			if err := nativeIdentityStaging(pod); (err == nil) != (scenario == "selected") {
				t.Fatal("staging admission disagrees with selected mount", err)
			}
		})
	}
}

// Opt-in source oracle: Helm owns values merging and rendering. Comparing the
// complete source output proves this adapter adds only its exact staging mount
// and volume, retaining each source layout's security, probes and image fields.
func TestNativeIdentityStagingSourceChart(t *testing.T) {
	source, tools := os.Getenv("NVKR_NVKE_SOURCE_CONTRACT"), os.Getenv("NVKR_TEST_SITE_HELM_TOOLS")
	if source == "" || tools == "" {
		t.Skip("requires an exact NVKE source snapshot and selected Helm directory")
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(tools) {
		t.Fatal("source and selected tools must be absolute")
	}
	identity, err := selectNativeControllerIdentity(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	root := privateTestDir(t)
	values := nativeTenantSecurityValues()["controller"].(map[string]any)
	baseline := maps.Clone(values)
	delete(baseline, "volumes")
	delete(baseline, "volumeMounts")
	var objects [2][]map[string]any
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for index, override := range []map[string]any{baseline, values} {
		encoded, err := yaml.Marshal(override)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, []string{"baseline.yaml", "staging.yaml"}[index])
		writeTenantFixture(t, path, encoded, 0600)
		args := []string{filepath.Join(tools, "helm"), "template", identity.name, filepath.Join(source, "controller/deploy/charts/controller"), "--namespace", identity.name, "--kube-version", "1.36.3", "-f", filepath.Join(source, "deploy/nico/controller/values.yaml"), "-f", filepath.Join(source, "deploy/nico/controller/overlay.yaml"), "-f", path, "--set", "appDb.postgresPassword=synthetic-fixture-password", "--set", "profiles.image.tag=synthetic-fixture"}
		runner := Runner{Work: root, Env: NativeEnvironment()}
		raw, err := runner.Capture(ctx, args, []string{"baseline-render", "staging-render"}[index], 0)
		if err != nil {
			t.Fatal("source chart rendering failed; raw output remains private", err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		targets := 0
		for {
			var object map[string]any
			if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if object == nil {
				continue
			}
			if object["kind"] == "Deployment" && object["metadata"].(map[string]any)["name"] == identity.name {
				targets++
				if index == 1 {
					template := object["spec"].(map[string]any)["template"].(map[string]any)
					encoded, err := json.Marshal(template)
					if err != nil {
						t.Fatal(err)
					}
					var observed nativeIdentityPodDocument
					if err := json.Unmarshal(encoded, &observed); err != nil {
						t.Fatal(err)
					}
					pod := nativeIdentityPod{pod: observed}
					for _, container := range observed.Spec.Containers {
						if container.Name == identity.name {
							pod.container = container
						}
					}
					if err := nativeIdentityStaging(pod); err != nil {
						t.Fatal("rendered source does not admit helper staging", err)
					}
					spec := template["spec"].(map[string]any)
					remove := func(items []any) []any {
						var retained []any
						for _, raw := range items {
							if raw.(map[string]any)["name"] != nativeIdentityStagingVolume {
								retained = append(retained, raw)
							}
						}
						return retained
					}
					spec["volumes"] = remove(spec["volumes"].([]any))
					for _, raw := range spec["containers"].([]any) {
						container := raw.(map[string]any)
						if container["name"] == identity.name {
							container["volumeMounts"] = remove(container["volumeMounts"].([]any))
						}
					}
				}
			}
			objects[index] = append(objects[index], object)
		}
		if targets != 1 {
			t.Fatal("source chart must render exactly one selected controller")
		}
	}
	if !reflect.DeepEqual(objects[0], objects[1]) {
		t.Fatal("helper staging changed source fields beyond its dedicated volume/mount")
	}
}
