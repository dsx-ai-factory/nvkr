// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func nativeStorageObjects(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var objects []map[string]any
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			return objects
		} else if err != nil {
			t.Fatal(err)
		}
		if len(object) != 0 {
			objects = append(objects, object)
		}
	}
}

func assertNativeStorageFits(t *testing.T, claims []int) {
	t.Helper()
	var capacities []int
	paths := map[string]bool{}
	classes := 0
	for _, object := range nativeStorageObjects(t, nativeSiteStorage()) {
		if object["kind"] == "StorageClass" {
			metadata := object["metadata"].(map[string]any)
			annotations := metadata["annotations"].(map[string]any)
			if metadata["name"] != "nvke-native-local" || annotations["storageclass.kubernetes.io/is-default-class"] != "true" || object["provisioner"] != "kubernetes.io/no-provisioner" {
				t.Fatal("site storage must retain its static default class")
			}
			classes++
		}
		if object["kind"] != "PersistentVolume" {
			continue
		}
		spec := object["spec"].(map[string]any)
		path := spec["hostPath"].(map[string]any)["path"].(string)
		relative, err := filepath.Rel("/var/lib/nvke-native", path)
		if err != nil || !filepath.IsLocal(relative) || relative == "." || paths[relative] {
			t.Fatalf("volume directory is shared or outside the guest-owned root: %s", path)
		}
		paths[relative] = true
		if spec["storageClassName"] != "nvke-native-local" || spec["persistentVolumeReclaimPolicy"] != "Retain" {
			t.Fatal("site volumes must retain data under the demo's default storage class")
		}
		capacity := spec["capacity"].(map[string]any)["storage"].(string)
		gib, err := strconv.Atoi(strings.TrimSuffix(capacity, "Gi"))
		if err != nil || gib <= 0 {
			t.Fatalf("invalid test volume capacity %s", capacity)
		}
		capacities = append(capacities, gib)
	}
	if classes != 1 {
		t.Fatalf("expected one static default StorageClass, got %d", classes)
	}
	// Check aggregate capacity with all volumes available. The separate
	// reservation test covers queues racing database-volume initialization.
	slices.Sort(capacities)
	for _, claim := range claims {
		index := slices.IndexFunc(capacities, func(capacity int) bool { return capacity >= claim })
		if index < 0 {
			t.Fatalf("no remaining volume fits %dGi claim; available=%v", claim, capacities)
		}
		capacities = slices.Delete(capacities, index, index+1)
	}
}

func TestNativeSiteStorageFitsPersistentTelemetryQueues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims []int
	}{
		{"retained database storage", []int{40, 10}},
		// NKTP 0.2.0 defaults declare four 1Gi queue claims, one each for the
		// watchdog and three gateways after the demo's one-replica override.
		{"telemetry before databases", []int{1, 1, 1, 1, 40, 10}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertNativeStorageFits(t, tc.claims) })
	}
}

func TestNativeSiteDatabaseVolumesAreReserved(t *testing.T) {
	want := map[string]struct {
		claim    map[string]any
		capacity string
	}{
		"nvke-native-postgres": {map[string]any{"namespace": "postgres", "name": "data-postgres-postgresql-0"}, "40Gi"},
		"nvke-native-spire":    {map[string]any{"namespace": "spire-server", "name": "spire-data-spire-server-0"}, "10Gi"},
	}
	queues := 0
	for _, object := range nativeStorageObjects(t, nativeSiteStorage()) {
		if object["kind"] != "PersistentVolume" {
			continue
		}
		name := object["metadata"].(map[string]any)["name"].(string)
		spec := object["spec"].(map[string]any)
		if claim, database := want[name]; database {
			if !reflect.DeepEqual(spec["claimRef"], claim.claim) {
				t.Fatalf("%s can bind outside its database claim: %v", name, spec["claimRef"])
			}
			if capacity := spec["capacity"].(map[string]any)["storage"]; capacity != claim.capacity {
				t.Fatalf("%s reserved capacity = %v, want %s", name, capacity, claim.capacity)
			}
			delete(want, name)
			continue
		}
		if spec["claimRef"] != nil || spec["capacity"].(map[string]any)["storage"] != "1Gi" {
			t.Fatalf("queue volume must remain available to a 1Gi telemetry claim: %s", name)
		}
		queues++
	}
	if len(want) != 0 || queues != 4 {
		t.Fatalf("missing database reservations or queue capacity: databases=%v queues=%d", want, queues)
	}
}

func TestNativeSiteStorageDirectoriesMatchVolumes(t *testing.T) {
	data, err := SiteStartupUserData(startupTestContext(t), startupDescription())
	if err != nil {
		t.Fatal(err)
	}
	var startup startupCloudConfig
	if err := yaml.Unmarshal([]byte(data), &startup); err != nil {
		t.Fatal(err)
	}
	var command string
	for _, line := range strings.Split(startup.WriteFiles[0].Content, "\n") {
		if strings.HasPrefix(line, "install -d -m 1777 ") {
			if command != "" {
				t.Fatal("duplicate guest storage preparation")
			}
			command = line
		}
	}
	if command == "" {
		t.Fatal("guest storage preparation is missing")
	}
	dir := t.TempDir()
	// Execute only the rendered directory preparation inside this test's root.
	local := strings.ReplaceAll(command, "/var/lib/nvke-native", operatorQuote(dir))
	if output, err := exec.CommandContext(t.Context(), "bash", "-ec", local).CombinedOutput(); err != nil {
		t.Fatalf("prepare storage: %v: %s", err, output)
	}
	for _, object := range nativeStorageObjects(t, nativeSiteStorage()) {
		if object["kind"] != "PersistentVolume" {
			continue
		}
		path := object["spec"].(map[string]any)["hostPath"].(map[string]any)["path"].(string)
		relative, err := filepath.Rel("/var/lib/nvke-native", path)
		if err != nil || !filepath.IsLocal(relative) || relative == "." {
			t.Fatalf("volume path is outside the guest-owned root: %s", path)
		}
		info, err := os.Stat(filepath.Join(dir, relative))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0777 || info.Mode()&os.ModeSticky == 0 {
			t.Fatalf("volume lacks a guest directory writable by its source-owned non-root container: %s: %v", path, err)
		}
	}
}

func TestNativeSiteStorageMatchesSourceNKTPChart(t *testing.T) {
	chart, base := os.Getenv("NVKR_NATIVE_PROFILE_NKTP_CHART"), os.Getenv("NVKR_NATIVE_PROFILE_NKTP_VALUES")
	if chart == "" && base == "" {
		t.Skip("set exact NKTP chart and NVKE values for the source storage contract")
	}
	if chart == "" || base == "" {
		t.Fatal("both exact source chart inputs are required")
	}
	values, err := demoTelemetryValuesForProfile("small")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, values, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "helm", "template", "nktp", chart, "--namespace", "nktp", "--kube-version", "1.36.3", "-f", base, "-f", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var claims []int
	for _, object := range nativeStorageObjects(t, string(raw)) {
		if object["kind"] != "OpenTelemetryCollector" {
			continue
		}
		spec := object["spec"].(map[string]any)
		volumes, _ := spec["volumeClaimTemplates"].([]any)
		for _, volume := range volumes {
			claim := volume.(map[string]any)["spec"].(map[string]any)
			requests := claim["resources"].(map[string]any)["requests"].(map[string]any)
			if spec["replicas"] != 1 || requests["storage"] != "1Gi" || claim["storageClassName"] != nil {
				t.Fatalf("source queue contract changed: %v", claim)
			}
			if claim["selector"] != nil || claim["volumeName"] != nil || !reflect.DeepEqual(claim["accessModes"], []any{"ReadWriteOnce"}) ||
				(claim["volumeMode"] != nil && claim["volumeMode"] != "Filesystem") {
				t.Fatalf("source queue cannot bind to the demo's unreserved filesystem volumes: %v", claim)
			}
			claims = append(claims, 1)
		}
	}
	if len(claims) != 4 {
		t.Fatalf("source queue count changed: %d", len(claims))
	}
	assertNativeStorageFits(t, append(claims, 40, 10))
}
