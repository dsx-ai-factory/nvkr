// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

const fullDemoYAML = `version: 1
build:
  repo: /native/nke-recipes
  nkeClone: /native/nke-source
  attempt: example-01
  jobs: 2
run:
  nkeCheckout: /native/nke-installer
  nkdctl: /native/nkdctl
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
	var coded *nkderr.Error
	if !errors.As(err, &coded) || coded.Code != nkderr.CodeUsage {
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
	if err != nil || build != (BuildConfig{Repository: "/native/nke-recipes", NKEClone: "/native/nke-source", AttemptPrefix: "example-01", Jobs: 2, Progress: &progress}) {
		t.Fatalf("build=%+v error=%v", build, err)
	}
	run, err := file.RunConfig(&progress)
	expected := Config{NKECheckout: "/native/nke-installer", NKDCTL: "/native/nkdctl", Out: "/native/output", Timeout: 900 * time.Second, Progress: &progress, Runs: RunInputs{
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
		{"nonstring key", "version: 1\nrestore:\n  12: secret-config-sentinel\n"},
		{"alias", "version: 1\nbuild:\n  repo: &path /native\n  nkeClone: *path\n"},
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
	minimal := "version: 1\nbuild:\n  repo: '$HOME/relative/repo'\n  attempt: example\nrun:\n  nkeCheckout: '~/relative/installer'\n  out: relative/out\n"
	for range 2 {
		file, err := LoadDemoFile(t.Context(), demoConfigPath(t, minimal))
		if err != nil {
			t.Fatal(err)
		}
		build, run, err := file.E2EConfig(nil)
		if err != nil || build.Jobs != 1 || build.Repository != "$HOME/relative/repo" || build.NKEClone != "" || run.Timeout != time.Hour || run.NKECheckout != "~/relative/installer" || run.Runs != (RunInputs{}) || run.NKDCTL != "" {
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
		{"build invalid optional clone", func(f *DemoFile) { f.Build.NKEClone = "\n" }, "build"},
		{"build version", func(f *DemoFile) { f.Version = 2 }, "build"},
		{"run checkout", func(f *DemoFile) { f.Run.NKECheckout = "" }, "run"},
		{"run out", func(f *DemoFile) { f.Run.Out = "" }, "run"},
		{"run executable", func(f *DemoFile) { f.Run.NKDCTL = "" }, "run"},
		{"run version", func(f *DemoFile) { f.Version = 0 }, "run"},
		{"run low timeout", func(f *DemoFile) { f.Run.TimeoutSeconds = 299 }, "run"},
		{"run high timeout", func(f *DemoFile) { f.Run.TimeoutSeconds = 3601 }, "run"},
		{"run newline", func(f *DemoFile) { f.Run.Runs.Site = "/native\nforeign" }, "run"},
		{"restore missing", func(f *DemoFile) { f.Restore.Work = "" }, "restore"},
		{"restore version", func(f *DemoFile) { f.Version = 0 }, "restore"},
		{"e2e executable", func(f *DemoFile) { f.Run.Runs = RunFileInputs{} }, "e2e"},
		{"e2e retained runs", func(f *DemoFile) { f.Run.NKDCTL = "" }, "e2e"},
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
	for _, name := range []string{"os", "components", "providers", "site", "tools", "profiles", "simulator"} {
		t.Run("missing "+name, func(t *testing.T) {
			file := base
			fields := map[string]*string{"os": &file.Run.Runs.OS, "components": &file.Run.Runs.Components, "providers": &file.Run.Runs.Providers, "site": &file.Run.Runs.Site, "tools": &file.Run.Runs.Tools, "profiles": &file.Run.Runs.Profiles, "simulator": &file.Run.Runs.Simulator}
			*fields[name] = ""
			_, err := file.RunConfig(nil)
			assertConfigUsage(t, err)
		})
	}
}

func TestDemoFileMaintainedExample(t *testing.T) {
	file, err := LoadDemoFile(t.Context(), "../../demos/local-nke.yaml")
	if err != nil {
		t.Fatal(err)
	}
	build, run, err := file.E2EConfig(nil)
	if err != nil || build.Jobs != 2 || build.AttemptPrefix != "demo-e2e-01" || run.Timeout != time.Hour || run.Runs != (RunInputs{}) || run.NKDCTL != "" {
		t.Fatalf("maintained example does not describe a fresh e2e: build=%+v run=%+v error=%v", build, run, err)
	}
}
