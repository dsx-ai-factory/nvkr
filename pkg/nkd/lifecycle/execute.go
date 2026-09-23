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
	return executeLocalWithFactoryRunner(ctx, prepared, options,
		func(ctx context.Context, plan *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
			return factory.RunLocal(ctx, plan, options)
		})
}

type localExecutionResult interface {
	diskPayloadObserver
	QualificationLane() string
	Close() error
}

type localFactoryRunner func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error)

type localExecutionDependencies struct {
	admitLocal       func(context.Context, *factory.Plan, factory.LocalRunOptions) error
	runFactory       localFactoryRunner
	writeRecordState func(context.Context, string, *Run) (bool, error)
	beginDiskCommit  func(string) (*diskAttemptTransaction, error)
}

func executeLocalWithFactoryRunner(ctx context.Context, prepared *PreparedRun, options LocalOptions,
	runFactory localFactoryRunner,
) (*Run, error) {
	return executeLocalWithDependencies(ctx, prepared, options, localExecutionDependencies{
		admitLocal: factory.AdmitLocal, runFactory: runFactory, writeRecordState: writeRunRecordState,
		beginDiskCommit: beginDiskAttemptTransaction,
	})
}

func executeLocalWithDependencies(ctx context.Context, prepared *PreparedRun, options LocalOptions,
	deps localExecutionDependencies,
) (*Run, error) {
	if prepared == nil || prepared.plan == nil || prepared.run == nil {
		return nil, e.New(e.CodeUsage, "local execution requires a prepared run")
	}
	if deps.admitLocal == nil || deps.runFactory == nil || deps.writeRecordState == nil || deps.beginDiskCommit == nil {
		return nil, e.New(e.CodeInternal, "local execution dependencies are incomplete")
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
	// Standalone factory admission intentionally rejects a non-dry-run VM Plan
	// because only RunLocal owns the held workspace needed for storage and KVM
	// admission. Ordinary and dry-run executions retain the mutation-free
	// preflight before attempt storage is reserved.
	if options.DryRun || !hasDiskLifecycleOutput(prepared.plan) {
		if err := deps.admitLocal(ctx, prepared.plan, factoryOptions); err != nil {
			return nil, err
		}
	}
	if options.DryRun {
		if _, err := deps.runFactory(ctx, prepared.plan, factoryOptions); err != nil {
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
			return nil, describeExistingAttempt(workspace, prepared.run.Attempt)
		} else if err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "reserving run attempt storage")
		}
	}
	result, err := deps.runFactory(ctx, prepared.plan, factoryOptions)
	if err != nil {
		return recordRunFailure(ctx, recordPath, prepared.run, err, "recording failed run attempt")
	}
	if result == nil {
		return recordRunFailure(ctx, recordPath, prepared.run, e.New(e.CodeInternal, "local executor returned no run record"), "recording failed run attempt")
	}
	defer func() { _ = result.Close() }()
	runForObservation := prepared.Description()
	runForObservation.Lane = result.QualificationLane()
	artifacts := make(map[string]factory.Artifact, len(prepared.plan.Graph.Artifacts))
	for _, artifact := range prepared.plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	diskExports := newDiskExportTransaction(workspace)
	observed, err := observeOutputsWithDiskStaging(ctx, workspace, prepared.run.Expected,
		artifacts, prepared.plan, result, diskExports.observe)
	if err != nil {
		cause := closeLocalExecutionResult(result, err)
		if cleanupErr := cleanupDiskExports(diskExports.stages, false); cleanupErr != nil {
			return nil, diskCleanupFailure(cause, cleanupErr)
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, cause, "recording failed run attempt")
	}
	bound, err := bindObserved(&runForObservation, observed)
	if err != nil {
		cause := closeLocalExecutionResult(result, err)
		if cleanupErr := cleanupDiskExports(diskExports.stages, false); cleanupErr != nil {
			return nil, diskCleanupFailure(cause, cleanupErr)
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, cause, "recording failed run attempt")
	}
	bound, err = bindReleaseContent(prepared.plan, bound)
	if err != nil {
		cause := closeLocalExecutionResult(result, err)
		if cleanupErr := cleanupDiskExports(diskExports.stages, false); cleanupErr != nil {
			return nil, diskCleanupFailure(cause, cleanupErr)
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, cause, "recording failed run attempt")
	}
	if err := result.Close(); err != nil {
		if cleanupErr := cleanupDiskExports(diskExports.stages, false); cleanupErr != nil {
			return nil, diskCleanupFailure(err, cleanupErr)
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, err, "recording failed run attempt")
	}
	// Ordinary outputs retain the original immutable-record path. The disk
	// marker transaction is needed only when held disk bytes were staged and
	// must be committed atomically with the Run record.
	if len(diskExports.stages) == 0 {
		if _, err := deps.writeRecordState(ctx, recordPath, bound); err != nil {
			return nil, err
		}
		return bound, nil
	}
	attemptTransaction, err := deps.beginDiskCommit(workspace)
	if err != nil {
		if cleanupErr := cleanupDiskExports(diskExports.stages, false); cleanupErr != nil {
			return nil, diskCleanupFailure(err, cleanupErr)
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, err, "recording failed run attempt")
	}
	if err := commitDiskExports(diskExports.stages); err != nil {
		rollbackErr := attemptTransaction.rollback(false, diskExports.stages)
		if rollbackErr != nil {
			return nil, diskCleanupFailure(err, rollbackErr)
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, err, "recording failed run attempt")
	}
	recordLinked, err := deps.writeRecordState(ctx, recordPath, bound)
	if err != nil {
		rollbackErr := attemptTransaction.rollback(recordLinked, diskExports.stages)
		if rollbackErr != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, errors.Join(err, rollbackErr),
				"committing run record and rolling back disk outputs")
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, err,
			"recording failed run after local disk commit rollback")
	}
	if err := attemptTransaction.complete(); err != nil {
		rollbackErr := attemptTransaction.rollback(recordLinked, diskExports.stages)
		if rollbackErr != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, errors.Join(err, rollbackErr),
				"completing local disk transaction and rolling it back")
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, err,
			"recording failed run after local disk transaction rollback")
	}
	// Marker-directory sync is the durable success commit point. Descriptor
	// close cannot change the completed outcome after it.
	releaseDiskExportHandles(diskExports.stages)
	return bound, nil
}

func hasDiskLifecycleOutput(plan *factory.Plan) bool {
	if plan == nil {
		return false
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == OutputDiskImage {
			return true
		}
	}
	return false
}

func closeLocalExecutionResult(result localExecutionResult, cause error) error {
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

func diskCleanupFailure(cause, cleanup error) error {
	return e.Wrap(e.CodeImmutableViolation, errors.Join(cause, cleanup),
		"local Run failed and durable disk cleanup also failed")
}
