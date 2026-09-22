package lifecycle

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

type LocalOptions struct {
	AttemptsRoot string
	RunnerSource string
	Jobs         int
	Runtime      string
	Go           string
	Publish      bool
	DryRun       bool
	Stdout       io.Writer
	Stderr       io.Writer
}

// ExecuteLocal performs one new attempt and independently observes every output
// before binding observations. Existing attempt storage is immutable.
func ExecuteLocal(ctx context.Context, prepared *PreparedRun, options LocalOptions) (*Run, error) {
	if prepared == nil || prepared.plan == nil || prepared.run == nil {
		return nil, e.New(e.CodeUsage, "local execution requires a prepared run")
	}
	root := options.AttemptsRoot
	if root == "" {
		root = filepath.ToSlash(filepath.Join(".nkd", "runs"))
	}
	workspace := filepath.ToSlash(filepath.Join(root, prepared.run.Attempt))
	recordPath := filepath.Join(workspace, "lifecycle.json")
	factoryOptions := factory.LocalRunOptions{Workspace: workspace, RunnerSource: options.RunnerSource,
		Jobs: options.Jobs, Runtime: options.Runtime, Go: options.Go, Publish: options.Publish,
		DryRun: options.DryRun, Stdout: options.Stdout, Stderr: options.Stderr}
	if err := factory.AdmitLocal(ctx, prepared.plan, factoryOptions); err != nil {
		return nil, err
	}
	if options.DryRun {
		if _, err := factory.RunLocal(ctx, prepared.plan, factoryOptions); err != nil {
			return nil, err
		}
		description := prepared.Description()
		return &description, nil
	}
	if !options.DryRun {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating run attempts root")
		}
		if err := os.Mkdir(workspace, 0o700); errors.Is(err, os.ErrExist) {
			return nil, e.New(e.CodeImmutableViolation, "run attempt %q already exists", prepared.run.Attempt)
		} else if err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "reserving run attempt storage")
		}
	}
	result, err := factory.RunLocal(ctx, prepared.plan, factoryOptions)
	if err != nil {
		return recordRunFailure(ctx, recordPath, prepared.run, err, "recording failed run attempt")
	}
	if result == nil {
		return recordRunFailure(ctx, recordPath, prepared.run, e.New(e.CodeInternal, "local executor returned no run record"), "recording failed run attempt")
	}
	defer func() { _ = result.Close() }()
	runForObservation := prepared.Description()
	runForObservation.Lane = result.Qualification.String()
	artifacts := make(map[string]factory.Artifact, len(prepared.plan.Graph.Artifacts))
	for _, artifact := range prepared.plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	observed, err := observeOutputs(ctx, workspace, prepared.run.Expected, artifacts, prepared.plan, result)
	if err != nil {
		return recordRunFailure(ctx, recordPath, &runForObservation,
			closeLocalExecutionResult(result, err), "recording failed run attempt")
	}
	bound, err := bindObserved(&runForObservation, observed)
	if err != nil {
		return recordRunFailure(ctx, recordPath, &runForObservation,
			closeLocalExecutionResult(result, err), "recording failed run attempt")
	}
	bound, err = bindReleaseContent(prepared.plan, bound)
	if err != nil {
		return recordRunFailure(ctx, recordPath, &runForObservation,
			closeLocalExecutionResult(result, err), "recording failed run attempt")
	}
	if err := result.Close(); err != nil {
		return recordRunFailure(ctx, recordPath, &runForObservation, err, "recording failed run attempt")
	}
	if err := writeRunRecord(ctx, recordPath, bound); err != nil {
		return nil, err
	}
	return bound, nil
}

func closeLocalExecutionResult(result *factory.LocalRunResult, cause error) error {
	closeErr := result.Close()
	if closeErr == nil {
		return cause
	}
	if cause == nil {
		return closeErr
	}
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(cause, &coded) {
		code = coded.Code
	}
	return e.Wrap(code, errors.Join(cause, closeErr),
		"local Run failed and closing disk results also failed")
}
