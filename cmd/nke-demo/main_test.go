// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestFlagsDoNotExecuteOnHelpCompletionOrInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		code nkderr.Code
	}{
		{"empty", nil, nkderr.CodeUsage},
		{"unknown", []string{"unknown"}, nkderr.CodeUsage},
		{"root-help", []string{"--help"}, ""},
		{"help-command", []string{"help", "run"}, ""},
		{"run-help", []string{"run", "--help"}, ""},
		{"build-help", []string{"build", "--help"}, ""},
		{"restore-help", []string{"restore", "--help"}, ""},
		{"e2e-help", []string{"e2e", "--help"}, ""},
		{"configured-help", []string{"run", "--config", "/must-not-read", "--help"}, ""},
		{"progress-help", []string{"run", "--config", "/must-not-read", "--progress", "invalid", "--help"}, ""},
		{"progress-invalid", []string{"run", "--config", "/must-not-read", "--progress", "invalid"}, nkderr.CodeUsage},
		{"progress-empty", []string{"build", "--config", "/must-not-read", "--progress="}, nkderr.CodeUsage},
		{"root-badflag", []string{"--wrong"}, nkderr.CodeUsage},
		{"run-badflag", []string{"run", "--wrong"}, nkderr.CodeUsage},
		{"build-badflag", []string{"build", "--wrong"}, nkderr.CodeUsage},
		{"restore-badflag", []string{"restore", "--wrong"}, nkderr.CodeUsage},
		{"run-position", []string{"run", "unexpected"}, nkderr.CodeUsage},
		{"build-position", []string{"build", "unexpected"}, nkderr.CodeUsage},
		{"restore-position", []string{"restore", "unexpected"}, nkderr.CodeUsage},
		{"e2e-position", []string{"e2e", "unexpected"}, nkderr.CodeUsage},
		{"e2e-retained-input-flag", []string{"e2e", "--os-run", "retained"}, nkderr.CodeUsage},
		{"config-empty", []string{"run", "--config="}, nkderr.CodeUsage},
		{"restore-missing", []string{"restore"}, nkderr.CodeUsage},
		{"restore-empty", []string{"restore", "--work="}, nkderr.CodeUsage},
		{"bad-timeout-type", []string{"run", "--timeout", "not-seconds"}, nkderr.CodeUsage},
		{"missing-timeout-value", []string{"run", "--timeout"}, nkderr.CodeUsage},
		{"invalid-budget-low", []string{"run", "--timeout", "1"}, nkderr.CodePreflightFailed},
		{"invalid-budget-high", []string{"run", "--timeout", "3601"}, nkderr.CodePreflightFailed},
		{"invalid-budget-overflow", []string{"run", "--timeout", "9223372036854775807"}, nkderr.CodePreflightFailed},
		{"completion-help", []string{"completion", "--help"}, ""},
		{"completion-bash", []string{"completion", "bash"}, ""},
		{"configured-completion", []string{"--config", "/must-not-read", "completion", "bash"}, ""},
		{"completion-zsh", []string{"completion", "zsh"}, ""},
		{"completion-fish", []string{"completion", "fish"}, ""},
		{"completion-powershell", []string{"completion", "powershell"}, ""},
		{"completion-unknown", []string{"completion", "unknown"}, nkderr.CodeUsage},
		{"completion-position", []string{"completion", "bash", "unexpected"}, nkderr.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			actions := demoActions{
				run: func(context.Context, nkedemo.Config) error { t.Fatal("invalid input reached deployment"); return nil },
				build: func(context.Context, nkedemo.BuildConfig) (nkedemo.BuiltInputs, error) {
					t.Fatal("invalid input reached build")
					return nkedemo.BuiltInputs{}, nil
				},
				restore: func(context.Context, string, io.Writer) error {
					t.Fatal("invalid input reached restoration")
					return nil
				},
				load: func(context.Context, string) (nkedemo.DemoFile, error) {
					t.Fatal("help or invalid input read configuration")
					return nkedemo.DemoFile{}, nil
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
		})
	}
}

func configuredFixture() nkedemo.DemoFile {
	return nkedemo.DemoFile{Version: 1,
		Build: nkedemo.BuildFile{Repo: "configured-repo", NKEClone: "configured-clone", Attempt: "configured-attempt", Jobs: 3},
		Run: nkedemo.RunFile{NKECheckout: "configured-installer", NKDCTL: "configured-nkdctl", Out: "configured-out", TimeoutSeconds: 600,
			Runs: nkedemo.RunFileInputs{OS: "os", Components: "components", Providers: "providers", Site: "site", Tools: "tools", Profiles: "profiles", Simulator: "simulator"}},
		Restore: nkedemo.RestoreFile{Work: "configured-work"},
	}
}

func TestExplicitConfigOnlyChangedFlagsOverrideIt(t *testing.T) {
	for _, name := range []string{"run", "build", "restore", "explicit-empty", "config-failure"} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			loads, executions := 0, 0
			actions := demoActions{
				load: func(ctx context.Context, path string) (nkedemo.DemoFile, error) {
					loads++
					if ctx != t.Context() || path != "explicit.yaml" {
						t.Fatal("wrong explicit load")
					}
					if name == "config-failure" {
						return nkedemo.DemoFile{}, nkderr.New(nkderr.CodeUsage, "unknown YAML field")
					}
					return configuredFixture(), nil
				},
				run: func(_ context.Context, cfg nkedemo.Config) error {
					executions++
					if cfg.Out != "flag-out" || cfg.Timeout != 600*time.Second || cfg.NKDCTL != "configured-nkdctl" || cfg.NKECheckout != "configured-installer" {
						t.Fatalf("unchanged defaults overwrote config: %+v", cfg)
					}
					return nil
				},
				build: func(_ context.Context, cfg nkedemo.BuildConfig) (nkedemo.BuiltInputs, error) {
					executions++
					if cfg.AttemptPrefix != "flag-attempt" || cfg.Jobs != 3 || cfg.Repository != "configured-repo" {
						t.Fatalf("lost config: %+v", cfg)
					}
					return nkedemo.BuiltInputs{NKDCTL: "built"}, nil
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
				args = []string{"run", "--config", "explicit.yaml", "--nkdctl="}
			}
			err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args)
			if name == "explicit-empty" || name == "config-failure" {
				assertCode(t, err, nkderr.CodeUsage)
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
	for _, name := range []string{"exact", "retainedRun", "retainedTool", "buildFailure", "runFailure"} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			builds, runs := 0, 0
			var workContext context.Context
			fresh := nkedemo.BuiltInputs{NKDCTL: "fresh-nkdctl", Runs: nkedemo.RunInputs{OS: "fresh-os", Components: "fresh-components", Providers: "fresh-components", Site: "fresh-site", Tools: "fresh-tools", Profiles: "fresh-profiles", Simulator: "fresh-simulator"}}
			actions := demoActions{
				load: func(context.Context, string) (nkedemo.DemoFile, error) {
					file := configuredFixture()
					file.Run.NKDCTL = ""
					file.Run.Runs = nkedemo.RunFileInputs{}
					if name == "retainedRun" {
						file.Run.Runs.OS = "retained"
					}
					if name == "retainedTool" {
						file.Run.NKDCTL = "retained"
					}
					return file, nil
				},
				build: func(ctx context.Context, cfg nkedemo.BuildConfig) (nkedemo.BuiltInputs, error) {
					builds++
					workContext = ctx
					assertE2EBudget(t, ctx)
					if cfg.AttemptPrefix != "fresh-prefix" || cfg.Jobs != 3 {
						t.Fatal("wrong build intent")
					}
					if name == "buildFailure" {
						return nkedemo.BuiltInputs{}, nkderr.New(nkderr.CodeBuildFailed, "build failed")
					}
					return fresh, nil
				},
				run: func(ctx context.Context, cfg nkedemo.Config) error {
					runs++
					assertE2EBudget(t, ctx)
					if builds != 1 || ctx != workContext || cfg.Runs != fresh.Runs || cfg.NKDCTL != fresh.NKDCTL || cfg.NKECheckout != "configured-installer" || cfg.Timeout != 600*time.Second {
						t.Fatalf("deployment not bound to fresh build: %+v", cfg)
					}
					if name == "runFailure" {
						return nkderr.New(nkderr.CodePreflightFailed, "tenant failed")
					}
					return nil
				},
			}
			err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), []string{"e2e", "--config", "explicit.yaml", "--attempt", "fresh-prefix"})
			switch name {
			case "retainedRun", "retainedTool":
				assertCode(t, err, nkderr.CodeUsage)
				if builds != 0 || runs != 0 {
					t.Fatal("conflicting references started work")
				}
			case "buildFailure":
				assertCode(t, err, nkderr.CodeBuildFailed)
				if builds != 1 || runs != 0 {
					t.Fatal("deployment followed failed build")
				}
			case "runFailure":
				assertCode(t, err, nkderr.CodePreflightFailed)
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
	var out, diagnostics bytes.Buffer
	fresh := nkedemo.BuiltInputs{NKDCTL: "new-nkdctl", Runs: nkedemo.RunInputs{OS: "new-os", Components: "new-components", Providers: "new-components", Site: "new-site", Tools: "new-tools", Profiles: "new-profiles", Simulator: "new-simulator"}}
	order := make([]string, 0, 2)
	actions := demoActions{
		load: func(context.Context, string) (nkedemo.DemoFile, error) {
			t.Fatal("searched for implicit configuration")
			return nkedemo.DemoFile{}, nil
		},
		build: func(_ context.Context, cfg nkedemo.BuildConfig) (nkedemo.BuiltInputs, error) {
			order = append(order, "build")
			if cfg.Repository != "repo" || cfg.AttemptPrefix != "fresh" || cfg.Jobs != 1 {
				t.Fatalf("wrong build flags: %+v", cfg)
			}
			return fresh, nil
		},
		run: func(_ context.Context, cfg nkedemo.Config) error {
			order = append(order, "run")
			if cfg.Timeout != time.Hour || cfg.NKECheckout != "installer" || cfg.Out != "new-out" || cfg.NKDCTL != fresh.NKDCTL || cfg.Runs != fresh.Runs {
				t.Fatalf("not fresh derived inputs: %+v", cfg)
			}
			return nil
		},
	}
	err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), []string{"e2e", "--repo", "repo", "--attempt", "fresh", "--nke-checkout", "installer", "--out", "new-out"})
	assertCode(t, err, "")
	if strings.Join(order, ",") != "build,run" {
		t.Fatalf("wrong operation order: %v", order)
	}
}

func assertCode(t *testing.T, err error, want nkderr.Code) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var coded *nkderr.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v want code=%s", err, want)
	}
}

func TestTypedRunFlagsAndContextReachLibraryUnchanged(t *testing.T) {
	for _, seconds := range []string{"300", "3600"} {
		t.Run(seconds, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			actions := demoActions{run: func(got context.Context, cfg nkedemo.Config) error {
				called = true
				if got.Err() != nil || cfg.Progress.(*checkedWriter).out != &out {
					t.Fatal("cancellation context or progress destination lost")
				}
				if cfg.NKECheckout != "installer" || cfg.NKDCTL != "nkdctl" || cfg.Out != "new-output" || cfg.Runs != (nkedemo.RunInputs{OS: "os", Components: "components", Providers: "providers", Site: "site", Tools: "tools", Profiles: "profiles", Simulator: "simulator"}) {
					t.Fatalf("lost flags: %+v", cfg)
				}
				expected := 300 * time.Second
				if seconds == "3600" {
					expected = time.Hour
				}
				if cfg.Timeout != expected {
					t.Fatal("wrong timeout")
				}
				return nkderr.New(nkderr.CodeTimeout, "library canceled")
			}}
			args := []string{"run", "--nke-checkout", "installer", "--nkdctl", "nkdctl", "--out", "new-output", "--os-run", "os", "--components-run", "components", "--provider-run", "providers", "--site-run", "site", "--tools-run", "tools", "--profiles-run", "profiles", "--simulator-run", "simulator", "--timeout", seconds}
			err := executeRoot(ctx, newCommand(&out, &diagnostics, actions), args)
			assertCode(t, err, nkderr.CodeTimeout)
			if !called {
				t.Fatal("library not called")
			}
		})
	}
}

func TestBuildAndRestoreDelegateTypedInputs(t *testing.T) {
	for _, name := range []string{"build", "restore", "restore-failure"} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			called := false
			actions := demoActions{
				build: func(ctx context.Context, cfg nkedemo.BuildConfig) (nkedemo.BuiltInputs, error) {
					called = true
					if ctx.Err() != nil || cfg.Repository != "repo" || cfg.NKEClone != "clone" || cfg.AttemptPrefix != "fresh" || cfg.Jobs != 2 || cfg.Progress.(*checkedWriter).out != &out {
						t.Fatalf("lost build options: %+v", cfg)
					}
					return nkedemo.BuiltInputs{NKDCTL: "built-nkdctl"}, nil
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
						return nkderr.New(nkderr.CodePreflightFailed, "changed controls")
					}
					return nil
				},
			}
			args := []string{"restore", "--work", "original-work"}
			if name == "build" {
				args = []string{"build", "--repo", "repo", "--nke-clone", "clone", "--attempt", "fresh", "--jobs", "2"}
			}
			err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args)
			if name == "restore-failure" {
				assertCode(t, err, nkderr.CodePreflightFailed)
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
