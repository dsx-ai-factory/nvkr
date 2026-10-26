// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const fullDemoYAML = `version: 1
build:
  repo: /native/nvkr
  nvkeClone: /native/nvke-source
  attempt: example-01
  jobs: 2
run:
  nvkeCheckout: /native/nvke-installer
  nvkr: /native/nvkr
  out: /native/output
  timeoutSeconds: 900
  runs:
    os: /native/runs/os
    components: /native/runs/components
    providers: /native/runs/providers
    site: /native/runs/site
    tools: /native/runs/tools
    profiles: /native/runs/profiles
    simulator: /native/runs/simulator
restore:
  work: /native/output/network
`

func demoConfigPath(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "demo.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertConfigUsage(t *testing.T, err error) {
	t.Helper()
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
		t.Fatalf("require CodeUsage, got %v", err)
	}
}

func TestDemoFileTypedConversion(t *testing.T) {
	path := demoConfigPath(t, fullDemoYAML)
	file, err := LoadDemoFile(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	build, err := file.BuildConfig(&progress)
	if err != nil || build != (BuildConfig{Repository: "/native/nvkr", NVKEClone: "/native/nvke-source", AttemptPrefix: "example-01", Jobs: 2, Progress: &progress, Network: "ovn"}) {
		t.Fatalf("build=%+v error=%v", build, err)
	}
	run, err := file.RunConfig(&progress)
	expected := Config{Network: "ovn", NVKECheckout: "/native/nvke-installer", NVKR: "/native/nvkr", Out: "/native/output", Timeout: 900 * time.Second, Progress: &progress, Runs: RunInputs{
		OS: "/native/runs/os", Components: "/native/runs/components", Providers: "/native/runs/providers", Site: "/native/runs/site", Tools: "/native/runs/tools", Profiles: "/native/runs/profiles", Simulator: "/native/runs/simulator"}}
	if err != nil || run != expected {
		t.Fatalf("run=%+v error=%v", run, err)
	}
	work, err := file.RestoreWork()
	if err != nil || work != "/native/output/network" {
		t.Fatalf("work=%q error=%v", work, err)
	}
	_, _, err = file.E2EConfig(nil)
	assertConfigUsage(t, err)
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != fullDemoYAML || progress.Len() != 0 {
		t.Fatal("loading and conversion must not write files or progress")
	}
}

func TestDemoNetworkSelection(t *testing.T) {
	for _, tc := range []struct {
		name, network, expected string
	}{
		{"default", "", "ovn"},
		{"maintained", "ovn", "ovn"},
		{"upstream diagnostic", "upstream-ovn", "upstream-ovn"},
		{"temporary diagnostic", "kind-calico", "kind-calico"},
		{"unknown", "calico", ""},
		{"case alias", "OVN", ""},
		{"whitespace", " ovn", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := DemoFile{Version: 1, Build: BuildFile{Repo: "/native/repo", Attempt: "fresh", Jobs: 1}, Run: RunFile{Network: tc.network, Out: "/native/output", TimeoutSeconds: 3600}}
			build, run, err := file.E2EConfig(nil)
			if tc.expected == "" {
				assertConfigUsage(t, err)
				return
			}
			if err != nil || run.Network != tc.expected || build.Network != tc.expected {
				t.Fatalf("network=%q error=%v", run.Network, err)
			}
			contents := strings.Replace(fullDemoYAML, "run:\n", "run:\n  network: "+tc.expected+"\n", 1)
			loaded, err := LoadDemoFile(t.Context(), demoConfigPath(t, contents))
			if err != nil {
				t.Fatal(err)
			}
			configured, err := loaded.RunConfig(nil)
			if err != nil || configured.Network != tc.expected {
				t.Fatalf("configured network=%q error=%v", configured.Network, err)
			}
		})
	}
}

func TestDemoTenantWorkersSelection(t *testing.T) {
	for _, tc := range []struct {
		value       string
		conformance bool
		workers     int
		fail        bool
	}{
		{"0", false, 0, false}, {"1", false, 1, false}, {"2", false, 2, false},
		{"0", true, 0, false}, {"2", true, 2, false}, {"1", true, 0, true},
		{"-1", false, 0, true}, {"3", false, 0, true},
		{"1.5", false, 0, true}, {"'2'", false, 0, true}, {"true", false, 0, true},
	} {
		t.Run(tc.value+fmt.Sprint(tc.conformance), func(t *testing.T) {
			contents := strings.Replace(fullDemoYAML, "run:\n", fmt.Sprintf("run:\n  tenantWorkers: %s\n  conformance: %t\n", tc.value, tc.conformance), 1)
			file, err := LoadDemoFile(t.Context(), demoConfigPath(t, contents))
			var cfg Config
			if err == nil {
				cfg, err = file.RunConfig(nil)
			}
			if tc.fail {
				assertConfigUsage(t, err)
				return
			}
			if err != nil || cfg.TenantWorkers != tc.workers || cfg.Conformance != tc.conformance {
				t.Fatalf("worker selection lost: %+v %v", cfg, err)
			}
			file.Run.NVKR, file.Run.Runs = "", RunFileInputs{}
			build, run, err := file.E2EConfig(nil)
			if err != nil || run.TenantWorkers != tc.workers || run.Conformance != tc.conformance || build.Conformance != tc.conformance {
				t.Fatalf("E2E selection changed worker or suite intent: %+v %+v %v", build, run, err)
			}
		})
	}
}

func TestDemoCleanupRequiresStrictBoolean(t *testing.T) {
	for _, test := range []struct {
		value          string
		valid, cleanup bool
	}{{"false", true, false}, {"true", true, true}, {"'true'", false, false}, {"yes", false, false}, {"on", false, false}, {"1", false, false}, {"null", false, false}, {"[]", false, false}} {
		t.Run(test.value, func(t *testing.T) {
			contents := strings.Replace(fullDemoYAML, "run:\n", "run:\n  cleanup: "+test.value+"\n", 1)
			file, err := LoadDemoFile(t.Context(), demoConfigPath(t, contents))
			if !test.valid {
				assertConfigUsage(t, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := file.RunConfig(nil)
			if err != nil || cfg.Cleanup != test.cleanup {
				t.Fatal("cleanup intent lost", cfg.Cleanup, err)
			}
		})
	}
}

func TestDemoFileSchemaRejection(t *testing.T) {
	const valid = "version: 1\nrestore:\n  work: /native/work\n"
	for _, tc := range []struct{ name, contents string }{
		{"empty", ""}, {"missing version", "restore:\n  work: /native/work\n"},
		{"wrong version", strings.Replace(valid, "version: 1", "version: 2", 1)},
		{"version string", strings.Replace(valid, "version: 1", `version: "1"`, 1)},
		{"version float", strings.Replace(valid, "version: 1", "version: 1.0", 1)},
		{"fractional version", strings.Replace(valid, "version: 1", "version: 1.5", 1)},
		{"missing operations", "version: 1\n"}, {"empty operation", "version: 1\nbuild: {}\n"},
		{"unknown root", valid + "unknown: secret-config-sentinel\n"},
		{"unknown nested", valid + "build:\n  repository: secret-config-sentinel\n"},
		{"unknown run", valid + "run:\n  tool: secret-config-sentinel\n"},
		{"unknown runs", valid + "run:\n  runs:\n    wrong: secret-config-sentinel\n"},
		{"duplicate root", valid + "version: 1\n"},
		{"duplicate nested", "version: 1\nrestore:\n  work: first\n  work: secret-config-sentinel\n"},
		{"case alias", valid + "Version: 1\n"},
		{"case nested alias", "version: 1\nbuild:\n  repo: first\n  Repo: secret-config-sentinel\n"},
		{"integer string field", "version: 1\nrestore:\n  work: 123\n"},
		{"boolean string field", "version: 1\nrestore:\n  work: true\n"},
		{"null", "version: 1\nrestore:\n  work: null\n"},
		{"sequence", "version: 1\nrestore:\n  work: [secret-config-sentinel]\n"},
		{"integer coercion", "version: 1\nbuild:\n  jobs: '2'\n"},
		{"float integer", "version: 1\nbuild:\n  jobs: 2.0\n"},
		{"timeout integer overflow", "version: 1\nrun:\n  timeoutSeconds: 9223372036854775808\n"},
		{"nonstring key", "version: 1\nrestore:\n  12: secret-config-sentinel\n"},
		{"alias", "version: 1\nbuild:\n  repo: &path /native\n  nvkeClone: *path\n"},
		{"merge", "version: 1\nbuild: &base\n  repo: /native\nrun:\n  <<: *base\n"},
		{"tag", "version: 1\nrestore:\n  work: !custom secret-config-sentinel\n"},
		{"second document", valid + "---\n" + valid}, {"empty second document", valid + "---\n"},
		{"malformed", valid + "build: [secret-config-sentinel\n"},
		{"root scalar", "secret-config-sentinel\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := LoadDemoFile(t.Context(), demoConfigPath(t, tc.contents))
			assertConfigUsage(t, err)
			if result != (DemoFile{}) || strings.Contains(err.Error(), "secret-config-sentinel") {
				t.Fatalf("failed loader returned descriptions or exposed contents: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestDemoFileBoundedExplicitRead(t *testing.T) {
	valid := "version: 1\nrestore:\n  work: /native/work\n"
	for _, name := range []string{"maximum", "oversize", "symlink", "directory", "fifo", "missing", "empty path", "canceled", "nil context"} {
		t.Run(name, func(t *testing.T) {
			contents := valid
			if name == "maximum" || name == "oversize" {
				contents += "#" + strings.Repeat("x", demoFileLimit-len(valid)-2) + "\n"
				if name == "oversize" {
					contents += "x"
				}
			}
			path := demoConfigPath(t, contents)
			ctx := t.Context()
			switch name {
			case "symlink":
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "directory":
				path = filepath.Dir(path)
			case "fifo":
				path += ".fifo"
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				path += ".missing"
			case "empty path":
				path = ""
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil context":
				ctx = nil
			}
			result, err := LoadDemoFile(ctx, path)
			if name == "maximum" {
				if err != nil || result.Restore.Work != "/native/work" {
					t.Fatalf("maximum sized file rejected: %v", err)
				}
				return
			}
			assertConfigUsage(t, err)
			if result != (DemoFile{}) || (name == "canceled" && !errors.Is(err, context.Canceled)) {
				t.Fatal("read failure returned partial description or lost cancellation")
			}
		})
	}
}

func TestDemoFileDefaultsIsolation(t *testing.T) {
	t.Setenv("BUILD_JOBS", "72")
	t.Setenv("RUN_TIMEOUTSECONDS", "1")
	t.Setenv("REPO", "/wrong/environment")
	first, err := LoadDemoFile(t.Context(), demoConfigPath(t, fullDemoYAML))
	if err != nil || first.Build.Jobs != 2 {
		t.Fatal(err)
	}
	minimal := "version: 1\nbuild:\n  repo: '$HOME/relative/repo'\n  attempt: example\nrun:\n  nvkeCheckout: '~/relative/installer'\n  out: relative/out\n"
	for range 2 {
		file, err := LoadDemoFile(t.Context(), demoConfigPath(t, minimal))
		if err != nil {
			t.Fatal(err)
		}
		build, run, err := file.E2EConfig(nil)
		if err != nil || build.Jobs != 1 || build.Repository != "$HOME/relative/repo" || build.NVKEClone != "" || run.Timeout != time.Hour || run.NVKECheckout != "~/relative/installer" || run.Runs != (RunInputs{}) || run.NVKR != "" {
			t.Fatalf("unexpected expansion, environment or previous load state: build=%+v run=%+v error=%v", build, run, err)
		}
	}
	for _, tc := range []struct{ name, contents string }{
		{"jobs zero", strings.Replace(minimal, "  attempt: example", "  attempt: example\n  jobs: 0", 1)},
		{"timeout zero", minimal + "  timeoutSeconds: 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := LoadDemoFile(t.Context(), demoConfigPath(t, tc.contents))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = file.E2EConfig(nil)
			assertConfigUsage(t, err)
		})
	}
}

func TestDemoFileConversionRefusals(t *testing.T) {
	base, err := LoadDemoFile(t.Context(), demoConfigPath(t, fullDemoYAML))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		change    func(*DemoFile)
		operation string
	}{
		{"build missing repo", func(f *DemoFile) { f.Build.Repo = "" }, "build"},
		{"build missing attempt", func(f *DemoFile) { f.Build.Attempt = "" }, "build"},
		{"build jobs negative", func(f *DemoFile) { f.Build.Jobs = -1 }, "build"},
		{"build jobs zero", func(f *DemoFile) { f.Build.Jobs = 0 }, "build"},
		{"build invalid optional clone", func(f *DemoFile) { f.Build.NVKEClone = "\n" }, "build"},
		{"build version", func(f *DemoFile) { f.Version = 2 }, "build"},
		{"run invalid accelerator", func(f *DemoFile) { f.Run.Accelerator = "unavailable" }, "run"},
		{"run invalid optional checkout", func(f *DemoFile) { f.Run.NVKECheckout = "\n" }, "run"},
		{"run out", func(f *DemoFile) { f.Run.Out = "" }, "run"},
		{"run executable", func(f *DemoFile) { f.Run.NVKR = "" }, "run"},
		{"run version", func(f *DemoFile) { f.Version = 0 }, "run"},
		{"run low timeout", func(f *DemoFile) { f.Run.TimeoutSeconds = 299 }, "run"},
		{"run high timeout", func(f *DemoFile) { f.Run.TimeoutSeconds = 7201 }, "run"},
		{"run newline", func(f *DemoFile) { f.Run.Runs.Site = "/native\nforeign" }, "run"},
		{"restore missing", func(f *DemoFile) { f.Restore.Work = "" }, "restore"},
		{"restore version", func(f *DemoFile) { f.Version = 0 }, "restore"},
		{"e2e executable", func(f *DemoFile) { f.Run.Runs = RunFileInputs{} }, "e2e"},
		{"e2e retained runs", func(f *DemoFile) { f.Run.NVKR = "" }, "e2e"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := base
			tc.change(&file)
			var err error
			switch tc.operation {
			case "build":
				_, err = file.BuildConfig(nil)
			case "run":
				_, err = file.RunConfig(nil)
			case "restore":
				_, err = file.RestoreWork()
			case "e2e":
				_, _, err = file.E2EConfig(nil)
			}
			assertConfigUsage(t, err)
		})
	}
	for _, name := range []string{"os", "components", "providers", "site", "tools", "profiles"} {
		t.Run("missing "+name, func(t *testing.T) {
			file := base
			fields := map[string]*string{"os": &file.Run.Runs.OS, "components": &file.Run.Runs.Components, "providers": &file.Run.Runs.Providers, "site": &file.Run.Runs.Site, "tools": &file.Run.Runs.Tools, "profiles": &file.Run.Runs.Profiles, "simulator": &file.Run.Runs.Simulator}
			*fields[name] = ""
			_, err := file.RunConfig(nil)
			assertConfigUsage(t, err)
		})
	}
}

func TestDemoFileTenantBudgetBoundaries(t *testing.T) {
	for _, tc := range []struct {
		seconds string
		want    time.Duration
	}{
		{"299", 0}, {"300", 5 * time.Minute}, {"3600", time.Hour},
		{"7200", 2 * time.Hour}, {"7201", 0}, {"9223372036854775807", 0},
	} {
		t.Run(tc.seconds, func(t *testing.T) {
			contents := strings.Replace(fullDemoYAML, "timeoutSeconds: 900", "timeoutSeconds: "+tc.seconds, 1)
			file, err := LoadDemoFile(t.Context(), demoConfigPath(t, contents))
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"run", "e2e"} {
				t.Run(operation, func(t *testing.T) {
					selected := file
					var run Config
					if operation == "e2e" {
						selected.Run.NVKR = ""
						selected.Run.Runs = RunFileInputs{}
						_, run, err = selected.E2EConfig(nil)
					} else {
						run, err = selected.RunConfig(nil)
					}
					if tc.want == 0 {
						assertConfigUsage(t, err)
						if run != (Config{}) {
							t.Fatal("invalid budget returned partial inputs")
						}
					} else if err != nil || run.Timeout != tc.want {
						t.Fatalf("timeout=%s want=%s error=%v", run.Timeout, tc.want, err)
					}
				})
			}
		})
	}
}

func TestInstallerCheckoutOptionalForBuildAndDeploy(t *testing.T) {
	for _, checkout := range []string{"", "/native/installer", "\n"} {
		t.Run("checkout="+checkout, func(t *testing.T) {
			file := DemoFile{Version: 1, Build: BuildFile{Repo: "/native/repo", Attempt: "fresh", Jobs: 2}, Run: RunFile{NVKECheckout: checkout, Out: "/native/output", TimeoutSeconds: 3600}}
			_, run, err := file.E2EConfig(nil)
			if checkout == "\n" {
				assertConfigUsage(t, err)
				return
			}
			if err != nil || run.NVKECheckout != checkout {
				t.Fatalf("run=%+v error=%v", run, err)
			}
			file.Run.NVKR = "/native/nvkr"
			file.Run.Runs = RunFileInputs{OS: "run", Components: "run", Providers: "run", Site: "run", Tools: "run", Profiles: "run", Simulator: "run"}
			_, err = file.RunConfig(nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDemoFileMaintainedExample(t *testing.T) {
	file, err := LoadDemoFile(t.Context(), "../../demos/local-nvke.yaml")
	if err != nil {
		t.Fatal(err)
	}
	build, run, err := file.E2EConfig(nil)
	if err != nil || build.Jobs != 2 || build.AttemptPrefix != "demo-e2e-01" || run.Timeout != time.Hour || run.Runs != (RunInputs{}) || run.NVKR != "" {
		t.Fatalf("maintained example does not describe a fresh e2e: build=%+v run=%+v error=%v", build, run, err)
	}
}

func TestCombinedArtifactRunNeedsOnlyNativeTools(t *testing.T) {
	file := DemoFile{Version: 1, Run: RunFile{NVKR: "nvkr", Out: "out", TimeoutSeconds: 3600, Runs: RunFileInputs{Artifacts: "built-artifacts", Tools: "built-native-tools"}}}
	cfg, err := file.RunConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NVKECheckout != "" || cfg.Runs.Simulator != "" {
		t.Fatal("native deployment added an old installer or simulator dependency")
	}
	for _, root := range []string{cfg.Runs.OS, cfg.Runs.Components, cfg.Runs.Providers, cfg.Runs.Site, cfg.Runs.Profiles} {
		if root != "built-artifacts" {
			t.Fatalf("combined Run binding changed: %+v", cfg.Runs)
		}
	}
	if cfg.Runs.Tools != "built-native-tools" {
		t.Fatal("native tools Run changed")
	}
	_, _, err = file.E2EConfig(nil)
	assertConfigUsage(t, err)
}
