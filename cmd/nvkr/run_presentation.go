// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

type runPresentation struct {
	tui, json, noColor bool
}

type runSnapshotSource interface{ Snapshot() progress.Snapshot }

type runPresentationServices struct {
	eligible func(io.Writer) bool
	show     func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result
}

// defaultRunPresentationServices binds terminal admission and session ownership
// to the presenter; tests can replace both without opening the controlling TTY.
func defaultRunPresentationServices() runPresentationServices {
	return runPresentationServices{eligible: runpresenter.Eligible, show: func(ctx context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
		return runpresenter.Run(ctx, options, source, work)
	}}
}

// commandOutput respects an injected application writer before using stdout.
// Terminal admission checks this exact writer to preserve redirected output.
func commandOutput(c *cli.Context) io.Writer {
	if c.App.Writer != nil {
		return c.App.Writer
	}
	return os.Stdout
}

// Checked reporters receive fd 1/2 write errors without changing producer signal
// dispositions. Go resets handled signals in children; ignored signals would be
// inherited. Stop removes only our registration after owned work has joined.
func checkedOutputSignals() func() {
	if signal.Ignored(syscall.SIGPIPE) {
		return func() {}
	}
	pipe := make(chan os.Signal, 1)
	signal.Notify(pipe, syscall.SIGPIPE)
	return func() { signal.Stop(pipe) }
}

// selectRunPresentationWithEligibility rejects conflicting modes before work.
// Auto selects a TUI only for live, unstructured output on an admitted terminal;
// an explicit TUI request fails closed when the terminal is unavailable.
func selectRunPresentationWithEligibility(c *cli.Context, live bool, eligibleTerminal func(io.Writer) bool) (runPresentation, error) {
	mode := c.String("ui")
	if mode == "" {
		mode = "plain"
	}
	if mode != "auto" && mode != "plain" && mode != "tui" {
		return runPresentation{}, e.New(e.CodeUsage, "--ui must be auto, plain, or tui")
	}
	structured := c.Bool("json") || c.Bool("pretty")
	if mode == "tui" && structured {
		return runPresentation{}, e.New(e.CodeUsage, "--ui=tui cannot be combined with JSON output")
	}
	eligible := eligibleTerminal(commandOutput(c))
	if mode == "tui" && !eligible {
		return runPresentation{}, e.New(e.CodeUsage, "--ui=tui requires interactive stdin and stdout with a supported TERM; use --ui=plain")
	}
	return runPresentation{tui: mode == "tui" || mode == "auto" && live && eligible && !structured && os.Getenv("CI") == "",
		json: c.Bool("json"), noColor: c.Bool("no-color") || os.Getenv("NO_COLOR") != ""}, nil
}

// runLocalCommand presents local execution through the real terminal services.
// The injected executor retains ownership of the Run and its coded outcome.
func runLocalCommand(c *cli.Context, execute localLifecycleExecutor) error {
	return runLocalCommandWithServices(c, execute, defaultRunPresentationServices())
}

// runLocalCommandWithServices prepares and delegates a local Run without making
// presentation state authoritative. The session must restore the terminal and
// join work before returning; only then are captured producer bytes replayed on
// failure. Plain and JSON modes keep their existing stream routing, and reporting
// failures preserve the owning code and any retained complete Run.
func runLocalCommandWithServices(c *cli.Context, execute localLifecycleExecutor, services runPresentationServices) error {
	view, err := selectRunPresentationWithEligibility(c, !c.Bool("dry-run"), services.eligible)
	if err != nil {
		return err
	}
	if c.Args().Present() {
		return e.New(e.CodeUsage, "run local accepts flags only")
	}
	if c.String("attempts-root") == "" {
		return e.New(e.CodeUsage, "--attempts-root must not be empty")
	}
	if view.tui || view.json {
		defer checkedOutputSignals()()
	}
	var activity *buildactivity.Sink
	if c.IsSet("build-activity-fd") {
		if c.Bool("dry-run") || c.Bool("publish") {
			return e.New(e.CodeUsage, "build activity is available only for local execution")
		}
		activity, err = buildactivity.AdoptFD(c.Context, c.Int("build-activity-fd"))
		if err != nil {
			return err
		}
		defer func() { _ = activity.Close() }()
	}
	overrides, err := parseSourceOverrideFlags(c.StringSlice("source-override"))
	if err != nil {
		return err
	}
	if len(overrides) != 0 && c.IsSet("capture-inputs-root") {
		return e.New(e.CodeUsage, "--source-override excludes --capture-inputs-root; overridden Runs cannot feed qualification")
	}
	var prepared *lifecycle.PreparedRun
	if len(overrides) != 0 {
		prepared, err = lifecycle.PrepareSourceOverrideFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("attempt"), overrides)
	} else if c.IsSet("capture-inputs-root") {
		prepared, err = lifecycle.PrepareCapturedFiles(c.Context, c.String("capture-inputs-root"), c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("attempt"))
	} else {
		prepared, err = lifecycle.PrepareFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("attempt"))
	}
	if err != nil {
		if view.json {
			return preserveRunError(err, writeLocalRunJSON(commandOutput(c), nil, nil, err, c.Bool("dry-run"), false))
		}
		return err
	}
	monitor, err := lifecycle.NewProgress(prepared)
	if err != nil {
		return err
	}
	stdout, stderr := commandOutput(c), io.Writer(os.Stderr)
	var diagnostics rundiagnostics.Buffer
	if view.json {
		stdout = stderr
	} else if view.tui {
		// Raw bytes remain in bounded private memory, outside safe snapshots.
		// Failure reporting waits until the presenter restores and joins work.
		stdout, stderr = &diagnostics, &diagnostics
	}
	var result *lifecycle.Run
	work := func(ctx context.Context) error {
		var workErr error
		result, workErr = execute(ctx, prepared, lifecycle.LocalOptions{AttemptsRoot: c.String("attempts-root"), RunnerSource: c.String("runner-source"), Jobs: c.Int("jobs"), Runtime: c.String("runtime"), Go: c.String("go"), Publish: c.Bool("publish"), DryRun: c.Bool("dry-run"), Stdout: stdout, Stderr: stderr, Activity: activity, Progress: monitor})
		if result == nil && workErr == nil {
			workErr = e.New(e.CodeInternal, "local execution returned no Run result")
		}
		return workErr
	}
	var workErr, presentationErr error
	if view.tui && c.Bool("dry-run") {
		// Admit the ordinary dry-run path first. It creates no attempt and executes
		// no producers; the screen then acts only as a read-only static viewer.
		workErr = work(c.Context)
		if workErr == nil {
			shown := services.show(c.Context, runpresenter.Options{NoColor: view.noColor, Diagnostics: &diagnostics}, monitor, nil)
			presentationErr = preserveRunError(shown.WorkErr, shown.PresentationErr)
		}
	} else if view.tui {
		shown := services.show(c.Context, runpresenter.Options{Live: true, NoColor: view.noColor, Diagnostics: &diagnostics}, monitor, work)
		workErr, presentationErr = shown.WorkErr, shown.PresentationErr
	} else {
		workErr = work(c.Context)
	}
	err = preserveRunError(workErr, presentationErr)
	if view.json {
		return preserveRunError(err, writeLocalRunJSON(commandOutput(c), prepared, result, err, c.Bool("dry-run"), result != nil && result.Status == lifecycle.RunComplete && workErr != nil))
	}
	if view.tui && err != nil && (result == nil || result.Status != lifecycle.RunComplete) {
		err = preserveRunError(err, printSafeLocalFailure(c, prepared, result, err))
	} else if result != nil && (workErr == nil || result.Status == lifecycle.RunComplete) {
		err = preserveRunError(err, printLocalRunResult(c, result))
	}
	if view.tui {
		// Print the safe result before raw replay, which may end inside a terminal
		// control sequence. Both happen after the session restores and joins work.
		return replayRunDiagnostics(c, &diagnostics, err)
	}
	return err
}

// parseSourceOverrideFlags splits NAME=REQUEST flags into requests. Lifecycle
// admits them against the Environment through the shared intake.
func parseSourceOverrideFlags(values []string) ([]lifecycle.SourceOverrideRequest, error) {
	requests := make([]lifecycle.SourceOverrideRequest, 0, len(values))
	for _, value := range values {
		name, request, found := strings.Cut(value, "=")
		if !found || name == "" || request == "" {
			return nil, e.New(e.CodeUsage, "--source-override %q must be NAME=REQUEST", value)
		}
		requests = append(requests, lifecycle.SourceOverrideRequest{Name: name, Request: request})
	}
	return requests, nil
}

// replayRunDiagnostics reports a private bounded tail only after a failed session
// has restored and joined. Reporting cannot replace the owning failure code.
func replayRunDiagnostics(c *cli.Context, diagnostics *rundiagnostics.Buffer, ownerErr error) error {
	if ownerErr == nil {
		return nil
	}
	raw, truncated := diagnostics.Snapshot()
	if len(raw) == 0 {
		return ownerErr
	}
	replay := []byte("\nMemory-captured diagnostics tail:\n")
	if truncated {
		replay = append(replay, "[Earlier diagnostics truncated; showing the last 128 KiB.]\n"...)
	}
	replay = append(replay, raw...)
	if replay[len(replay)-1] != '\n' {
		replay = append(replay, '\n')
	}
	output := c.App.ErrWriter
	if output == nil {
		output = os.Stderr
	}
	return preserveRunError(ownerErr, checkedRunWrite(output, replay))
}

// printSafeLocalFailure writes only identity, observed counts, and a known code.
// Raw producer bytes and error text belong to separate private diagnostic replay.
func printSafeLocalFailure(c *cli.Context, prepared *lifecycle.PreparedRun, run *lifecycle.Run, err error) error {
	status, observed := "unavailable", 0
	if run != nil {
		switch run.Status {
		case lifecycle.RunFailed, lifecycle.RunPending:
			status = run.Status
		}
		observed = len(run.Observed)
	}
	frozen := prepared.Description()
	message := fmt.Sprintf("\nattempt: %s\nrun: %s\nstatus: %s\nobserved outputs: %d/%d required\nfailure code: %s\nNo release verification or runtime health result is implied.\n", frozen.Attempt, frozen.PlanDigest, status, observed, len(frozen.Expected), runErrorCode(err))
	return checkedRunWrite(commandOutput(c), []byte(message))
}

// runErrorCode exposes a registered owner code, maps unknown errors to internal,
// and leaves successful outcomes empty instead of inventing a failure code.
func runErrorCode(err error) e.Code {
	if err == nil {
		return ""
	}
	var coded *e.Error
	if errors.As(err, &coded) {
		for _, known := range e.AllCodes {
			if coded.Code == known {
				return known
			}
		}
	}
	return e.CodeInternal
}

// preserveRunError keeps the owning failure first with its coded status and cause.
// A reporting failure cannot turn a retained successful Run into a failed Run.
func preserveRunError(owner, reporting error) error {
	if owner == nil {
		return reporting
	}
	if reporting == nil {
		return owner
	}
	return e.Wrap(runErrorCode(owner), errors.Join(owner, reporting), "Run reporting also failed")
}

// checkedRunWrite turns failed and short writes into coded reporting errors.
// Callers combine them with the owner outcome through preserveRunError.
func checkedRunWrite(out io.Writer, raw []byte) error {
	n, err := out.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "writing Run result")
	}
	return nil
}

// printLocalRunResult reports the returned Run and owner-derived payload paths.
// Dry-run output omits paths to records and payloads that were never retained.
func printLocalRunResult(c *cli.Context, result *lifecycle.Run) error {
	if result == nil {
		return e.New(e.CodeInternal, "local execution returned no Run result")
	}
	workspace := filepath.Join(c.String("attempts-root"), c.String("attempt"))
	var out strings.Builder
	fmt.Fprintf(&out, "\nattempt: %s\nrun: %s\nlane: %s\nstatus: %s\nobserved outputs: %d\n", result.Attempt, result.PlanDigest, result.Lane, result.Status, len(result.Observed))
	if !c.Bool("dry-run") {
		fmt.Fprintf(&out, "record: %s\n", filepath.Join(workspace, "lifecycle.json"))
		for _, observed := range result.Observed {
			var label, path string
			var err error
			switch observed.Kind {
			case lifecycle.OutputDiskImage:
				label = "disk"
				path, err = lifecycle.LocalDiskOutputPath(workspace, observed.Name)
			case lifecycle.OutputDebianPackage:
				label = "package"
				path, err = lifecycle.LocalDebianOutputPath(workspace, observed.Name)
			default:
				continue
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(&out, "%s: %s\n", label, path)
		}
	}
	return checkedRunWrite(commandOutput(c), []byte(out.String()))
}
