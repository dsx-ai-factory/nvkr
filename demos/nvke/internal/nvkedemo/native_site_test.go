// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNativeRetainedStatusObservesBothClustersAndPreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name                string
		unhealthy, canceled bool
		missing             bool
		wantError           bool
	}{
		{name: "healthy"},
		{name: "health changed", unhealthy: true},
		{name: "canceled", canceled: true, wantError: true},
		{name: "missing config", missing: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, runner := operatorStatusFixture(t)
			access.siteQCOW = true
			var output bytes.Buffer
			runner.Progress = &output
			if tc.unhealthy {
				runner.Env = SetEnv(runner.Env, map[string]string{"READY_REPLY": "not ready"})
			}
			if tc.missing {
				access.tenant = filepath.Join(t.TempDir(), "missing")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			err := presentNativeStatus(ctx, runner, access)
			if (err != nil) != tc.wantError {
				t.Fatalf("status error=%v; wantError=%v", err, tc.wantError)
			}
			if tc.wantError {
				return
			}
			for _, label := range []string{"Site / native QCOW2", "Tenant / QCOW2"} {
				if !strings.Contains(output.String(), label) {
					t.Fatalf("missing native cluster observation %q: %s", label, &output)
				}
			}
			if tc.unhealthy && !strings.Contains(output.String(), "readiness not confirmed") {
				t.Fatal("changed cluster health was hidden")
			}
			if strings.Contains(output.String(), "Kind management") || strings.Contains(output.String(), "PASS") {
				t.Fatal("native observation described a different runtime or granted acceptance")
			}
		})
	}
}

func TestNativeSiteSelectsExactlyOneInstalledSpirePackageUnit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		units []string
		want  string
	}{
		{"current", []string{"nvke-spire-configure.service"}, "restart nvke-spire-configure.service"},
		{"weekend", []string{"nke-spire-configure.service"}, "restart nke-spire-configure.service"},
		{"missing", nil, ""},
		{"ambiguous", []string{"nvke-spire-configure.service", "nke-spire-configure.service"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "called")
			for _, unit := range tc.units {
				if err := os.WriteFile(filepath.Join(root, unit), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			shim := filepath.Join(root, "systemctl")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf '%s' \"$*\" > "+operatorQuote(marker)+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "bash", "-euc", strings.ReplaceAll(siteSpireConfigurationScript, "/lib/systemd/system/", root+"/"))
			command.Env = []string{"PATH=" + root + ":/usr/bin:/bin"}
			output, err := command.CombinedOutput()
			if tc.want == "" {
				if err == nil {
					t.Fatal("missing or ambiguous package unit accepted")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("rejected unit selection reached systemctl")
				}
				return
			}
			if err != nil {
				t.Fatalf("unit selection: %v: %s", err, output)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != tc.want {
				t.Fatalf("systemctl dispatch=%q error=%v", data, err)
			}
		})
	}
}

func TestNativeSiteHandoffObservesRuntimeExitAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		exited, callback, cancel bool
		want                     error
	}{
		{"callback", false, true, false, nil},
		{"startup failure", false, true, false, os.ErrPermission},
		{"runtime already exited", true, false, false, os.ErrPermission},
		{"exit supersedes queued callback", true, true, false, os.ErrPermission},
		{"canceled", false, false, true, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := &OwnedProcess{done: make(chan struct{}), waitErr: os.ErrPermission}
			if tc.exited {
				close(server.done)
			}
			handoff := &nativeHandoff{result: make(chan error, 1)}
			if tc.callback {
				if tc.exited {
					handoff.result <- nil
				} else {
					handoff.result <- tc.want
				}
			}
			if tc.cancel {
				cancel()
			}
			err := waitNativeSiteHandoff(ctx, handoff, server)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("handoff returned %v; want %v", err, tc.want)
			}
		})
	}
}

func TestNativeSiteSeedProjectsSharedTenantBudgetAndPreservesSource(t *testing.T) {
	const fixture = `tenants: [{id: original, name: unchanged}]
instance_types:
  - {id: a1b2c3d4-0005-4000-8000-000000000004, name: cpu6-mem32gb-disk60gb, site_id: a1b2c3d4-0001-4000-8000-000000000001, gpu_count: 0, cpu_count: 6, memory_gib: 32, disk_gib: 60, extra: preserve-site}
  - {id: a1b2c3d4-0005-4000-8000-000000000003, name: cpu4-mem6gb-disk60gb, site_id: a1b2c3d4-0001-4000-8000-000000000001, gpu_count: 0, cpu_count: 4, memory_gib: 6, disk_gib: 60, extra: preserve-tenant}
  - {id: unrelated, name: untouched, cpu_count: 2, memory_gib: 4, disk_gib: 50}
`
	type testCase struct {
		name   string
		change func(map[string]any)
	}
	cases := []testCase{{name: "known source"}}
	for index, role := range []string{"site", "tenant"} {
		cases = append(cases,
			testCase{role + " missing", func(doc map[string]any) {
				items := doc["instance_types"].([]any)
				doc["instance_types"] = append(items[:index], items[index+1:]...)
			}},
			testCase{role + " duplicate", func(doc map[string]any) {
				items := doc["instance_types"].([]any)
				doc["instance_types"] = append(items, items[index])
			}},
		)
		for field, value := range map[string]any{"id": "unknown", "name": "other", "site_id": "other", "gpu_count": 1, "cpu_count": 3, "memory_gib": 16, "disk_gib": 61} {
			cases = append(cases, testCase{role + " changed " + field, func(doc map[string]any) {
				doc["instance_types"].([]any)[index].(map[string]any)[field] = value
			}})
		}
	}
	cases = append(cases, testCase{"malformed instance type", func(doc map[string]any) {
		doc["instance_types"] = append(doc["instance_types"].([]any), "invalid")
	}})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var expected map[string]any
			if err := yaml.Unmarshal([]byte(fixture), &expected); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(expected)
			}
			raw, err := yaml.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "seed.yaml")
			if err := os.WriteFile(source, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			path, err := nativeSiteSeed(t.Context(), source, dir, NativeResources{})
			original, readErr := os.ReadFile(source)
			if readErr != nil || !bytes.Equal(original, raw) {
				t.Fatal("source seed changed")
			}
			if tc.change != nil {
				if err == nil || path != "" {
					t.Fatalf("unknown or ambiguous source accepted: path=%q error=%v", path, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "native-seed.yaml")); !os.IsNotExist(err) {
					t.Fatal("rejected source produced output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]any
			if err := yaml.Unmarshal(data, &actual); err != nil {
				t.Fatal(err)
			}
			items := expected["instance_types"].([]any)
			site := items[0].(map[string]any)
			site["name"], site["memory_gib"] = "native-site-cpu6-mem12gb-disk60gb", 12
			tenant := items[1].(map[string]any)
			tenant["name"], tenant["cpu_count"], tenant["memory_gib"] = "cpu6-mem12gb-disk60gb", 6, 12
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("runtime seed changed fields outside selected sizing: got=%v want=%v", actual, expected)
			}
			legacy := []byte(fmt.Sprintf("  - id: %q\n    name: \"cpu4-mem6gb-disk60gb\"\n    site_id: %q\n    gpu_count: 0\n    cpu_count: 4\n    memory_gib: 6\n    disk_gib: 60\n", TenantInstanceType, SiteID))
			projected, err := projectSeedFixture(legacy, plainSHA(legacy))
			if err != nil || !bytes.Contains(projected, []byte("cpu_count: 6")) || !bytes.Contains(projected, []byte("memory_gib: 12")) {
				t.Fatalf("legacy and native tenant policy diverged: %s; %v", projected, err)
			}
		})
	}
}

func TestNativeSiteStartupNeverCopiesOrRunsHostCLI(t *testing.T) {
	cfg := startupDescription()
	cfg.Deployment = &NativeSiteDeployment{NicoURL: "http://172.30.0.1:18080", ImageURL: "http://172.30.0.1:18081/local.qcow2", ProfilesImage: "docker.io/library/cwe-profiles:local"}
	data, err := SiteStartupUserData(startupTestContext(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded startupCloudConfig
	if err := yaml.Unmarshal([]byte(data), &decoded); err != nil {
		t.Fatal(err)
	}
	script := decoded.WriteFiles[0].Content
	for _, forbidden := range []string{"nvkectl", "kind create", "\ndocker ", "--disable-mtls"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("host control entered guest startup: %s", forbidden)
		}
	}
	if !strings.Contains(script, "images import --local --digests --base-name ") {
		t.Fatal("guest must import local runtime images")
	}
}
