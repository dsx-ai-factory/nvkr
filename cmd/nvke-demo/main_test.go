// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
				cfg := nvkedemo.Config{DemoExecutable: "/caller-supplied-executable", Accelerator: "tcg", Cleanup: cleanup, Network: "upstream-ovn", Out: "/selected-output", Timeout: time.Minute, Progress: &progress}
				var expected error
				if fails {
					expected = nvkrerr.New(nvkrerr.CodeFetchFailed, "fixture runtime refusal")
				}
				ctx := t.Context()
				calls := 0
				action := withOwnDemoExecutable(func(selected context.Context, actual nvkedemo.Config) error {
					calls++
					if selected != ctx || actual.DemoExecutable != executable || actual.Accelerator != "tcg" || actual.Cleanup != cfg.Cleanup || actual.Network != cfg.Network || actual.Out != cfg.Out || actual.Timeout != cfg.Timeout || actual.Progress != cfg.Progress {
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
					expectedCalls = 2
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
	for _, name := range []string{"exact", "omittedInstaller", "retainedRun", "retainedTool", "buildFailure", "runFailure"} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			builds, runs := 0, 0
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
				build: func(ctx context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					builds++
					workContext = ctx
					assertE2EBudget(t, ctx)
					if cfg.AttemptPrefix != "fresh-prefix" || cfg.Jobs != 3 {
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
						expectedInstaller = ""
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
				if builds != 0 || runs != 0 {
					t.Fatal("conflicting references started work")
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
		build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
			order = append(order, "build")
			if cfg.Repository != "repo" || cfg.AttemptPrefix != "fresh" || cfg.Jobs != 1 {
				t.Fatalf("wrong build flags: %+v", cfg)
			}
			return fresh, nil
		},
		run: func(_ context.Context, cfg nvkedemo.Config) error {
			order = append(order, "run")
			if cfg.Timeout != time.Hour || cfg.NVKECheckout != "" || cfg.Out != "new-out" || cfg.NVKR != fresh.NVKR || cfg.Runs != fresh.Runs {
				t.Fatalf("not fresh derived inputs: %+v", cfg)
			}
			return nil
		},
	}
	err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), []string{"e2e", "--repo", "repo", "--attempt", "fresh", "--out", "new-out"})
	assertCode(t, err, "")
	if strings.Join(order, ",") != "build,run" {
		t.Fatalf("wrong operation order: %v", order)
	}
}

func TestE2EPreservesExplicitTCGThroughAdmissionBuildAndOwnExecutable(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	actions := demoActions{
		admit: func(_ context.Context, cfg nvkedemo.Config) error {
			order = append(order, "admit")
			if cfg.Accelerator != "tcg" {
				t.Fatal("TCG lost before build")
			}
			return nil
		},
		build: func(context.Context, nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
			order = append(order, "build")
			return nvkedemo.BuiltInputs{NVKR: "fresh-nvkr", Runs: nvkedemo.RunInputs{OS: "fresh-os", Tools: "fresh-tools"}}, nil
		},
		run: withOwnDemoExecutable(func(_ context.Context, cfg nvkedemo.Config) error {
			order = append(order, "run")
			if cfg.Accelerator != "tcg" || cfg.DemoExecutable != executable || cfg.Runs.OS != "fresh-os" {
				t.Fatal("TCG or adapter executable lost after build")
			}
			return nil
		}),
	}
	err = executeRoot(t.Context(), newCommand(io.Discard, io.Discard, actions), []string{"e2e", "--repo", "repo", "--attempt", "fresh", "--out", "fresh-out", "--accelerator", "tcg"})
	if err != nil || strings.Join(order, ",") != "admit,build,run" {
		t.Fatalf("TCG handoff order=%v err=%v", order, err)
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
					wantCalls = 2
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

func TestProductionRunCannotSelectKindSiteWorkflow(t *testing.T) {
	root := t.TempDir()
	// Supply only the platform's version prerequisite so this test still
	// reaches real native artifact selection without starting a backend.
	version := "2.2.1"
	if runtime.GOOS == "linux" {
		version = "f47461e"
	}
	for name, body := range map[string]string{
		"limactl":      "#!/bin/sh\nset -eu\ntest \"$#\" = 1\ntest \"$1\" = --version\ntest -n \"$HOME\"\nprintf 'limactl version " + version + "\\n'\n",
		"nerdctl.lima": "#!/bin/sh\nexit 89\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, diagnostics bytes.Buffer
	err := execute(t.Context(), []string{"run", "--plain", "--artifacts-run", filepath.Join(root, "missing-artifacts"), "--tools-run", filepath.Join(root, "missing-tools"), "--nvkr", filepath.Join(root, "nvkr"), "--out", filepath.Join(root, "demo")}, &out, &diagnostics)
	if err == nil {
		t.Fatal("missing artifacts unexpectedly started deployment")
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	if platform != "darwin/arm64" && platform != "linux/arm64" {
		assertCode(t, err, nvkrerr.CodeEngineCapability)
		if !strings.Contains(err.Error(), "native demo does not support host") {
			t.Fatalf("unsupported host selected another deployment owner: %v", err)
		}
	} else if strings.Contains(err.Error(), "native guests require") {
		// The real native capacity boundary can reject a constrained test host
		// before artifact selection; it must not dispatch another backend.
		assertCode(t, err, nvkrerr.CodeEngineCapability)
	} else if admissionErr := nvkedemo.AdmitNativeDeployment(t.Context(), ""); admissionErr != nil {
		assertCode(t, err, nvkrerr.CodeEngineCapability)
		assertCode(t, admissionErr, nvkrerr.CodeEngineCapability)
		if err.Error() != admissionErr.Error() {
			t.Fatalf("run lost its native capability refusal: %v; admission: %v", err, admissionErr)
		}
	} else {
		assertCode(t, err, nvkrerr.CodePreflightFailed)
		if !strings.Contains(err.Error(), "select native deployment from NVKR outputs") {
			t.Fatalf("standard run selected another deployment owner: %v", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
		t.Fatal("missing native artifacts reached deployment work", err)
	}
}

func TestE2EAdmitsNativeVMRuntimeBeforeBuild(t *testing.T) {
	calls := 0
	actions := demoActions{
		load: func(context.Context, string) (nvkedemo.DemoFile, error) {
			file := configuredFixture()
			file.Run.NVKR = ""
			file.Run.Runs = nvkedemo.RunFileInputs{}
			return file, nil
		},
		admit: func(context.Context, nvkedemo.Config) error {
			calls++
			return nvkrerr.New(nvkrerr.CodeEngineCapability, "native runtime unavailable")
		},
		build: func(context.Context, nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
			t.Fatal("unsupported runtime reached BUILD")
			return nvkedemo.BuiltInputs{}, nil
		},
		run: func(context.Context, nvkedemo.Config) error {
			t.Fatal("unsupported runtime reached DEPLOY")
			return nil
		},
	}
	err := executeRoot(t.Context(), newCommand(io.Discard, io.Discard, actions), []string{"e2e", "--config", "explicit.yaml"})
	assertCode(t, err, nvkrerr.CodeEngineCapability)
	if calls != 1 {
		t.Fatal("native admission was not performed once")
	}
}

func TestConformanceOptionKeepsFreshE2EHandoffAndExplicitFalse(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, row := range []struct {
		name       string
		configured bool
		flag       string
		want       bool
	}{
		{"explicit on", false, "true", true}, {"configured on", true, "", true}, {"explicit off", true, "false", false}, {"ordinary", false, "", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			fresh := nvkedemo.BuiltInputs{NVKR: "fresh-nvkr", Runs: nvkedemo.RunInputs{OS: "fresh-artifacts", Components: "fresh-artifacts", Providers: "fresh-artifacts", Site: "fresh-artifacts", Profiles: "fresh-artifacts", Tools: "fresh-native-tools"}}
			actions := demoActions{
				load: func(context.Context, string) (nvkedemo.DemoFile, error) {
					file := configuredFixture()
					file.Run.NVKR = ""
					file.Run.Runs = nvkedemo.RunFileInputs{}
					file.Run.Conformance = row.configured
					return file, nil
				},
				admit: func(_ context.Context, cfg nvkedemo.Config) error {
					if cfg.Conformance != row.want || cfg.Repository != "configured-repo" {
						t.Fatalf("wrong prebuild admission: %+v", cfg)
					}
					return nil
				},
				build: func(ctx context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					calls++
					if cfg.Conformance != row.want {
						t.Fatal("build lost explicit selection")
					}
					deadline, ok := ctx.Deadline()
					ceiling := 8 * time.Hour
					if row.want {
						ceiling = 12 * time.Hour
					}
					if !ok || time.Until(deadline) > ceiling {
						t.Fatal("missing bounded e2e ceiling")
					}
					return fresh, nil
				},
				run: func(_ context.Context, cfg nvkedemo.Config) error {
					calls++
					if cfg.Conformance != row.want || cfg.Runs != fresh.Runs || cfg.NVKR != fresh.NVKR {
						t.Fatalf("conformance lost SAMEfresh handoff: %+v", cfg)
					}
					return nil
				},
			}
			args := []string{"e2e", "--config", "explicit.yaml"}
			if row.flag != "" {
				args = append(args, "--conformance="+row.flag)
			}
			err := executeRoot(t.Context(), newCommand(&out, io.Discard, actions), args)
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func narrationRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, relative := range []string{
		"inputs/definitions/demo/arm64-nvidia-local-kvm.json",
		"inputs/definitions/demo/arm64-upstream-local-kvm.json",
		"inputs/definitions/demo/arm64-nvidia-local-kvm-conformance.json",
		"inputs/definitions/demo/arm64-upstream-local-kvm-conformance.json",
		"inputs/environments/local-arm64-kvm.json",
		"inputs/environments/local-native-mac.json",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, relative)), 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join("..", "..", relative))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, relative), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestDemoNarrationFailuresBeforeExecution(t *testing.T) {
	repo := narrationRepository(t)
	for _, row := range []struct {
		name string
		cfg  nvkedemo.BuildConfig
		out  io.Writer
		code nvkrerr.Code
	}{
		{"unknown network", nvkedemo.BuildConfig{Repository: "/must-not-read", Network: "unknown"}, &bytes.Buffer{}, nvkrerr.CodeUsage},
		{"missing inputs", nvkedemo.BuildConfig{Repository: t.TempDir()}, &bytes.Buffer{}, nvkrerr.CodeInvalidCatalog},
		{"nil writer", nvkedemo.BuildConfig{Repository: repo}, nil, nvkrerr.CodeUsage},
		{"write error", nvkedemo.BuildConfig{Repository: repo}, failedProgressWriter{}, nvkrerr.CodePreflightFailed},
		{"short write", nvkedemo.BuildConfig{Repository: repo}, failedProgressWriter{short: true}, nvkrerr.CodePreflightFailed},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := presentDemoNarration(t.Context(), row.out, row.cfg)
			assertCode(t, err, row.code)
			if strings.Contains(err.Error(), "private writer details") {
				t.Fatal("private writer error escaped")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	assertCode(t, presentDemoNarration(ctx, &out, nvkedemo.BuildConfig{Repository: repo}), nvkrerr.CodeTimeout)
	if out.Len() != 0 {
		t.Fatal("canceled introduction emitted output")
	}
}

func TestDemoNarrationInvalidAuthoredInputsEmitNothing(t *testing.T) {
	environment := "inputs/environments/local-arm64-kvm.json"
	if runtime.GOOS == "darwin" {
		environment = "inputs/environments/local-native-mac.json"
	}
	for _, name := range []string{"inputs/definitions/demo/arm64-nvidia-local-kvm.json", environment} {
		t.Run(name, func(t *testing.T) {
			repo := narrationRepository(t)
			if err := os.WriteFile(filepath.Join(repo, name), []byte(`{"unknown":"rejected"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			assertCode(t, presentDemoNarration(t.Context(), &out, nvkedemo.BuildConfig{Repository: repo}), nvkrerr.CodeInvalidCatalog)
			if out.Len() != 0 {
				t.Fatal("invalid authored input emitted an introduction")
			}
		})
	}
}

func TestE2EWalkthroughIntroductionAdmissionAndOrder(t *testing.T) {
	repo := narrationRepository(t)
	for _, row := range []struct {
		name, network, definition                     string
		invalid, outputFailure, conformance, explicit bool
	}{
		{name: "admitted", definition: "arm64-nvidia-local-kvm.json"},
		{name: "upstream", network: "upstream-ovn", definition: "arm64-upstream-local-kvm.json"},
		{name: "conformance", conformance: true, definition: "arm64-nvidia-local-kvm-conformance.json"},
		{name: "upstream conformance", network: "upstream-ovn", conformance: true, definition: "arm64-upstream-local-kvm-conformance.json"},
		{name: "explicit selection", explicit: true, definition: "arm64-upstream-local-kvm.json"},
		{name: "invalid config", invalid: true},
		{name: "output failure", outputFailure: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			var out bytes.Buffer
			var writer io.Writer = &out
			if row.outputFailure {
				writer = failedProgressWriter{}
			}
			calls := 0
			actions := demoActions{build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
				calls++
				if out.Len() == 0 {
					t.Fatal("runtime admission preceded narration")
				}
				definition := filepath.Join("inputs/definitions/demo", row.definition)
				environment := "inputs/environments/local-arm64-kvm.json"
				if runtime.GOOS == "darwin" {
					environment = "inputs/environments/local-native-mac.json"
				}
				if row.explicit {
					if cfg.Definition != definition || cfg.Environment != "inputs/environments/local-arm64-kvm.json" || cfg.NVKR != "/explicit/nvkr" {
						t.Fatal("explicit build selection changed", cfg)
					}
					definition = filepath.Join(repo, definition)
					environment = filepath.Join(repo, cfg.Environment)
				}
				if !strings.Contains(out.String(), fmt.Sprintf("%q", definition)) || !strings.Contains(out.String(), fmt.Sprintf("%q", environment)) {
					t.Fatalf("narration differs from build selection: Definition=%s Environment=%s", definition, environment)
				}
				return nvkedemo.BuiltInputs{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "bounded test stops before runtime")
			}}
			args := []string{"e2e", "--repo", repo, "--attempt", "fresh-01", "--out", filepath.Join(repo, "fresh-output"), "--walkthrough"}
			if row.network != "" {
				args = append(args, "--network", row.network)
			}
			if row.conformance {
				args = append(args, "--conformance")
			}
			if row.explicit {
				args = append(args, "--definition", filepath.Join("inputs/definitions/demo", row.definition), "--environment", "inputs/environments/local-arm64-kvm.json", "--nvkr", "/explicit/nvkr")
			}
			if row.invalid {
				args = append(args, "--network", "unknown")
			}
			err := executeRoot(t.Context(), newCommand(writer, io.Discard, actions), args)
			want := nvkrerr.CodeEngineCapability
			if row.invalid {
				want = nvkrerr.CodeUsage
			}
			if row.outputFailure {
				want = nvkrerr.CodePreflightFailed
			}
			assertCode(t, err, want)
			if calls != map[bool]int{true: 0, false: 1}[row.invalid || row.outputFailure] {
				t.Fatal("unexpected execution", calls)
			}
			if row.invalid && out.Len() != 0 {
				t.Fatal("invalid configuration emitted introduction")
			}
			for _, name := range []string{".nvkr", "fresh-output"} {
				if _, err := os.Stat(filepath.Join(repo, name)); !os.IsNotExist(err) {
					t.Fatal("presentation wrote runtime state", err)
				}
			}
		})
	}
}
