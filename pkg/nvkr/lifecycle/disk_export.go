// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"golang.org/x/sys/unix"
)

const localDiskOutputDirectory = "outputs"

const diskAttemptMarker = ".disk-commit-in-progress"

type diskExportStage struct {
	parent           *os.File
	temporary        string
	target           string
	path             string
	committed        bool
	temporaryRemoved bool
	committedRemoved bool
	syncParent       func(*os.File) error
	closeParent      func(*os.File) error
}

type diskPayloadExporter interface {
	ExportDiskLifecyclePayload(context.Context, *factory.Plan, factory.LifecycleOutputPlan, io.Writer) (factory.DiskPayloadObservation, error)
}

type diskExportTransaction struct {
	workspace string
	stages    []*diskExportStage
}

type diskAttemptTransaction struct {
	root          *os.File
	marker        bool
	recordLinked  bool
	recordRemoved bool
	markerRemoved bool
	unlink        func(int, string, int) error
	syncRoot      func(*os.File) error
	closeRoot     func(*os.File) error
}

func newDiskExportTransaction(workspace string) *diskExportTransaction {
	return &diskExportTransaction{workspace: workspace}
}

func beginDiskAttemptTransaction(workspace string) (*diskAttemptTransaction, error) {
	root, _, err := securepath.OpenDirectory(workspace)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "holding local attempt workspace")
	}
	fd, err := unix.Openat(int(root.Fd()), diskAttemptMarker,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
	if err != nil {
		_ = root.Close()
		return nil, e.Wrap(e.CodeImmutableViolation, err, "starting local disk commit transaction")
	}
	marker := os.NewFile(uintptr(fd), diskAttemptMarker)
	markerErr := errors.Join(marker.Sync(), marker.Close(), root.Sync())
	if markerErr != nil {
		cleanupErr := unix.Unlinkat(int(root.Fd()), diskAttemptMarker, 0)
		if cleanupErr == nil {
			cleanupErr = root.Sync()
		}
		cleanupErr = errors.Join(cleanupErr, root.Close())
		return nil, e.Wrap(e.CodeImmutableViolation, errors.Join(markerErr, cleanupErr),
			"persisting local disk commit transaction")
	}
	return &diskAttemptTransaction{root: root, marker: true,
		unlink: unix.Unlinkat, syncRoot: func(file *os.File) error { return file.Sync() },
		closeRoot: func(file *os.File) error { return file.Close() }}, nil
}

func (transaction *diskAttemptTransaction) rollback(recordLinked bool, stages []*diskExportStage) error {
	if transaction == nil || transaction.root == nil {
		return e.New(e.CodeInternal, "local disk commit transaction is unavailable")
	}
	transaction.recordLinked = transaction.recordLinked || recordLinked
	var rollbackErr error
	if transaction.recordLinked && !transaction.recordRemoved {
		if err := transaction.unlink(int(transaction.root.Fd()), "lifecycle.json", 0); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		} else {
			transaction.recordRemoved = true
		}
	}
	rollbackErr = errors.Join(rollbackErr, cleanupDiskExports(stages, false))
	if rollbackErr != nil {
		// Keep the marker and held descriptors so a caller can retry cleanup.
		return rollbackErr
	}
	if transaction.marker && !transaction.markerRemoved {
		if err := transaction.unlink(int(transaction.root.Fd()), diskAttemptMarker, 0); err != nil {
			return err
		}
		transaction.markerRemoved = true
	}
	if err := transaction.syncRoot(transaction.root); err != nil {
		return err
	}
	transaction.recordLinked = false
	transaction.recordRemoved = false
	transaction.marker = false
	transaction.markerRemoved = false
	// Root sync above is the durable rollback point. A descriptor-close error
	// after it cannot undo the deletion of the record, disk outputs, or marker,
	// and must not prevent the caller from persisting the failed Run.
	_ = transaction.close()
	return nil
}

func (transaction *diskAttemptTransaction) complete() error {
	if transaction == nil || transaction.root == nil || !transaction.marker || transaction.markerRemoved {
		return e.New(e.CodeInternal, "local disk commit transaction is incomplete")
	}
	if err := transaction.unlink(int(transaction.root.Fd()), diskAttemptMarker, 0); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "completing local disk commit transaction")
	}
	transaction.markerRemoved = true
	if err := transaction.syncRoot(transaction.root); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"syncing completed local disk transaction")
	}
	transaction.marker = false
	transaction.markerRemoved = false
	// Directory sync above is the durable success commit point. Descriptor-close
	// failures after it cannot make the already-committed Run fail.
	_ = transaction.close()
	return nil
}

func (transaction *diskAttemptTransaction) close() error {
	if transaction == nil || transaction.root == nil {
		return nil
	}
	err := transaction.closeRoot(transaction.root)
	transaction.root = nil
	return err
}

func describeExistingAttempt(workspace, attempt string) error {
	root, _, err := securepath.OpenDirectory(workspace)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"inspecting existing run attempt %q", attempt)
	}
	defer root.Close()
	exists := func(name string) bool {
		var stat unix.Stat_t
		return unix.Fstatat(int(root.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW) == nil
	}
	markerExists := exists(diskAttemptMarker)
	recordExists := exists("lifecycle.json")
	if markerExists && recordExists {
		return e.New(e.CodeImmutableViolation,
			"run attempt %q has an immutable lifecycle record and an incomplete local disk commit cleanup", attempt)
	}
	if markerExists {
		return e.New(e.CodeImmutableViolation,
			"run attempt %q contains an incomplete local disk commit transaction", attempt)
	}
	if recordExists {
		return e.New(e.CodeImmutableViolation, "run attempt %q already has an immutable lifecycle record", attempt)
	}
	return e.New(e.CodeImmutableViolation,
		"run attempt %q was interrupted before its lifecycle record was committed", attempt)
}

func (transaction *diskExportTransaction) observe(ctx context.Context, slot requiredOutput,
	plan *factory.Plan, execution diskPayloadExporter,
) (Artifact, error) {
	if execution == nil {
		return Artifact{}, e.New(e.CodeEngineCapability,
			"durable disk export requires a held local execution result")
	}
	if slot.Kind == OutputDebianPackage {
		return transaction.observeDebian(ctx, slot, plan)
	}
	if slot.Kind != OutputDiskImage {
		return Artifact{}, e.New(e.CodeInternal,
			"durable disk export received non-disk output %q", slot.Name)
	}
	path, err := LocalDiskOutputPath(transaction.workspace, slot.Name)
	if err != nil {
		return Artifact{}, err
	}
	file, err := transaction.openStage(path)
	if err != nil {
		return Artifact{}, err
	}
	expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind,
		ArtifactID: slot.MemberArtifactID, BuildDigest: slot.BuildDigest,
		Components: append([]string(nil), slot.Components...)}
	observation, exportErr := execution.ExportDiskLifecyclePayload(ctx, plan, expected, file)
	if exportErr == nil {
		exportErr = file.Sync()
	}
	if exportErr == nil {
		exportErr = file.Chmod(0o444)
	}
	if exportErr == nil {
		exportErr = file.Sync()
	}
	closeErr := file.Close()
	if exportErr == nil {
		exportErr = closeErr
	}
	if exportErr != nil {
		if ctx.Err() != nil {
			return Artifact{}, e.Wrap(e.CodeTimeout, ctx.Err(),
				"staging durable disk output %q", slot.Name)
		}
		return Artifact{}, e.Wrap(e.CodeVerifyFailed, exportErr,
			"staging durable disk output %q", slot.Name)
	}
	return artifactFromDiskObservation(slot, observation), nil
}

func commitDiskExports(stages []*diskExportStage) error {
	for _, stage := range stages {
		if err := transportarchive.PublishAtNoReplace(int(stage.parent.Fd()), stage.temporary,
			int(stage.parent.Fd()), stage.target); err != nil {
			return e.Wrap(e.CodeImmutableViolation, err,
				"committing durable disk output %s", stage.path)
		}
		stage.committed = true
		stage.temporary = ""
	}
	for _, stage := range stages {
		if err := stage.parent.Sync(); err != nil {
			return e.Wrap(e.CodeImmutableViolation, err,
				"syncing durable disk output directory")
		}
	}
	return nil
}

func cleanupDiskExports(stages []*diskExportStage, keepCommitted bool) error {
	var cleanupErr error
	for _, stage := range stages {
		if stage == nil || stage.parent == nil {
			continue
		}
		var stageErr error
		if stage.temporary != "" && !stage.temporaryRemoved {
			if err := unix.Unlinkat(int(stage.parent.Fd()), stage.temporary, 0); err != nil {
				stageErr = errors.Join(stageErr, err)
			} else {
				stage.temporaryRemoved = true
			}
		}
		if stage.committed && !keepCommitted && !stage.committedRemoved {
			if err := unix.Unlinkat(int(stage.parent.Fd()), stage.target, 0); err != nil {
				stageErr = errors.Join(stageErr, err)
			} else {
				stage.committedRemoved = true
			}
		}
		if stageErr != nil {
			cleanupErr = errors.Join(cleanupErr, stageErr)
			continue
		}
		if syncErr := stage.syncParent(stage.parent); syncErr != nil {
			cleanupErr = errors.Join(cleanupErr, syncErr)
			continue
		}
		if stage.temporaryRemoved {
			stage.temporary = ""
			stage.temporaryRemoved = false
		}
		if stage.committedRemoved {
			stage.committed = false
			stage.committedRemoved = false
		}
		// Directory sync above is the durable cleanup point. Descriptor close
		// cannot restore an unlinked output, so a close failure is non-authoritative
		// and must not block failed-Run persistence or marker completion.
		closeDiskExportParent(stage)
	}
	return cleanupErr
}

func releaseDiskExportHandles(stages []*diskExportStage) {
	for _, stage := range stages {
		if stage != nil && stage.parent != nil {
			closeDiskExportParent(stage)
		}
	}
}

func closeDiskExportParent(stage *diskExportStage) {
	if stage == nil || stage.parent == nil {
		return
	}
	if stage.closeParent != nil {
		_ = stage.closeParent(stage.parent)
	} else {
		_ = stage.parent.Close()
	}
	stage.parent = nil
}

// LocalDiskOutputPath returns the deterministic descriptive path for a disk
// payload inside one already-reserved local attempt workspace. It grants no
// filesystem or artifact authority; lifecycle execution still resolves and
// commits the path beneath its held attempt directory.
func LocalDiskOutputPath(workspace, outputName string) (string, error) {
	if workspace == "" || !namePattern.MatchString(outputName) {
		return "", e.New(e.CodeUsage,
			"local disk output requires an attempt workspace and canonical output name")
	}
	return filepath.Join(workspace, localDiskOutputDirectory, outputName+".qcow2"), nil
}

func (transaction *diskExportTransaction) openStage(path string) (*os.File, error) {
	relative, err := filepath.Rel(transaction.workspace, path)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err,
			"deriving durable output %q", path)
	}
	parent, target, err := securepath.CreateParentBeneath(transaction.workspace, relative)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err,
			"resolving durable output %q", path)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), target, &stat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		_ = parent.Close()
		return nil, e.New(e.CodeImmutableViolation,
			"durable output %q already exists", path)
	} else if err != unix.ENOENT {
		_ = parent.Close()
		return nil, e.Wrap(e.CodeImmutableViolation, err,
			"checking durable output %q", path)
	}
	file, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		_ = parent.Close()
		return nil, e.Wrap(e.CodeImmutableViolation, err,
			"staging durable output %q", path)
	}
	stage := &diskExportStage{parent: parent, temporary: temporary, target: target, path: path,
		syncParent:  func(file *os.File) error { return file.Sync() },
		closeParent: func(file *os.File) error { return file.Close() }}
	transaction.stages = append(transaction.stages, stage)
	return file, nil
}

func (transaction *diskExportTransaction) observeDebian(ctx context.Context, slot requiredOutput, plan *factory.Plan) (Artifact, error) {
	if ctx == nil {
		return Artifact{}, e.New(e.CodeUsage, "raw package staging requires context")
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, e.Wrap(e.CodeTimeout, err, "raw package staging canceled")
	}
	if !namePattern.MatchString(slot.Name) || slot.Kind != OutputDebianPackage {
		return Artifact{}, e.New(e.CodeVerifyFailed, "raw package staging requires an exact named output")
	}
	path := filepath.Join(transaction.workspace, localDiskOutputDirectory, slot.Name+".deb")
	file, err := transaction.openStage(path)
	if err != nil {
		return Artifact{}, err
	}
	expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind, ArtifactID: slot.MemberArtifactID,
		BuildDigest: slot.BuildDigest, Components: append([]string(nil), slot.Components...)}
	observation, exportErr := factory.ExportWorkspaceDebianPayload(ctx, plan, expected, transaction.workspace, file)
	if exportErr == nil {
		exportErr = finishNativeStage(file)
	} else {
		exportErr = errors.Join(exportErr, file.Close())
	}
	if exportErr != nil {
		code := e.CodeVerifyFailed
		var coded *e.Error
		if errors.As(exportErr, &coded) {
			code = coded.Code
		}
		return Artifact{}, e.Wrap(code, exportErr, "staging raw package output")
	}
	artifact := Artifact{Name: slot.Name, Kind: slot.Kind, Digest: observation.SHA256, BuildDigest: slot.BuildDigest,
		Components: append([]string(nil), slot.Components...), Reference: path}
	if err := artifact.Validate(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func finishNativeStage(file *os.File) error {
	err := file.Sync()
	if err == nil {
		err = file.Chmod(0444)
	}
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
