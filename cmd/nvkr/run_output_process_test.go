// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

// The fresh process executes main against a real fd 1 with a closed reader.
// Plan inspection and local dry-run perform no producer execution or deployment.
func TestCLIClosedStdoutPreservesCheckedStatus(t *testing.T) {
	if scenario := os.Getenv("NVKR_TEST_CLOSED_STDOUT"); scenario != "" {
		switch scenario {
		case "plan":
			os.Args = []string{"nvkr", "run", "plan", "--plan", ".github/nvkr-run/plan.json", "--json"}
		case "plain plan":
			os.Args = []string{"nvkr", "run", "plan", "--plan", ".github/nvkr-run/plan.json"}
		case "dry-run":
			os.Args = localPresentationArgs("--dry-run", "--ui=plain")
		case "JSON dry-run":
			os.Args = localPresentationArgs("--dry-run", "--json")
		case "JSON failure":
			os.Args = []string{"nvkr", "run", "local", "--definition", "missing", "--environment", "missing", "--attempt", "fresh", "--json"}
		case "inspection":
			os.Args = []string{"nvkr", "run", "inspect", "--record", os.Getenv("NVKR_TEST_RECORD_PATH"), "--json"}
		case "plain inspection":
			os.Args = []string{"nvkr", "run", "inspect", "--record", os.Getenv("NVKR_TEST_RECORD_PATH")}
		case "unchecked definition":
			os.Args = []string{"nvkr", "definition", "validate", "--definition", "docs/examples/nvke-tutorial/definition.yaml"}
		default:
			t.Fatal("unknown process fixture")
		}
		main()
		return
	}
	lifecycleCommandRepo(t)
	_, recordPath, _ := runInspectionFixture(t, "process-inspection")
	for _, test := range []struct {
		name   string
		code   e.Code
		signal bool
	}{
		{"plan", e.CodeInternal, false}, {"dry-run", "", true}, {"JSON dry-run", e.CodeInternal, false}, {"JSON failure", e.CodeInvalidCatalog, false},
		{"inspection", e.CodeInternal, false}, {"unchecked definition", "", true}, {"plain plan", "", true}, {"plain inspection", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIClosedStdoutPreservesCheckedStatus$")
			command.Env = append(os.Environ(), "NVKR_TEST_CLOSED_STDOUT="+test.name, "NVKR_TEST_RECORD_PATH="+recordPath)
			command.Stdout = writer
			err = command.Run()
			if test.signal {
				if command.ProcessState == nil {
					t.Fatalf("unchecked reporter has no process status: %v", err)
				}
				status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != syscall.SIGPIPE {
					t.Fatalf("unchecked reporter's SIGPIPE behavior changed: %v, state=%v", err, command.ProcessState)
				}
				return
			}
			want := e.ExitCode(e.New(test.code, ""))
			if command.ProcessState == nil || command.ProcessState.ExitCode() != want {
				t.Fatalf("closed stdout replaced coded result: %v, state=%v, want %d", err, command.ProcessState, want)
			}
		})
	}
}

func TestCheckedOutputSignalsPreserveProcessDispositions(t *testing.T) {
	if scenario := os.Getenv("NVKR_TEST_SIGNAL_SCOPE"); scenario != "" {
		if scenario == "ignored" {
			signal.Ignore(syscall.SIGPIPE)
		}
		var prior chan os.Signal
		if scenario == "prior notification" {
			prior = make(chan os.Signal, 1)
			signal.Notify(prior, syscall.SIGPIPE)
			defer signal.Stop(prior)
		}
		stop := checkedOutputSignals()
		child := exec.CommandContext(t.Context(), "/bin/sh", "-c", "kill -PIPE $$; printf survived")
		raw, err := child.CombinedOutput()
		if scenario == "ignored" {
			if err != nil || string(raw) != "survived" {
				t.Fatalf("preexisting ignore changed in child: %v %q", err, raw)
			}
		} else {
			status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGPIPE {
				t.Fatalf("child inherited nondefault SIGPIPE: %v %q", err, raw)
			}
		}
		stop()
		if signal.Ignored(syscall.SIGPIPE) != (scenario == "ignored") {
			t.Fatal("checked reporter changed ignored disposition")
		}
		if scenario == "restored" {
			// stdout has a closed reader. After Stop the ordinary Go fd1
			// SIGPIPE behavior must resume and terminate this fixture.
			_, _ = os.Stdout.Write([]byte("restored\n"))
			t.Fatal("SIGPIPE default did not resume")
		}
		if prior != nil {
			// Exercise the Go fd1 SIGPIPE path used by checked reporters,
			// rather than an unrelated user-generated signal.
			_, _ = os.Stdout.Write([]byte("prior subscriber\n"))
			select {
			case <-prior:
			case <-time.After(3 * time.Second):
				t.Fatal("Stop removed another subscriber's registration")
			}
			signal.Stop(prior)
			os.Exit(0) // avoid testing the harness's own PASS write to fd1
		}
		return
	}
	for _, scenario := range []string{"default", "ignored", "prior notification", "restored"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckedOutputSignalsPreserveProcessDispositions$")
			command.Env = append(os.Environ(), "NVKR_TEST_SIGNAL_SCOPE="+scenario)
			if scenario == "restored" || scenario == "prior notification" {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				_ = reader.Close()
				defer writer.Close()
				command.Stdout = writer
				err = command.Run()
				if command.ProcessState == nil {
					t.Fatalf("missing restored process state: %v", err)
				}
				status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
				if scenario == "prior notification" {
					if err != nil {
						t.Fatalf("prior subscriber was removed: %v", err)
					}
				} else if !ok || !status.Signaled() || status.Signal() != syscall.SIGPIPE {
					t.Fatalf("parent SIGPIPE behavior not restored: %v", err)
				}
			} else if raw, err := command.CombinedOutput(); err != nil {
				t.Fatalf("signal scope failed: %v\n%s", err, raw)
			}
		})
	}
}

func TestRunLocalProducerInheritsDefaultSIGPIPE(t *testing.T) {
	if mode := os.Getenv("NVKR_TEST_LOCAL_CHILD_PIPE"); mode != "" {
		execute := func(ctx context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
			child := exec.CommandContext(ctx, "/bin/sh", "-c", "kill -PIPE $$; printf survived")
			raw, err := child.CombinedOutput()
			status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGPIPE {
				return nil, e.New(e.CodeInternal, "producer inherited SIGPIPE ignore: %v %q", err, raw)
			}
			return completedPresentationRun(prepared), nil
		}
		services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(ctx context.Context, _ runpresenter.Options, _ runSnapshotSource, work func(context.Context) error) runpresenter.Result {
			return runpresenter.Result{WorkErr: work(ctx)}
		}}
		app := localPresentationTestApp(execute, io.Discard, services)
		flags := []string{"--ui=" + mode}
		if mode == "json" {
			flags = []string{"--json"}
		}
		if err := app.RunContext(t.Context(), localPresentationArgs(flags...)); err != nil {
			t.Fatal(err)
		}
		return
	}
	lifecycleCommandRepo(t)
	for _, mode := range []string{"plain", "json", "tui"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunLocalProducerInheritsDefaultSIGPIPE$")
			command.Env = append(os.Environ(), "NVKR_TEST_LOCAL_CHILD_PIPE="+mode)
			if raw, err := command.CombinedOutput(); err != nil {
				t.Fatalf("local producer signal contract failed: %v\n%s", err, raw)
			}
		})
	}
}

// Generic result viewers replay private renderer diagnostics after the session
// returns. A closed real fd2 must not replace the owning/presentation exit code.
func TestCommandViewerClosedStderrPreservesDiagnosticFailureStatus(t *testing.T) {
	if scenario := os.Getenv("NVKR_TEST_VIEWER_CLOSED_STDERR"); scenario != "" {
		presentation := e.New(e.CodeUnavailable, "renderer unavailable")
		var owner error
		if scenario == "owning failure" {
			owner = e.New(e.CodeBuildFailed, "owning build failed")
		}
		services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, options runpresenter.Options, _ runSnapshotSource, work func(context.Context) error) runpresenter.Result {
			if options.Diagnostics == nil || options.Live || work != nil {
				t.Fatal("generic result viewer changed diagnostic or work ownership")
			}
			if _, err := options.Diagnostics.Write([]byte("private renderer diagnostic replay\n")); err != nil {
				t.Fatal(err)
			}
			return runpresenter.Result{PresentationErr: presentation}
		}}
		app := &cli.App{Writer: io.Discard, ErrWriter: os.Stderr, Metadata: map[string]interface{}{commandPresentationServicesKey: services}, Action: func(c *cli.Context) error {
			return showCommandResult(c, runPresentation{tui: true}, commandDisplay{Title: "Result", Note: "Description only."}, owner)
		}}
		err := app.RunContext(t.Context(), []string{"nvkr"})
		if !errors.Is(err, syscall.EPIPE) || !errors.Is(err, presentation) || owner != nil && !errors.Is(err, owner) {
			t.Fatalf("diagnostic replay did not retain write failure and primary causes: %v", err)
		}
		os.Exit(e.ExitCode(err))
	}
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"presentation failure", e.CodeUnavailable},
		{"owning failure", e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandViewerClosedStderrPreservesDiagnosticFailureStatus$")
			command.Env = append(os.Environ(), "NVKR_TEST_VIEWER_CLOSED_STDERR="+test.name)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, writer
			err = command.Run()
			want := e.ExitCode(e.New(test.code, ""))
			if command.ProcessState == nil || command.ProcessState.ExitCode() != want {
				t.Fatalf("closed diagnostic reader replaced primary code: %v state=%v want=%d stdout=%q", err, command.ProcessState, want, output.String())
			}
		})
	}
}
