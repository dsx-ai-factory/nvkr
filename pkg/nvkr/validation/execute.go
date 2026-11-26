// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const (
	// inputsPath holds each staged input at <role>/<output> inside the container.
	inputsPath = "/inputs"
	// acquisitionTimeout bounds dependency acquisition separately from the suite timeout.
	acquisitionTimeout = 30 * time.Minute
	maxExecutableBytes = 1 << 30
)

// ExecutionState is how far one suite's execution progressed.
type ExecutionState string

// Execution states.
const (
	ExecutionNotStarted ExecutionState = "not-started"
	ExecutionPreparing  ExecutionState = "preparing"
	ExecutionRunning    ExecutionState = "running"
	ExecutionCompleted  ExecutionState = "completed"
)

// Termination is how the suite command ended.
type Termination string

// Terminations. A suite whose command never started has none, unless
// cancellation stopped its setup. Cancellation means the caller's context
// ended, including through a deadline of its own; timeout means the suite's
// registered timeout elapsed.
const (
	TerminationExited      Termination = "exited"
	TerminationLaunchError Termination = "launch-error"
	TerminationTimeout     Termination = "timeout"
	TerminationCanceled    Termination = "canceled"
)

// SuiteStatus is a suite's verdict.
type SuiteStatus string

// Verdicts.
const (
	StatusPassed         SuiteStatus = "passed"
	StatusFailed         SuiteStatus = "failed"
	StatusExecutionError SuiteStatus = "execution-error"
)

// CleanupState records whether a suite's owned resources were removed.
type CleanupState string

// Cleanup states.
const (
	CleanupNotRequired CleanupState = "not-required"
	CleanupComplete    CleanupState = "complete"
	CleanupFailed      CleanupState = "failed"
)

// SuiteExecution records how one suite's execution ended. The verdict comes
// from the process outcome only: exit 0 passes, another exit status fails, and
// a launch error, timeout, cancellation, failed setup or input bytes changed
// during the run are execution errors. ExitCode is nil when no exit status of
// the suite command was observed. Cleanup is recorded separately from the verdict.
type SuiteExecution struct {
	ID                    string
	State                 ExecutionState
	Termination           Termination
	ExitCode              *int
	StartedAt, FinishedAt time.Time
	Status                SuiteStatus
	Reason                string
	Cleanup               CleanupState
}

// ExecuteOptions configures where a suite works and where its output goes.
type ExecuteOptions struct {
	// WorkspaceRoot receives each suite's private workspace, which is removed
	// after the suite. The root is created if missing.
	WorkspaceRoot string
	// Stdout and Stderr receive the suite's raw output unchanged; nil discards it.
	Stdout, Stderr io.Writer
}

// suiteExecutor is the source and container execution this package delegates to.
type suiteExecutor struct {
	admitUser    func() error
	importSource func(ctx context.Context, environment []string, directory, repository, commit, tree string, stderr io.Writer) error
	run          func(ctx context.Context, command pipeline.IsolatedCommand, stdout, stderr io.Writer) error
	now          func() time.Time
}

// Execute runs one bound suite through the container-command executor: it
// stages the suite's checked inputs, imports its exact test origin, runs the
// optional acquisition command with network and without inputs, then runs the
// command offline with inputs mounted read-only, and finally removes the
// workspace. It refuses to run as root before any work. The returned error is nil only when the suite passed and cleanup
// completed; a suite failure stays primary over a cleanup failure.
func (b *BoundValidation) Execute(ctx context.Context, id string, options ExecuteOptions) (SuiteExecution, error) {
	return b.execute(ctx, id, options, suiteExecutor{admitUser: pipeline.AdmitIsolatedCommandUser,
		importSource: sourcegit.ImportLocal, run: pipeline.RunIsolatedCommand, now: time.Now})
}

func (b *BoundValidation) execute(ctx context.Context, id string, options ExecuteOptions, x suiteExecutor) (SuiteExecution, error) {
	result := SuiteExecution{ID: id, State: ExecutionNotStarted}
	if b == nil || b.run == nil {
		return result, usage("execution requires a bound validation")
	}
	if ctx == nil {
		return result, usage("execution requires context")
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "validation canceled before execution")
	}
	index := slices.IndexFunc(b.suites, func(s BoundSuite) bool { return s.ID == id })
	if index < 0 {
		return result, e.New(e.CodeUsage, "suite %q is not bound", id)
	}
	if !pathText(options.WorkspaceRoot) {
		return result, usage("execution requires a workspace root")
	}
	// Container mounts need absolute host paths.
	root, err := filepath.Abs(options.WorkspaceRoot)
	if err != nil {
		return result, e.Wrap(e.CodeUsage, err, "resolving workspace root")
	}
	options.WorkspaceRoot = root
	// Suites never run as root; refuse before staging, fetching or containers.
	if err := x.admitUser(); err != nil {
		return result, err
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	suite := b.suites[index]
	var suiteErr error
	err = b.run.WithDiagnosticOperation(ctx, func(operation context.Context, _ lifecycle.LocalDiagnosticDescription) error {
		result.State, result.StartedAt = ExecutionPreparing, x.now()
		suiteErr = b.runSuite(operation, suite, options, x, &result)
		result.State, result.FinishedAt = ExecutionCompleted, x.now()
		return nil
	})
	if result.State == ExecutionNotStarted {
		return result, err
	}
	if suiteErr != nil {
		return result, suiteErr
	}
	if err != nil {
		// The Run was not held intact through the whole suite: a pass cannot stand.
		result.Status, result.Reason = StatusExecutionError, "bound Run was not held through the suite: "+err.Error()
	}
	return result, err
}

// runSuite records the verdict in result and returns the suite's coded error.
// Cleanup runs on every path once the workspace exists.
func (b *BoundValidation) runSuite(ctx context.Context, suite BoundSuite, options ExecuteOptions, x suiteExecutor, result *SuiteExecution) (returnErr error) {
	result.Cleanup = CleanupNotRequired
	if err := os.MkdirAll(options.WorkspaceRoot, 0o700); err != nil {
		return setupFailure(ctx, result, e.Wrap(e.CodeBuildFailed, err, "creating validation workspace root"))
	}
	workspace, err := os.MkdirTemp(options.WorkspaceRoot, "suite-")
	if err != nil {
		return setupFailure(ctx, result, e.Wrap(e.CodeBuildFailed, err, "creating private validation workspace"))
	}
	defer func() {
		if err := os.RemoveAll(workspace); err != nil {
			result.Cleanup = CleanupFailed
			if returnErr == nil {
				returnErr = e.Wrap(e.CodeBuildFailed, err, "removing validation workspace")
			}
			return
		}
		result.Cleanup = CleanupComplete
	}()
	cache := filepath.Join(workspace, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		return setupFailure(ctx, result, e.Wrap(e.CodeBuildFailed, err, "creating validation cache"))
	}
	staged, err := b.stageInputs(ctx, suite, filepath.Join(workspace, "inputs"))
	if err != nil {
		return setupFailure(ctx, result, err)
	}
	source := filepath.Join(workspace, "source")
	if err := x.importSource(ctx, os.Environ(), source, suite.TestOrigin.Repository, suite.TestOrigin.Commit, suite.TestOrigin.Tree, options.Stderr); err != nil {
		return setupFailure(ctx, result, codedAs(err, e.CodeFetchFailed, "importing test origin"))
	}
	if info, err := os.Stat(filepath.Join(source, filepath.FromSlash(suite.WorkingDirectory))); err != nil || !info.IsDir() {
		return setupFailure(ctx, result, e.New(e.CodeEngineCapability, "test origin has no working directory %q", suite.WorkingDirectory))
	}
	references := map[string]string{}
	for role, input := range suite.Inputs {
		references["inputs."+role+".path"] = path.Join(inputsPath, role, input.Output)
		references["inputs."+role+".version"] = input.Version
	}
	environment := make([]string, 0, len(suite.Environment))
	for _, name := range slices.Sorted(maps.Keys(suite.Environment)) {
		environment = append(environment, name+"="+substitute(suite.Environment[name], references))
	}
	command := pipeline.IsolatedCommand{Image: suite.Image, Platform: suite.Platform, Source: source, Cache: cache,
		WorkingDirectory: suite.WorkingDirectory, Environment: environment}
	if suite.Acquisition != nil {
		acquire := command
		acquire.Command, acquire.Timeout = substituteAll(suite.Acquisition.Command, references), acquisitionTimeout
		if err := x.run(ctx, acquire, options.Stdout, options.Stderr); err != nil {
			return setupFailure(ctx, result, codedAs(err, e.CodeBuildFailed, "acquisition command failed"))
		}
	}
	timeout, err := time.ParseDuration(suite.Timeout)
	if err != nil {
		return setupFailure(ctx, result, e.Wrap(e.CodeInternal, err, "admitted suite timeout"))
	}
	test := command
	test.Offline, test.Timeout = true, timeout
	test.Command = substituteAll(suite.Command, references)
	for _, role := range slices.Sorted(maps.Keys(staged)) {
		test.Inputs = append(test.Inputs, pipeline.IsolatedMount{Source: staged[role], Target: path.Join(inputsPath, role, suite.Inputs[role].Output)})
	}
	result.State = ExecutionRunning
	verdictErr := processVerdict(ctx, x.run(ctx, test, options.Stdout, options.Stderr), result)
	for _, role := range slices.Sorted(maps.Keys(staged)) {
		if digest, err := fileDigest(staged[role]); err != nil || digest != suite.Inputs[role].Digest {
			result.Status, result.Reason = StatusExecutionError, fmt.Sprintf("input %q bytes changed during execution", role)
			return e.New(e.CodeVerifyFailed, "suite %q input %q bytes changed during execution", suite.ID, role)
		}
	}
	return verdictErr
}

// stageInputs writes each input's checked executable to <dir>/<role>/<output>,
// read-only, and requires its bytes to be the bound build subject.
func (b *BoundValidation) stageInputs(ctx context.Context, suite BoundSuite, dir string) (map[string]string, error) {
	staged := map[string]string{}
	for _, role := range slices.Sorted(maps.Keys(suite.Inputs)) {
		input := suite.Inputs[role]
		if err := os.MkdirAll(filepath.Join(dir, role), 0o700); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "creating input directory")
		}
		file := filepath.Join(dir, role, input.Output)
		destination, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o555)
		if err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "staging input %q", role)
		}
		hash := sha256.New()
		member, writeErr := b.run.WriteExecutableMember(ctx, input.Output, input.Component, input.Platform, maxExecutableBytes, io.MultiWriter(destination, hash))
		closeErr := destination.Close()
		if writeErr != nil {
			return nil, codedAs(writeErr, e.CodeVerifyFailed, "staging input %q", role)
		}
		if closeErr != nil {
			return nil, e.Wrap(e.CodeBuildFailed, closeErr, "staging input %q", role)
		}
		if member.PayloadDigest != input.Digest || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != input.Digest {
			return nil, e.New(e.CodeVerifyFailed, "staged input %q is not the bound build subject", role)
		}
		staged[role] = file
	}
	return staged, nil
}

// processVerdict maps the suite command's outcome to a verdict. Container
// runtime exit statuses 125-127 mean the command could not be launched.
func processVerdict(ctx context.Context, err error, result *SuiteExecution) error {
	if err == nil {
		code := 0
		result.Termination, result.ExitCode, result.Status = TerminationExited, &code, StatusPassed
		return nil
	}
	result.Status = StatusExecutionError
	var status *exec.ExitError
	var coded *e.Error
	switch {
	case errors.As(err, &status) && status.ExitCode() > 0 && (status.ExitCode() < 125 || status.ExitCode() > 127):
		code := status.ExitCode()
		result.Termination, result.ExitCode, result.Status = TerminationExited, &code, StatusFailed
		result.Reason = fmt.Sprintf("suite command exited with status %d", code)
		return codedAs(err, e.CodeBuildFailed, "suite command failed")
	case errors.As(err, &coded) && coded.Code == e.CodeTimeout && ctx.Err() != nil:
		result.Termination, result.Reason = TerminationCanceled, "suite command canceled"
	case errors.As(err, &coded) && coded.Code == e.CodeTimeout:
		result.Termination, result.Reason = TerminationTimeout, "suite command exceeded its timeout"
	default:
		result.Termination, result.Reason = TerminationLaunchError, "suite command could not be launched or its output delivered: "+err.Error()
	}
	return codedAs(err, e.CodeBuildFailed, "suite command failed")
}

// setupFailure records a failure before the suite command ran.
func setupFailure(ctx context.Context, result *SuiteExecution, err error) error {
	result.Status, result.Reason = StatusExecutionError, "setup failed: "+err.Error()
	if ctx.Err() != nil {
		result.Termination = TerminationCanceled
	}
	return err
}

// codedAs keeps a coded error's code and wraps an uncoded one with code.
func codedAs(err error, code e.Code, format string, args ...any) error {
	var coded *e.Error
	if errors.As(err, &coded) {
		return err
	}
	return e.Wrap(code, err, format, args...)
}

// substitute replaces each admitted ${inputs.<role>.path|version} reference as
// plain text; loading refused every other ${ text.
func substitute(value string, references map[string]string) string {
	var output strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			return output.String() + value
		}
		end := strings.IndexByte(value[start:], '}')
		if end < 0 {
			return output.String() + value
		}
		output.WriteString(value[:start])
		output.WriteString(references[value[start+2:start+end]])
		value = value[start+end+1:]
	}
}

func substituteAll(values []string, references map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, substitute(value, references))
	}
	return result
}

func fileDigest(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
