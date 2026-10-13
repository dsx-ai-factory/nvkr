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

func defaultRunPresentationServices() runPresentationServices {
	return runPresentationServices{eligible: runpresenter.Eligible, show: func(ctx context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
		return runpresenter.Run(ctx, options, source, work)
	}}
}

func commandOutput(c *cli.Context) io.Writer {
	if c.App.Writer != nil {
		return c.App.Writer
	}
	return os.Stdout
}

// Only these checked leaf reporters opt out of Go's fd 1/2 SIGPIPE termination.
// Restore the disposition after all owned work has joined so unrelated commands
// retain their existing behavior when an application is embedded or tested.
func checkedOutputSignals() func() {
	if signal.Ignored(syscall.SIGPIPE) {
		return func() {}
	}
	signal.Ignore(syscall.SIGPIPE)
	return func() { signal.Reset(syscall.SIGPIPE) }
}

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
	return runPresentation{tui: mode == "tui" || mode == "auto" && live && eligible && !structured,
		json: c.Bool("json"), noColor: c.Bool("no-color") || os.Getenv("NO_COLOR") != ""}, nil
}

func runLocalCommand(c *cli.Context, execute localLifecycleExecutor) error {
	return runLocalCommandWithServices(c, execute, defaultRunPresentationServices())
}

func runLocalCommandWithServices(c *cli.Context, execute localLifecycleExecutor, services runPresentationServices) error {
	view, err := selectRunPresentationWithEligibility(c, !c.Bool("dry-run"), services.eligible)
	if err != nil {
		return err
	}
	if c.Args().Present() {
		return e.New(e.CodeUsage, "run local accepts flags only")
	}
	defer checkedOutputSignals()()
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
	prepared, err := lifecycle.PrepareFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("attempt"))
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
	if view.json {
		stdout = stderr
	} else if view.tui {
		// Raw producer diagnostics never enter the safe terminal projection.
		// Lifecycle's existing private diagnostic owner retains supported logs.
		stdout, stderr = io.Discard, io.Discard
	}
	var result *lifecycle.Run
	work := func(ctx context.Context) error {
		var workErr error
		result, workErr = execute(ctx, prepared, lifecycle.LocalOptions{AttemptsRoot: c.String("attempts-root"), RunnerSource: c.String("runner-source"), Jobs: c.Int("jobs"), Runtime: c.String("runtime"), Go: c.String("go"), Publish: c.Bool("publish"), DryRun: c.Bool("dry-run"), Stdout: stdout, Stderr: stderr, Activity: activity, Progress: monitor})
		if result == nil && workErr == nil {
			workErr = e.New(e.CodeInternal, "local execution returned no Run result")
		}
		if result != nil && result.Status == lifecycle.RunComplete && workErr != nil {
			workErr = e.Wrap(e.CodeImmutableViolation, workErr, "completed local Run and outputs were retained, but commit cleanup is incomplete")
		}
		finishLocalPresentation(monitor, result, workErr, c.Bool("dry-run"))
		return workErr
	}
	var workErr, presentationErr error
	if view.tui && c.Bool("dry-run") {
		// Admit the ordinary dry-run path first. It creates no attempt and executes
		// no producers; the screen then acts only as a read-only static viewer.
		workErr = work(c.Context)
		if workErr == nil {
			shown := services.show(c.Context, runpresenter.Options{NoColor: view.noColor}, monitor, nil)
			presentationErr = preserveRunError(shown.WorkErr, shown.PresentationErr)
		}
	} else if view.tui {
		shown := services.show(c.Context, runpresenter.Options{Live: true, NoColor: view.noColor}, monitor, work)
		workErr, presentationErr = shown.WorkErr, shown.PresentationErr
	} else {
		workErr = work(c.Context)
	}
	err = preserveRunError(workErr, presentationErr)
	if view.json {
		return preserveRunError(err, writeLocalRunJSON(commandOutput(c), prepared, result, err, c.Bool("dry-run"), result != nil && result.Status == lifecycle.RunComplete && workErr != nil))
	}
	if view.tui && err != nil && (result == nil || result.Status != lifecycle.RunComplete) {
		return preserveRunError(err, printSafeLocalFailure(c, prepared, result, err))
	}
	if result != nil && (workErr == nil || result.Status == lifecycle.RunComplete) {
		return preserveRunError(err, printLocalRunResult(c, result))
	}
	return err
}

func finishLocalPresentation(monitor *progress.Monitor, result *lifecycle.Run, err error, dryRun bool) {
	if result == nil {
		monitor.Finish("unavailable", "", nil, string(runErrorCode(err)), false, false)
		return
	}
	observed := make([]string, 0, len(result.Observed))
	for _, output := range result.Observed {
		observed = append(observed, output.Name)
	}
	monitor.Finish(result.Status, result.Lane, observed, string(runErrorCode(err)), !dryRun, result.Status == lifecycle.RunComplete && err != nil)
}

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

// The owning failure remains first and retains its exact coded status and cause.
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
			if observed.Kind != lifecycle.OutputDiskImage {
				continue
			}
			path, err := lifecycle.LocalDiskOutputPath(workspace, observed.Name)
			if err != nil {
				return err
			}
			fmt.Fprintf(&out, "disk: %s\n", path)
		}
	}
	return checkedRunWrite(commandOutput(c), []byte(out.String()))
}
