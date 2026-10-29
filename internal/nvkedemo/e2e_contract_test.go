// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

func assertNightlyCode(t *testing.T, err error, code nvkrerr.Code) {
	t.Helper()
	var coded *nvkrerr.Error
	if err == nil || !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v; want %s", err, code)
	}
}

func TestNativeE2EOptInIsExplicit(t *testing.T) {
	for _, row := range []struct {
		value       string
		want, valid bool
	}{{"", false, true}, {"0", false, true}, {"1", true, true}, {"true", false, false}, {"yes", false, false}, {" 1", false, false}, {"2", false, false}} {
		t.Run(row.value, func(t *testing.T) {
			got, err := nativeE2EOptIn("NVKR_NVKE_E2E", row.value)
			if (err == nil) != row.valid || got != row.want {
				t.Fatalf("enabled=%v error=%v", got, err)
			}
			if !row.valid {
				assertNightlyCode(t, err, nvkrerr.CodeUsage)
			}
		})
	}
}

func TestNativeE2EExplicitConfigurationKeepsLocalInputsAndGuestResources(t *testing.T) {
	base := DemoFile{Version: 1,
		Build: BuildFile{Repo: "/source/nvkr", Definition: "inputs/definition.json", Environment: "inputs/environment.json", NVKR: "/distribution/nvkr", Attempt: "fresh", Jobs: 2, NVKEClone: "/source/nvke"},
		Run:   RunFile{Out: "/private/site", Network: "ovn", TimeoutSeconds: 3600, Resources: NativeResources{SiteMemoryGiB: 4, TenantMemoryGiB: 4, HeadroomMiB: 1024, VCPUs: 4}},
	}
	for _, row := range []struct {
		name   string
		change func(*DemoFile)
		valid  bool
	}{
		{"explicit local inputs", func(*DemoFile) {}, true},
		{"source config conformance", func(file *DemoFile) { file.Run.Conformance = true }, true},
		{"missing input", func(file *DemoFile) { file.Build.Environment = "" }, false},
		{"retained artifacts", func(file *DemoFile) { file.Run.Runs.Artifacts = "/old/run" }, false},
		{"partial resources", func(file *DemoFile) { file.Run.Resources.HeadroomMiB = 0 }, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			file := base
			row.change(&file)
			raw, err := yaml.Marshal(file)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "e2e.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			build, cfg, tenant, err := nativeE2EConfig(t.Context(), func(key string) string {
				if key == "NVKR_NVKE_E2E_CONFIG" {
					return path
				}
				return ""
			})
			if (err == nil) != row.valid {
				t.Fatalf("error=%v", err)
			}
			if !row.valid {
				return
			}
			if build.Definition != file.Build.Definition || build.Environment != file.Build.Environment || build.NVKR != file.Build.NVKR || build.NVKEClone != file.Build.NVKEClone || cfg.Resources != file.Run.Resources || !cfg.Cleanup || cfg.Runs != (RunInputs{}) || cfg.NVKR != "" || tenant != file.Run.Conformance {
				t.Fatalf("local input or guest resource selection changed: build=%+v run=%+v tenant=%v", build, cfg, tenant)
			}
		})
	}
}

func TestNativeE2EConfigurationSelectsSuiteBeforeBuild(t *testing.T) {
	for _, row := range []struct {
		name, tenant, conformance, missing string
		wantTenant, wantConformance, valid bool
	}{
		{name: "site smoke", valid: true},
		{name: "default helper", missing: "NVKR_NVKE_E2E_BINARY", valid: true},
		{name: "tenant", tenant: "1", wantTenant: true, valid: true},
		{name: "conformance implies tenant", tenant: "0", conformance: "1", wantTenant: true, wantConformance: true, valid: true},
		{name: "invalid tenant", tenant: "true"},
		{name: "invalid conformance", conformance: "true"},
		{name: "missing repo", missing: "NVKR_NVKE_E2E_REPO"},
		{name: "missing output", missing: "NVKR_NVKE_E2E_OUT"},
		{name: "missing attempt", missing: "NVKR_NVKE_E2E_ATTEMPT"},
	} {
		t.Run(row.name, func(t *testing.T) {
			env := map[string]string{
				"NVKR_NVKE_E2E_REPO": "/source/nvkr", "NVKR_NVKE_E2E_OUT": "/private/native-site",
				"NVKR_NVKE_E2E_ATTEMPT": "fresh-test", "NVKR_NVKE_E2E_BINARY": "/distribution/bin/nvke-demo",
				"NVKE_LOCAL_CLONE": "/source/nvke", "NVKR_NVKE_E2E_TENANT": row.tenant, "NVKR_NVKE_E2E_CONFORMANCE": row.conformance,
			}
			delete(env, row.missing)
			build, cfg, tenant, err := nativeE2EConfig(t.Context(), func(key string) string { return env[key] })
			if (err == nil) != row.valid {
				t.Fatalf("error=%v", err)
			}
			if !row.valid {
				assertNightlyCode(t, err, nvkrerr.CodeUsage)
				return
			}
			if tenant != row.wantTenant || build.Conformance != row.wantConformance || cfg.Conformance != row.wantConformance || build.Jobs != 1 || !cfg.Cleanup || build.Network != "ovn" || cfg.Network != "ovn" || build.NVKEClone != env["NVKE_LOCAL_CLONE"] || cfg.Runs != (RunInputs{}) || cfg.NVKR != "" {
				t.Fatalf("unexpected build/run selection: %+v %+v tenant=%v", build, cfg, tenant)
			}
			helper := env["NVKR_NVKE_E2E_BINARY"]
			if helper == "" {
				helper = filepath.Join(env["NVKR_NVKE_E2E_REPO"], "bin", "nvke-demo")
			}
			if cfg.DemoExecutable != helper || build.DemoExecutable != cfg.DemoExecutable {
				t.Fatal("existing helper distribution selection was lost")
			}
			// The Go test's temporary executable must not select Mac's adjacent
			// nvkr; the existing native distribution owns that bootstrap path.
			selected, err := defaultNativeBuild(&build, "darwin/arm64", build.DemoExecutable)
			if err != nil || !selected || build.NVKR != filepath.Join(filepath.Dir(helper), "nvkr") || build.Definition == "" || build.Environment != "inputs/environments/local-native-mac.json" {
				t.Fatalf("native Mac distribution selection: %+v error=%v", build, err)
			}
		})
	}
}

func TestNativeE2ESiteSmokeRequiresLiveAPIAndReadyNode(t *testing.T) {
	const good = `{"items":[{"metadata":{"name":"site","uid":"site-uid"},"status":{"nodeInfo":{"architecture":"arm64"},"conditions":[{"type":"Ready","status":"True"}]}}]}`
	for _, row := range []struct {
		name, api, nodes, wait, arch, cpu, domainArch, inventory string
		wrongDisk                                                bool
		valid                                                    bool
	}{
		{name: "healthy site", api: "ok", nodes: good, wait: "0", valid: true},
		{name: "healthy AMD site", api: "ok", nodes: strings.ReplaceAll(good, "arm64", "amd64"), wait: "0", arch: "amd64", valid: true},
		{name: "foreign node architecture", api: "ok", nodes: good, wait: "0", arch: "amd64"},
		{name: "foreign VM architecture", api: "ok", nodes: strings.ReplaceAll(good, "arm64", "amd64"), wait: "0", arch: "amd64", domainArch: "aarch64"},
		{name: "wrong VM CPUs", api: "ok", nodes: good, wait: "0", cpu: "6"},
		{name: "wrong VM backing", api: "ok", nodes: good, wait: "0", wrongDisk: true},
		{name: "extra VM", api: "ok", nodes: good, wait: "0", inventory: "extra"},
		{name: "missing VM", api: "ok", nodes: good, wait: "0", inventory: "missing"},
		{name: "unhealthy API", api: "unready", nodes: good, wait: "0"},
		{name: "node wait fails", api: "ok", nodes: good, wait: "1"},
		{name: "missing site node", api: "ok", nodes: `{"items":[]}`, wait: "0"},
		{name: "unready node", api: "ok", nodes: strings.ReplaceAll(good, "True", "False"), wait: "0"},
		{name: "unidentified node", api: "ok", nodes: strings.ReplaceAll(good, "site-uid", ""), wait: "0"},
		{name: "missing Ready condition", api: "ok", nodes: strings.ReplaceAll(good, "Ready", "MemoryPressure"), wait: "0"},
		{name: "malformed API response", api: "ok", nodes: "invalid", wait: "0"},
	} {
		t.Run(row.name, func(t *testing.T) {
			runner, root, digest, work, names := tenantVMFixture(t)
			arch, cpu := row.arch, row.cpu
			if arch == "" {
				arch = "arm64"
			}
			if cpu == "" {
				cpu = "4"
			}
			machine, err := nativeQEMUArchitecture(arch)
			if err != nil {
				t.Fatal(err)
			}
			if row.domainArch != "" {
				machine = row.domainArch
			}
			xmlPath := filepath.Join(root, names[0]+".xml")
			xml, err := os.ReadFile(xmlPath)
			if err != nil {
				t.Fatal(err)
			}
			xmlText := strings.ReplaceAll(string(xml), `arch="aarch64"`, `arch="`+machine+`"`)
			xmlText = strings.Replace(xmlText, "<os>", "<vcpu>"+cpu+"</vcpu><os>", 1)
			writeTenantFixture(t, xmlPath, []byte(xmlText), 0600)
			inventory := names[0] + "\n"
			if row.inventory == "extra" {
				inventory += names[1] + "\n"
			}
			if row.inventory == "missing" {
				inventory = ""
			}
			writeTenantFixture(t, filepath.Join(root, "inventory"), []byte(inventory), 0600)
			if row.wrongDisk {
				digest = "sha256:" + strings.Repeat("b", 64)
			}
			kubectl := filepath.Join(root, "kubectl")
			script := `#!/bin/sh
set -eu
case "$*" in
  *'get --raw=/readyz') printf '%s\n' "$API_REPLY" ;;
  *'wait --for=condition=Ready nodes --all --timeout=2m') exit "$WAIT_EXIT" ;;
  *'get nodes -o json') printf '%s\n' "$NODE_REPLY" ;;
  *) exit 99 ;;
esac
`
			if err := os.WriteFile(kubectl, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			runner.Env = append(runner.Env, "API_REPLY="+row.api, "NODE_REPLY="+row.nodes, "WAIT_EXIT="+row.wait)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			attempt := &nativeSiteAttempt{guard: &NetworkGuard{}, assets: NativeAssets{arch: arch}, simulatorWork: work, diskDigest: digest, resources: NativeResources{4, 4, 1024, 4}}
			err = withNativeSiteScope(ctx, attempt, runner, root, root, nativeE2ESiteSmoke)
			if (err == nil) != row.valid {
				t.Fatalf("error=%v", err)
			}
			if !row.valid {
				code := nvkrerr.CodePreflightFailed
				if row.name == "foreign VM architecture" {
					code = nvkrerr.CodeEngineCapability
				}
				if row.name == "wrong VM CPUs" {
					code = nvkrerr.CodeVerifyFailed
				}
				assertNightlyCode(t, err, code)
			}
			if row.valid {
				if raw, err := os.ReadFile(filepath.Join(root, "native-site-domains.json")); err != nil || !strings.Contains(string(raw), digest) {
					t.Fatalf("site VM evidence missing: %s %v", raw, err)
				}
			}
		})
	}
}

func TestNativeE2EMacBuildUsesExplicitDistributionBeforeWork(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("default Mac build distribution admission is native ARM64 only")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	distribution := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	_, err = buildArtifacts(ctx, BuildConfig{
		Repository: repo, AttemptPrefix: "must-not-start", Jobs: 1,
		DemoExecutable: filepath.Join(distribution, "nvke-demo"),
	})
	// The deliberately absent adjacent nvkr must fail existing distribution
	// admission before bootstrap, source preparation or guest work.
	if err == nil || !strings.Contains(err.Error(), filepath.Join(distribution, "nvkr")) {
		t.Fatalf("explicit native distribution was not selected: %v", err)
	}
}
