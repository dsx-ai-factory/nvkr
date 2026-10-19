// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
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
	// Activity is optional descriptive telemetry, never an execution receipt.
	Activity *buildactivity.Sink
	// Progress carries bounded descriptions, never execution or output authority.
	Progress *progress.Monitor
	// DiskInspection opts into diagnostic installed-package inspection. The
	// caller owns this already-admitted runtime and keeps it open for execution.
	// Omitting it preserves the existing build-only path, not an inspection claim.
	// DryRun does not admit or execute this runtime: it returns only the pending,
	// static-unqualified Run description, without observed outputs or inventories.
	DiskInspection *sbom.DiskInspectionRuntime
}

// ExecuteLocal performs one new attempt and independently observes every output
// before binding observations. Existing attempt storage is immutable.
func ExecuteLocal(ctx context.Context, prepared *PreparedRun, options LocalOptions) (*Run, error) {
	return executeLocalWithFactoryRunner(ctx, prepared, options,
		func(ctx context.Context, plan *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
			result, err := factory.RunLocal(ctx, plan, options)
			return adaptLocalExecutionResult(result, err)
		})
}

// Convert a nil concrete result to a nil interface, not a typed-nil observer.
func adaptLocalExecutionResult(result *factory.LocalRunResult, err error) (localExecutionResult, error) {
	if result == nil {
		return nil, err
	}
	return result, err
}

type localExecutionResult interface {
	diskPayloadObserver
	QualificationLane() string
	Close() error
}

type localFactoryRunner func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error)

type localPackerDiagnostics struct {
	capability *pipeline.PackerDiagnosticSink
	close      func() error
}

type localExecutionDependencies struct {
	admitLocal       func(context.Context, *factory.Plan, factory.LocalRunOptions) error
	runFactory       localFactoryRunner
	openDiagnostics  func(context.Context, string) (*localPackerDiagnostics, error)
	writeRecordState func(context.Context, string, *Run) (bool, error)
	beginDiskCommit  func(string) (*diskAttemptTransaction, error)
	admitInspection  func(context.Context, *factory.Plan, *sbom.DiskInspectionRuntime) error
	inspectDisks     func(context.Context, *factory.Plan, localExecutionResult, *sbom.DiskInspectionRuntime) ([]factory.DiskInstalledInventoryDescription, error)
}

func executeLocalWithFactoryRunner(ctx context.Context, prepared *PreparedRun, options LocalOptions,
	runFactory localFactoryRunner,
) (*Run, error) {
	return executeLocalWithDependencies(ctx, prepared, options, localExecutionDependencies{
		admitLocal: factory.AdmitLocal, runFactory: runFactory,
		openDiagnostics: newLocalPackerDiagnosticSink, writeRecordState: writeRunRecordState,
		beginDiskCommit: beginDiskAttemptTransaction,
	})
}

func executeLocalWithDependencies(ctx context.Context, prepared *PreparedRun, options LocalOptions,
	deps localExecutionDependencies,
) (returnedRun *Run, returnedErr error) {
	if prepared == nil || prepared.plan == nil || prepared.run == nil {
		return nil, e.New(e.CodeUsage, "local execution requires a prepared run")
	}
	if !options.Progress.Matches(prepared.run.Attempt, prepared.run.PlanDigest) {
		options.Progress = nil
	}
	defer func() { finishProgress(options.Progress, returnedRun, returnedErr, options.DryRun) }()
	if deps.admitLocal == nil || deps.runFactory == nil || deps.writeRecordState == nil || deps.beginDiskCommit == nil {
		return nil, e.New(e.CodeInternal, "local execution dependencies are incomplete")
	}
	if deps.openDiagnostics == nil {
		deps.openDiagnostics = newLocalPackerDiagnosticSink
	}
	if deps.inspectDisks == nil {
		deps.inspectDisks = inspectLocalDisks
	}
	if deps.admitInspection == nil {
		deps.admitInspection = admitDiskInspection
	}
	root := options.AttemptsRoot
	if root == "" {
		root = filepath.ToSlash(filepath.Join(".nvkr", "runs"))
	}
	workspace := filepath.ToSlash(filepath.Join(root, prepared.run.Attempt))
	recordPath := filepath.Join(workspace, "lifecycle.json")
	factoryOptions := factory.LocalRunOptions{Workspace: workspace, RunnerSource: options.RunnerSource,
		CompileInputs: prepared.inputs,
		Jobs:          options.Jobs, Runtime: options.Runtime, Go: options.Go, Publish: options.Publish,
		DryRun: options.DryRun, Stdout: options.Stdout, Stderr: options.Stderr, Activity: options.Activity, Progress: options.Progress}
	// Workspace-independent admission always precedes immutable attempt
	// reservation. RunLocal owns only the later held-workspace, capacity, and
	// KVM admission for disk plans.
	if err := deps.admitLocal(ctx, prepared.plan, factoryOptions); err != nil {
		return nil, err
	}
	mode := "local"
	if options.DryRun {
		mode = "dry-run"
	}
	options.Progress.Start(mode)
	if options.DryRun {
		if _, err := deps.runFactory(ctx, prepared.plan, factoryOptions); err != nil {
			return nil, err
		}
		description := prepared.Description()
		return &description, nil
	}
	if options.DiskInspection != nil {
		if err := deps.admitInspection(ctx, prepared.plan, options.DiskInspection); err != nil {
			return nil, err
		}
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
	var diagnostics *localPackerDiagnostics
	if localPlanUsesPacker(prepared.plan) {
		var err error
		diagnostics, err = deps.openDiagnostics(ctx, workspace)
		if err != nil {
			return recordRunFailure(ctx, recordPath, prepared.run, err,
				"recording failed run attempt after diagnostic admission")
		}
		if diagnostics == nil || diagnostics.capability == nil || diagnostics.close == nil {
			if diagnostics != nil && diagnostics.close != nil {
				_ = diagnostics.close()
			}
			return recordRunFailure(ctx, recordPath, prepared.run,
				e.New(e.CodeInternal, "local Packer diagnostic admission returned an incomplete capability"),
				"recording failed run attempt after diagnostic admission")
		}
	}
	if diagnostics != nil {
		factoryOptions.Diagnostics = diagnostics.capability
	}
	result, err := deps.runFactory(ctx, prepared.plan, factoryOptions)
	var diagnosticCloseErr error
	if diagnostics != nil {
		diagnosticCloseErr = diagnostics.close()
	}
	if err != nil {
		err = errors.Join(err, diagnosticCloseErr)
		return recordRunFailure(ctx, recordPath, prepared.run, err, "recording failed run attempt")
	}
	if diagnosticCloseErr != nil {
		if result != nil {
			diagnosticCloseErr = closeLocalExecutionResult(result, diagnosticCloseErr)
		}
		return recordRunFailure(ctx, recordPath, prepared.run, diagnosticCloseErr,
			"recording failed run attempt after diagnostic retention")
	}
	if result == nil {
		return recordRunFailure(ctx, recordPath, prepared.run, e.New(e.CodeInternal, "local executor returned no run record"), "recording failed run attempt")
	}
	defer func() { _ = result.Close() }()
	runForObservation := prepared.Description()
	runForObservation.Lane = result.QualificationLane()
	options.Progress.SetPhase("observation")
	if prepared.inputs != "" {
		if err := retainLocalDiskMaterials(ctx, workspace, prepared, result); err != nil {
			cause := closeLocalExecutionResult(result, err)
			return recordRunFailure(ctx, recordPath, &runForObservation, cause,
				"recording failed Run after live material retention")
		}
	}
	if options.DiskInspection != nil {
		installed, err := deps.inspectDisks(ctx, prepared.plan, result, options.DiskInspection)
		if err == nil && len(installed) == 0 {
			err = e.New(e.CodeVerifyFailed, "requested disk inspection returned no installed inventories")
		}
		if err != nil {
			cause := closeLocalExecutionResult(result, err)
			return recordRunFailure(ctx, recordPath, &runForObservation, cause,
				"recording failed Run after installed-package inspection")
		}
		runForObservation.diskInventories = installed
	}
	artifacts := make(map[string]factory.Artifact, len(prepared.plan.Graph.Artifacts))
	for _, artifact := range prepared.plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	diskExports := newDiskExportTransaction(workspace)
	observed, err := observeOutputsWithDiskStaging(ctx, workspace, prepared.run.Expected,
		artifacts, prepared.plan, result, diskExports.observe, options.Progress)
	if err != nil {
		cause := closeLocalExecutionResult(result, err)
		if cleanupErr := cleanupDiskExports(diskExports.stages, false); cleanupErr != nil {
			return nil, diskCleanupFailure(cause, cleanupErr)
		}
		return recordRunFailure(ctx, recordPath, &runForObservation, cause, "recording failed run attempt")
	}
	options.Progress.SetPhase("finalization")
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
		// The immutable record and byte-verified disk outputs are already
		// committed. Marker cleanup cannot revoke that successful result.
		releaseDiskExportHandles(diskExports.stages)
		if closeErr := attemptTransaction.close(); closeErr != nil {
			return bound, e.Wrap(e.CodeImmutableViolation, errors.Join(err, closeErr),
				"closing completed local disk transaction after cleanup failure")
		}
		return bound, err
	}
	// Marker-directory sync is the durable success commit point. Descriptor
	// close cannot change the completed outcome after it.
	releaseDiskExportHandles(diskExports.stages)
	return bound, nil
}

func newLocalPackerDiagnosticSink(ctx context.Context, workspace string) (*localPackerDiagnostics, error) {
	root, _, err := securepath.OpenDirectory(workspace)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err,
			"holding local attempt directory for Packer diagnostics")
	}
	sink, sinkErr := pipeline.NewPackerDiagnosticSink(ctx, root)
	closeErr := root.Close()
	if sinkErr != nil {
		return nil, errors.Join(sinkErr, closeErr)
	}
	if closeErr != nil {
		return nil, errors.Join(e.Wrap(e.CodeImmutableViolation, closeErr,
			"closing local attempt diagnostic directory"), sink.Close())
	}
	return &localPackerDiagnostics{capability: sink, close: sink.Close}, nil
}

func localPlanUsesPacker(plan *factory.Plan) bool {
	for _, build := range plan.Builds {
		for _, node := range build.Graph.Nodes {
			if node.Kind == ir.KindBuild && node.Packer != nil {
				return true
			}
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
