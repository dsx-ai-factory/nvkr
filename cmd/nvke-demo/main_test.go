// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRunActionSelectsOwnExecutableForBothLifetimeModes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil || !filepath.IsAbs(executable) {
		t.Fatal("test executable unavailable", err)
	}
	for _, cleanup := range []bool{false, true} {
		for _, fails := range []bool{false, true} {
			name := map[bool]string{false: "retain", true: "cleanup"}[cleanup] + map[bool]string{false: "/success", true: "/refusal"}[fails]
			t.Run(name, func(t *testing.T) {
				var progress bytes.Buffer
				cfg := nvkedemo.Config{DemoExecutable: "/caller-supplied-executable", Cleanup: cleanup, Network: "upstream-ovn", Out: "/selected-output", Timeout: time.Minute, Progress: &progress}
				var expected error
				if fails {
					expected = nvkrerr.New(nvkrerr.CodeFetchFailed, "fixture runtime refusal")
				}
				ctx := t.Context()
				calls := 0
				action := withOwnDemoExecutable(func(selected context.Context, actual nvkedemo.Config) error {
					calls++
					if selected != ctx || actual.DemoExecutable != executable || actual.Cleanup != cfg.Cleanup || actual.Network != cfg.Network || actual.Out != cfg.Out || actual.Timeout != cfg.Timeout || actual.Progress != cfg.Progress {
						t.Fatal("runtime action did not receive this CLI's executable and unchanged invocation")
					}
					return expected
				})
				result := action(ctx, cfg)
				if result != expected || calls != 1 || cfg.DemoExecutable != "/caller-supplied-executable" || progress.Len() != 0 {
					t.Fatal("executable selection lost the exact result, mutated caller input or performed runtime work", result)
				}
				if fails {
					assertCode(t, result, nvkrerr.CodeFetchFailed)
				}
			})
		}
	}
}

func TestFlagsDoNotExecuteOnHelpCompletionOrInvalidInput(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, test := range []struct {
		name string
		args []string
		code nvkrerr.Code
	}{
		{"empty", nil, nvkrerr.CodeUsage},
		{"unknown", []string{"unknown"}, nvkrerr.CodeUsage},
		{"root-help", []string{"--help"}, ""},
		{"help-command", []string{"help", "run"}, ""},
		{"run-help", []string{"run", "--help"}, ""},
		{"build-help", []string{"build", "--help"}, ""},
		{"verbose-build-help", []string{"build", "--verbose", "--help"}, ""},
		{"restore-help", []string{"restore", "--help"}, ""},
		{"e2e-help", []string{"e2e", "--help"}, ""},
		{"configured-help", []string{"run", "--config", "/must-not-read", "--help"}, ""},
		{"removed-progress-flag", []string{"run", "--config", "/must-not-read", "--progress", "tui"}, nvkrerr.CodeUsage},
		{"root-badflag", []string{"--wrong"}, nvkrerr.CodeUsage},
		{"run-badflag", []string{"run", "--wrong"}, nvkrerr.CodeUsage},
		{"build-badflag", []string{"build", "--wrong"}, nvkrerr.CodeUsage},
		{"restore-badflag", []string{"restore", "--wrong"}, nvkrerr.CodeUsage},
		{"run-position", []string{"run", "unexpected"}, nvkrerr.CodeUsage},
		{"build-position", []string{"build", "unexpected"}, nvkrerr.CodeUsage},
		{"restore-position", []string{"restore", "unexpected"}, nvkrerr.CodeUsage},
		{"e2e-position", []string{"e2e", "unexpected"}, nvkrerr.CodeUsage},
		{"probe-position", []string{"network-port-probe", "53"}, nvkrerr.CodeUsage},
		{"probe-address", []string{"network-port-probe", "--address", "127.0.0.1"}, nvkrerr.CodeUsage},
		{"probe-config", []string{"network-port-probe", "--config", "/must-not-read"}, nvkrerr.CodeUsage},
		{"probe-verbose", []string{"network-port-probe", "--verbose"}, nvkrerr.CodeUsage},
		{"probe-plain", []string{"network-port-probe", "--plain"}, nvkrerr.CodeUsage},
		{"probe-help", []string{"network-port-probe", "--help"}, ""},
		{"e2e-retained-input-flag", []string{"e2e", "--os-run", "retained"}, nvkrerr.CodeUsage},
		{"config-empty", []string{"run", "--config="}, nvkrerr.CodeUsage},
		{"restore-missing", []string{"restore"}, nvkrerr.CodeUsage},
		{"restore-empty", []string{"restore", "--work="}, nvkrerr.CodeUsage},
		{"bad-timeout-type", []string{"run", "--timeout", "not-seconds"}, nvkrerr.CodeUsage},
		{"missing-timeout-value", []string{"run", "--timeout"}, nvkrerr.CodeUsage},
		{"invalid-network", []string{"run", "--network", "unknown"}, nvkrerr.CodeUsage},
		{"invalid-e2e-network", []string{"e2e", "--network", "unknown"}, nvkrerr.CodeUsage},
		{"invalid-budget-low", []string{"run", "--timeout", "1"}, nvkrerr.CodeUsage},
		{"invalid-budget-high", []string{"run", "--timeout", "7201"}, nvkrerr.CodeUsage},
		{"invalid-budget-overflow", []string{"run", "--timeout", "9223372036854775807"}, nvkrerr.CodeUsage},
		{"invalid-e2e-budget-low", []string{"e2e", "--config", "/must-not-read", "--timeout", "299"}, nvkrerr.CodeUsage},
		{"invalid-e2e-budget-high", []string{"e2e", "--config", "/must-not-read", "--timeout", "7201"}, nvkrerr.CodeUsage},
		{"invalid-e2e-budget-overflow", []string{"e2e", "--config", "/must-not-read", "--timeout", "9223372036854775807"}, nvkrerr.CodeUsage},
		{"completion-help", []string{"completion", "--help"}, ""},
		{"completion-bash", []string{"completion", "bash"}, ""},
		{"configured-completion", []string{"--config", "/must-not-read", "completion", "bash"}, ""},
		{"completion-zsh", []string{"completion", "zsh"}, ""},
		{"completion-fish", []string{"completion", "fish"}, ""},
		{"completion-powershell", []string{"completion", "powershell"}, ""},
		{"completion-unknown", []string{"completion", "unknown"}, nvkrerr.CodeUsage},
		{"completion-position", []string{"completion", "bash", "unexpected"}, nvkrerr.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			actions := demoActions{
				run: func(context.Context, nvkedemo.Config) error { t.Fatal("invalid input reached deployment"); return nil },
				build: func(context.Context, nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					t.Fatal("invalid input reached build")
					return nvkedemo.BuiltInputs{}, nil
				},
				restore: func(context.Context, string, io.Writer) error {
					t.Fatal("invalid input reached restoration")
					return nil
				},
				load: func(context.Context, string) (nvkedemo.DemoFile, error) {
					t.Fatal("help or invalid input read configuration")
					return nvkedemo.DemoFile{}, nil
				},
			}
			err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), test.args)
			assertCode(t, err, test.code)
			if bytes.Contains(out.Bytes(), []byte("PASS:")) {
				t.Fatal("flag handling falsely reported acceptance")
			}
			if test.code == "" && out.Len() == 0 {
				t.Fatal("missing help or completion output")
			}
			if entries, err := os.ReadDir(os.Getenv("TMPDIR")); err != nil || len(entries) != 0 {
				t.Fatal("help or invalid input created build logs")
			}
		})
	}
}

func TestDeploymentNetworkConfigAndFlagSelection(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, tc := range []struct {
		name, configured, flag, expected string
	}{
		{"default", "", "", "ovn"},
		{"configured diagnostic", "kind-calico", "", "kind-calico"},
		{"configured upstream", "upstream-ovn", "", "upstream-ovn"},
		{"flag upstream", "ovn", "upstream-ovn", "upstream-ovn"},
		{"flag diagnostic", "ovn", "kind-calico", "kind-calico"},
		{"explicit maintained override", "kind-calico", "ovn", "ovn"},
		{"invalid configured", "unknown", "", ""},
		{"invalid flag", "ovn", "unknown", ""},
	} {
		for _, operation := range []string{"run", "e2e"} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				calls := 0
				actions := demoActions{
					load: func(context.Context, string) (nvkedemo.DemoFile, error) {
						file := configuredFixture()
						file.Run.Network = tc.configured
						if operation == "e2e" {
							file.Run.NVKR = ""
							file.Run.Runs = nvkedemo.RunFileInputs{}
						}
						return file, nil
					},
					prepare: func(_ context.Context, _ nvkedemo.BuildConfig, cfg nvkedemo.Config) (nvkedemo.Config, error) {
						calls++
						if cfg.Network != tc.expected {
							t.Fatalf("prepare network=%q want=%q", cfg.Network, tc.expected)
						}
						return cfg, nil
					},
					build: func(context.Context, nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
						calls++
						return nvkedemo.BuiltInputs{NVKR: "fresh"}, nil
					},
					run: func(_ context.Context, cfg nvkedemo.Config) error {
						calls++
						if cfg.Network != tc.expected {
							t.Fatalf("run network=%q want=%q", cfg.Network, tc.expected)
						}
						return nil
					},
				}
				args := []string{operation, "--config", "explicit.yaml"}
				if tc.flag != "" {
					args = append(args, "--network", tc.flag)
				}
				var out bytes.Buffer
				err := executeRoot(t.Context(), newCommand(&out, io.Discard, actions), args)
				if tc.expected == "" {
					assertCode(t, err, nvkrerr.CodeUsage)
					if calls != 0 || out.Len() != 0 {
						t.Fatal("invalid network started work or progress")
					}
					return
				}
				assertCode(t, err, "")
				expectedCalls := 1
				if operation == "e2e" {
					expectedCalls = 3
				}
				if calls != expectedCalls {
					t.Fatalf("calls=%d want=%d", calls, expectedCalls)
				}
			})
		}
	}
}

func TestNetworkPortHelperPlatformAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := executeRoot(ctx, newCommand(io.Discard, io.Discard, demoActions{}), []string{"network-port-probe"})
	assertCode(t, err, nvkrerr.CodePreflightFailed)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("helper cancellation lost: %v", err)
	}
	root := newCommand(io.Discard, io.Discard, demoActions{})
	command, _, err := root.Find([]string{"network-port-probe"})
	if err != nil || !command.Hidden {
		t.Fatal("network helper must remain hidden", err)
	}
}

func configuredFixture() nvkedemo.DemoFile {
	return nvkedemo.DemoFile{Version: 1,
		Build: nvkedemo.BuildFile{Repo: "configured-repo", NVKEClone: "configured-clone", Attempt: "configured-attempt", Jobs: 3},
		Run: nvkedemo.RunFile{NVKECheckout: "configured-installer", NVKR: "configured-nvkr", Out: "configured-out", TimeoutSeconds: 600,
			Runs: nvkedemo.RunFileInputs{OS: "os", Components: "components", Providers: "providers", Site: "site", Tools: "tools", Profiles: "profiles", Simulator: "simulator"}},
		Restore: nvkedemo.RestoreFile{Work: "configured-work"},
	}
}

func TestExplicitConfigOnlyChangedFlagsOverrideIt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, name := range []string{"run", "build", "restore", "explicit-empty", "config-failure"} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			loads, executions := 0, 0
			actions := demoActions{
				load: func(ctx context.Context, path string) (nvkedemo.DemoFile, error) {
					loads++
					if ctx != t.Context() || path != "explicit.yaml" {
						t.Fatal("wrong explicit load")
					}
					if name == "config-failure" {
						return nvkedemo.DemoFile{}, nvkrerr.New(nvkrerr.CodeUsage, "unknown YAML field")
					}
					return configuredFixture(), nil
				},
				run: func(_ context.Context, cfg nvkedemo.Config) error {
					executions++
					if cfg.Out != "flag-out" || cfg.Timeout != 600*time.Second || cfg.NVKR != "configured-nvkr" || cfg.NVKECheckout != "configured-installer" {
						t.Fatalf("unchanged defaults overwrote config: %+v", cfg)
					}
					return nil
				},
				build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					executions++
					if cfg.AttemptPrefix != "flag-attempt" || cfg.Jobs != 3 || cfg.Repository != "configured-repo" {
						t.Fatalf("lost config: %+v", cfg)
					}
					return nvkedemo.BuiltInputs{NVKR: "built"}, nil
				},
				restore: func(_ context.Context, work string, _ io.Writer) error {
					executions++
					if work != "flag-work" {
						t.Fatal("missing explicit restore override")
					}
					return nil
				},
			}
			args := []string{"run", "--config", "explicit.yaml", "--out", "flag-out"}
			switch name {
			case "build":
				args = []string{"--config", "explicit.yaml", "build", "--attempt", "flag-attempt"}
			case "restore":
				args = []string{"restore", "--config", "explicit.yaml", "--work", "flag-work"}
			case "explicit-empty":
				args = []string{"run", "--config", "explicit.yaml", "--nvkr="}
			}
			err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args)
			if name == "explicit-empty" || name == "config-failure" {
				assertCode(t, err, nvkrerr.CodeUsage)
				if executions != 0 {
					t.Fatal("invalid config executed")
				}
			} else {
				assertCode(t, err, "")
				if executions != 1 {
					t.Fatal("wrong selected operation")
				}
			}
			if loads != 1 {
				t.Fatal("configuration not loaded exactly once")
			}
		})
	}
}

func TestE2EBuildsBeforeDeploymentAndRejectsRetainedInputs(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, name := range []string{"exact", "omittedInstaller", "retainedRun", "retainedTool", "prepareFailure", "buildFailure", "runFailure"} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			prepares, builds, runs := 0, 0, 0
			var workContext context.Context
			fresh := nvkedemo.BuiltInputs{NVKR: "fresh-nvkr", Runs: nvkedemo.RunInputs{OS: "fresh-os", Components: "fresh-components", Providers: "fresh-components", Site: "fresh-site", Tools: "fresh-tools", Profiles: "fresh-profiles", Simulator: "fresh-simulator"}}
			actions := demoActions{
				load: func(context.Context, string) (nvkedemo.DemoFile, error) {
					file := configuredFixture()
					file.Run.NVKR = ""
					file.Run.Runs = nvkedemo.RunFileInputs{}
					if name == "omittedInstaller" {
						file.Run.NVKECheckout = ""
					}
					if name == "retainedRun" {
						file.Run.Runs.OS = "retained"
					}
					if name == "retainedTool" {
						file.Run.NVKR = "retained"
					}
					return file, nil
				},
				prepare: func(ctx context.Context, build nvkedemo.BuildConfig, cfg nvkedemo.Config) (nvkedemo.Config, error) {
					prepares++
					assertE2EBudget(t, ctx)
					if builds != 0 || runs != 0 || build.AttemptPrefix != "fresh-prefix" || build.Jobs != 3 {
						t.Fatal("installer preparation must precede the build")
					}
					if name == "prepareFailure" {
						return nvkedemo.Config{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "installer failed")
					}
					if name == "omittedInstaller" {
						if cfg.NVKECheckout != "" {
							t.Fatal("omitted checkout was fabricated by configuration")
						}
						cfg.NVKECheckout = "prepared-installer"
					}
					return cfg, nil
				},
				build: func(ctx context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					builds++
					workContext = ctx
					assertE2EBudget(t, ctx)
					if prepares != 1 || cfg.AttemptPrefix != "fresh-prefix" || cfg.Jobs != 3 {
						t.Fatal("wrong build intent")
					}
					if name == "buildFailure" {
						return nvkedemo.BuiltInputs{}, nvkrerr.New(nvkrerr.CodeBuildFailed, "build failed")
					}
					return fresh, nil
				},
				run: func(ctx context.Context, cfg nvkedemo.Config) error {
					runs++
					assertE2EBudget(t, ctx)
					expectedInstaller := "configured-installer"
					if name == "omittedInstaller" {
						expectedInstaller = "prepared-installer"
					}
					if builds != 1 || ctx != workContext || cfg.Runs != fresh.Runs || cfg.NVKR != fresh.NVKR || cfg.NVKECheckout != expectedInstaller || cfg.Timeout != 600*time.Second {
						t.Fatalf("deployment not bound to fresh build: %+v", cfg)
					}
					if name == "runFailure" {
						return nvkrerr.New(nvkrerr.CodePreflightFailed, "tenant failed")
					}
					return nil
				},
			}
			err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), []string{"e2e", "--config", "explicit.yaml", "--attempt", "fresh-prefix"})
			switch name {
			case "retainedRun", "retainedTool":
				assertCode(t, err, nvkrerr.CodeUsage)
				if prepares != 0 || builds != 0 || runs != 0 {
					t.Fatal("conflicting references started work")
				}
			case "prepareFailure":
				assertCode(t, err, nvkrerr.CodePreflightFailed)
				if prepares != 1 || builds != 0 || runs != 0 {
					t.Fatal("build followed failed installer preparation")
				}
			case "buildFailure":
				assertCode(t, err, nvkrerr.CodeBuildFailed)
				if builds != 1 || runs != 0 {
					t.Fatal("deployment followed failed build")
				}
			case "runFailure":
				assertCode(t, err, nvkrerr.CodePreflightFailed)
				if builds != 1 || runs != 1 {
					t.Fatal("wrong operation order")
				}
			default:
				assertCode(t, err, "")
				if builds != 1 || runs != 1 {
					t.Fatal("missing full workflow")
				}
			}
			if strings.Contains(out.String(), "PASS:") {
				t.Fatal("CLI fabricated real runtime acceptance")
			}
			if workContext != nil && workContext.Err() != context.Canceled {
				t.Fatal("e2e context not canceled after completion")
			}
		})
	}
}

func assertE2EBudget(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) > 8*time.Hour || ctx.Err() != nil {
		t.Fatal("missing active bounded e2e context")
	}
}

func TestE2EWithoutConfigKeepsFlagDefaultsAndFreshReferences(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var out, diagnostics bytes.Buffer
	fresh := nvkedemo.BuiltInputs{NVKR: "new-nvkr", Runs: nvkedemo.RunInputs{OS: "new-os", Components: "new-components", Providers: "new-components", Site: "new-site", Tools: "new-tools", Profiles: "new-profiles", Simulator: "new-simulator"}}
	order := make([]string, 0, 3)
	actions := demoActions{
		load: func(context.Context, string) (nvkedemo.DemoFile, error) {
			t.Fatal("searched for implicit configuration")
			return nvkedemo.DemoFile{}, nil
		},
		prepare: func(_ context.Context, _ nvkedemo.BuildConfig, cfg nvkedemo.Config) (nvkedemo.Config, error) {
			order = append(order, "prepare")
			if cfg.NVKECheckout != "" {
				t.Fatal("no explicit installer was provided")
			}
			cfg.NVKECheckout = "installer"
			return cfg, nil
		},
		build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
			order = append(order, "build")
			if cfg.Repository != "repo" || cfg.AttemptPrefix != "fresh" || cfg.Jobs != 1 {
				t.Fatalf("wrong build flags: %+v", cfg)
			}
			return fresh, nil
		},
		run: func(_ context.Context, cfg nvkedemo.Config) error {
			order = append(order, "run")
			if cfg.Timeout != time.Hour || cfg.NVKECheckout != "installer" || cfg.Out != "new-out" || cfg.NVKR != fresh.NVKR || cfg.Runs != fresh.Runs {
				t.Fatalf("not fresh derived inputs: %+v", cfg)
			}
			return nil
		},
	}
	err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), []string{"e2e", "--repo", "repo", "--attempt", "fresh", "--out", "new-out"})
	assertCode(t, err, "")
	if strings.Join(order, ",") != "prepare,build,run" {
		t.Fatalf("wrong operation order: %v", order)
	}
}

func assertCode(t *testing.T, err error, want nvkrerr.Code) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v want code=%s", err, want)
	}
}

func TestTypedRunFlagsAndContextReachLibraryUnchanged(t *testing.T) {
	for _, tc := range []struct {
		seconds string
		want    time.Duration
	}{
		{"300", 5 * time.Minute}, {"3600", time.Hour}, {"7200", 2 * time.Hour},
	} {
		t.Run(tc.seconds, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			actions := demoActions{run: func(got context.Context, cfg nvkedemo.Config) error {
				called = true
				if got.Err() != nil || cfg.Progress.(*checkedWriter).out != &out {
					t.Fatal("cancellation context or progress destination lost")
				}
				if cfg.NVKECheckout != "installer" || cfg.NVKR != "nvkr" || cfg.Out != "new-output" || cfg.Runs != (nvkedemo.RunInputs{OS: "os", Components: "components", Providers: "providers", Site: "site", Tools: "tools", Profiles: "profiles", Simulator: "simulator"}) {
					t.Fatalf("lost flags: %+v", cfg)
				}
				if cfg.Timeout != tc.want {
					t.Fatal("wrong timeout")
				}
				return nvkrerr.New(nvkrerr.CodeTimeout, "library canceled")
			}}
			args := []string{"run", "--nvke-checkout", "installer", "--nvkr", "nvkr", "--out", "new-output", "--os-run", "os", "--components-run", "components", "--provider-run", "providers", "--site-run", "site", "--tools-run", "tools", "--profiles-run", "profiles", "--simulator-run", "simulator", "--timeout", tc.seconds}
			err := executeRoot(ctx, newCommand(&out, &diagnostics, actions), args)
			assertCode(t, err, nvkrerr.CodeTimeout)
			if !called {
				t.Fatal("library not called")
			}
		})
	}
}

func TestTenantBudgetConfigAndFlagSelection(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, tc := range []struct {
		name       string
		configured int
		flag       string
		want       time.Duration
	}{
		{"configured minimum", 300, "", 5 * time.Minute},
		{"configured default", 3600, "", time.Hour},
		{"configured maximum", 7200, "", 2 * time.Hour},
		{"override minimum", 7200, "300", 5 * time.Minute},
		{"override default", 7200, "3600", time.Hour},
		{"override maximum", 600, "7200", 2 * time.Hour},
		{"configured low", 299, "", 0},
		{"configured high", 7201, "", 0},
		{"configured integer maximum", int(^uint(0) >> 1), "", 0},
	} {
		for _, operation := range []string{"run", "e2e"} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				calls := 0
				actions := demoActions{
					load: func(context.Context, string) (nvkedemo.DemoFile, error) {
						file := configuredFixture()
						file.Run.TimeoutSeconds = tc.configured
						if operation == "e2e" {
							file.Run.NVKR = ""
							file.Run.Runs = nvkedemo.RunFileInputs{}
						}
						return file, nil
					},
					prepare: func(ctx context.Context, _ nvkedemo.BuildConfig, cfg nvkedemo.Config) (nvkedemo.Config, error) {
						calls++
						assertE2EBudget(t, ctx)
						if cfg.Timeout != tc.want {
							t.Fatalf("prepare timeout=%s want=%s", cfg.Timeout, tc.want)
						}
						return cfg, nil
					},
					build: func(ctx context.Context, _ nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
						calls++
						assertE2EBudget(t, ctx)
						return nvkedemo.BuiltInputs{NVKR: "fresh"}, nil
					},
					run: func(ctx context.Context, cfg nvkedemo.Config) error {
						calls++
						if operation == "e2e" {
							assertE2EBudget(t, ctx)
						}
						if cfg.Timeout != tc.want {
							t.Fatalf("run timeout=%s want=%s", cfg.Timeout, tc.want)
						}
						return nil
					},
				}
				args := []string{operation, "--config", "explicit.yaml"}
				if tc.flag != "" {
					args = append(args, "--timeout", tc.flag)
				}
				var out bytes.Buffer
				err := executeRoot(t.Context(), newCommand(&out, io.Discard, actions), args)
				if tc.want == 0 {
					assertCode(t, err, nvkrerr.CodeUsage)
					if calls != 0 || out.Len() != 0 {
						t.Fatal("invalid budget started work or progress")
					}
					return
				}
				assertCode(t, err, "")
				wantCalls := 1
				if operation == "e2e" {
					wantCalls = 3
				}
				if calls != wantCalls {
					t.Fatalf("calls=%d want=%d", calls, wantCalls)
				}
			})
		}
	}
}

func TestBuildAndRestoreDelegateTypedInputs(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, name := range []string{"build", "build-verbose", "restore", "restore-failure"} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			called := false
			actions := demoActions{
				build: func(ctx context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					called = true
					if ctx.Err() != nil || cfg.Repository != "repo" || cfg.NVKEClone != "clone" || cfg.AttemptPrefix != "fresh" || cfg.Jobs != 2 || cfg.Progress.(*checkedWriter).out != &out {
						t.Fatalf("lost build options: %+v", cfg)
					}
					if cfg.Progress.(*checkedWriter).verbose != (name == "build-verbose") {
						t.Fatal("verbose flag not propagated")
					}
					return nvkedemo.BuiltInputs{NVKR: "built-nvkr"}, nil
				},
				restore: func(ctx context.Context, work string, progress io.Writer) error {
					called = true
					if work != "original-work" || progress.(*checkedWriter).out != &out {
						t.Fatal("lost recovery inputs")
					}
					deadline, bounded := ctx.Deadline()
					if !bounded || time.Until(deadline) > 8*time.Minute {
						t.Fatal("unbounded restoration")
					}
					if name == "restore-failure" {
						return nvkrerr.New(nvkrerr.CodePreflightFailed, "changed controls")
					}
					return nil
				},
			}
			args := []string{"restore", "--work", "original-work"}
			if strings.HasPrefix(name, "build") {
				args = []string{"build", "--repo", "repo", "--nvke-clone", "clone", "--attempt", "fresh", "--jobs", "2"}
				if name == "build-verbose" {
					args = append(args, "--verbose")
				}
			}
			err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args)
			if name == "restore-failure" {
				assertCode(t, err, nvkrerr.CodePreflightFailed)
				if strings.Contains(out.String(), "PASS:") {
					t.Fatal("acceptance emitted after failure")
				}
			} else {
				assertCode(t, err, "")
			}
			if !called {
				t.Fatal("library not called")
			}
		})
	}
}
