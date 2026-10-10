// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

type fakeDiskExporter struct {
	payload     []byte
	observation factory.DiskPayloadObservation
	err         error
	closed      bool
	exports     int
}

func (exporter *fakeDiskExporter) ExportDiskLifecyclePayload(ctx context.Context, _ *factory.Plan,
	_ factory.LifecycleOutputPlan, destination io.Writer,
) (factory.DiskPayloadObservation, error) {
	if exporter.err != nil {
		return factory.DiskPayloadObservation{}, exporter.err
	}
	if err := ctx.Err(); err != nil {
		return factory.DiskPayloadObservation{}, err
	}
	if pipeline.FileDigest(exporter.payload) != exporter.observation.Digest ||
		int64(len(exporter.payload)) != exporter.observation.Size {
		return factory.DiskPayloadObservation{}, nvkrerr.New(nvkrerr.CodeVerifyFailed,
			"held disk payload changed")
	}
	exporter.exports++
	if _, err := destination.Write(exporter.payload); err != nil {
		return factory.DiskPayloadObservation{}, err
	}
	return exporter.observation, nil
}

func (exporter *fakeDiskExporter) ObserveDiskLifecyclePayload(context.Context, *factory.Plan,
	factory.LifecycleOutputPlan,
) (factory.DiskPayloadObservation, error) {
	return exporter.observation, exporter.err
}

func (exporter *fakeDiskExporter) QualificationLane() string {
	return factory.TierLocalExecution.String()
}

func (exporter *fakeDiskExporter) Close() error {
	exporter.closed = true
	return nil
}

func TestDiskExportTransactionCommitsObservedBytes(t *testing.T) {
	payload := []byte("synthetic-qcow2")
	digest := pipeline.FileDigest(payload)
	slot := requiredOutput{Name: "node-image", Kind: OutputDiskImage,
		MemberArtifactID: "artifact/node-image/image", BuildDigest: "sha256:" + strings.Repeat("1", 64),
		Components: []string{"image"}}
	exporter := &fakeDiskExporter{payload: payload, observation: factory.DiskPayloadObservation{
		Digest: digest, Size: int64(len(payload)), BuildDigest: slot.BuildDigest,
		Components: slot.Components, Producer: "build-image", Component: "image",
		Format: "qcow2", Encryption: "none", Outcome: "succeeded",
		PlanDigest: "sha256:" + strings.Repeat("2", 64),
	}}
	workspace := t.TempDir()
	transaction := newDiskExportTransaction(workspace)
	artifact, err := transaction.observe(t.Context(), slot, nil, exporter)
	if err != nil {
		t.Fatal(err)
	}
	path, err := LocalDiskOutputPath(workspace, slot.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged disk became visible before lifecycle binding: %v", err)
	}
	if artifact.Digest != digest || artifact.Disk == nil || artifact.Disk.Size != int64(len(payload)) {
		t.Fatalf("disk observation = %+v", artifact)
	}
	if err := commitDiskExports(transaction.stages); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDiskExports(transaction.stages, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, payload) {
		t.Fatalf("durable disk after export transaction cleanup = %q, error=%v", raw, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("durable disk mode = %v", info.Mode())
	}
}

func TestDiskExportTransactionFailsClosed(t *testing.T) {
	payload := []byte("synthetic-qcow2")
	slot := requiredOutput{Name: "node-image", Kind: OutputDiskImage,
		MemberArtifactID: "artifact/node-image/image", BuildDigest: "sha256:" + strings.Repeat("1", 64),
		Components: []string{"image"}}
	valid := factory.DiskPayloadObservation{
		Digest: pipeline.FileDigest(payload), Size: int64(len(payload)), BuildDigest: slot.BuildDigest,
		Components: slot.Components, Producer: "build-image", Component: "image",
		Format: "qcow2", Encryption: "none", Outcome: "succeeded",
		PlanDigest: "sha256:" + strings.Repeat("2", 64),
	}
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string) (context.Context, *fakeDiskExporter)
		code    nvkrerr.Code
	}{
		{name: "staged bytes differ from observation", code: nvkrerr.CodeVerifyFailed,
			prepare: func(_ *testing.T, _ string) (context.Context, *fakeDiskExporter) {
				return context.Background(), &fakeDiskExporter{payload: []byte("substituted"), observation: valid}
			}},
		{name: "canceled export", code: nvkrerr.CodeTimeout,
			prepare: func(_ *testing.T, _ string) (context.Context, *fakeDiskExporter) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, &fakeDiskExporter{payload: payload, observation: valid}
			}},
		{name: "existing output", code: nvkrerr.CodeImmutableViolation,
			prepare: func(t *testing.T, workspace string) (context.Context, *fakeDiskExporter) {
				path, err := LocalDiskOutputPath(workspace, slot.Name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
				return context.Background(), &fakeDiskExporter{payload: payload, observation: valid}
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			ctx, exporter := test.prepare(t, workspace)
			transaction := newDiskExportTransaction(workspace)
			_, err := transaction.observe(ctx, slot, nil, exporter)
			assertCode(t, err, test.code)
			_ = cleanupDiskExports(transaction.stages, false)
			path, pathErr := LocalDiskOutputPath(workspace, slot.Name)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			raw, readErr := os.ReadFile(path)
			if test.name == "existing output" {
				if readErr != nil || string(raw) != "existing" {
					t.Fatalf("existing output changed: %q, %v", raw, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("failed export left visible output: %q, %v", raw, readErr)
			}
		})
	}
}

func TestDiskExportCleanupRetainsRetryableState(t *testing.T) {
	payload := []byte("synthetic-qcow2")
	slot := requiredOutput{Name: "node-image", Kind: OutputDiskImage,
		MemberArtifactID: "artifact/node-image/image", BuildDigest: "sha256:" + strings.Repeat("1", 64),
		Components: []string{"image"}}
	exporter := &fakeDiskExporter{payload: payload, observation: factory.DiskPayloadObservation{
		Digest: pipeline.FileDigest(payload), Size: int64(len(payload)), BuildDigest: slot.BuildDigest,
		Components: slot.Components, Producer: "build-image", Component: "image",
		Format: "qcow2", Encryption: "none", Outcome: "succeeded",
		PlanDigest: "sha256:" + strings.Repeat("2", 64),
	}}
	workspace := t.TempDir()
	transaction := newDiskExportTransaction(workspace)
	if _, err := transaction.observe(t.Context(), slot, nil, exporter); err != nil {
		t.Fatal(err)
	}
	if err := commitDiskExports(transaction.stages); err != nil {
		t.Fatal(err)
	}
	stage := transaction.stages[0]
	if err := os.Remove(stage.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stage.path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDiskExports(transaction.stages, false); err == nil ||
		!stage.committed || stage.parent == nil {
		t.Fatalf("failed unlink discarded retry state: stage=%+v error=%v", stage, err)
	}
	if err := os.Remove(stage.path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage.path, payload, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDiskExports(transaction.stages, false); err != nil {
		t.Fatal(err)
	}
	if stage.committed || stage.parent != nil {
		t.Fatalf("successful cleanup retained stale state: %+v", stage)
	}
}

func TestDiskExportCleanupRetainsStateUntilDirectorySync(t *testing.T) {
	payload := []byte("synthetic-qcow2")
	slot := requiredOutput{Name: "node-image", Kind: OutputDiskImage,
		MemberArtifactID: "artifact/node-image/image", BuildDigest: "sha256:" + strings.Repeat("1", 64),
		Components: []string{"image"}}
	exporter := &fakeDiskExporter{payload: payload, observation: factory.DiskPayloadObservation{
		Digest: pipeline.FileDigest(payload), Size: int64(len(payload)), BuildDigest: slot.BuildDigest,
		Components: slot.Components, Producer: "build-image", Component: "image",
		Format: "qcow2", Encryption: "none", Outcome: "succeeded",
		PlanDigest: "sha256:" + strings.Repeat("2", 64),
	}}
	transaction := newDiskExportTransaction(t.TempDir())
	if _, err := transaction.observe(t.Context(), slot, nil, exporter); err != nil {
		t.Fatal(err)
	}
	if err := commitDiskExports(transaction.stages); err != nil {
		t.Fatal(err)
	}
	stage := transaction.stages[0]
	sentinel := errors.New("output directory sync failed")
	stage.syncParent = func(*os.File) error { return sentinel }
	if err := cleanupDiskExports(transaction.stages, false); !errors.Is(err, sentinel) ||
		!stage.committed || !stage.committedRemoved || stage.parent == nil {
		t.Fatalf("sync failure discarded retry state: stage=%+v error=%v", stage, err)
	}
	stage.syncParent = func(file *os.File) error { return file.Sync() }
	if err := cleanupDiskExports(transaction.stages, false); err != nil {
		t.Fatal(err)
	}
	if stage.committed || stage.committedRemoved || stage.parent != nil {
		t.Fatalf("sync retry retained stale state: %+v", stage)
	}
}

func TestDiskExportCleanupFinalizesUnaffectedStages(t *testing.T) {
	firstRoot := t.TempDir()
	firstParent, err := os.Open(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(firstRoot, "first.qcow2")
	if err := os.Mkdir(firstPath, 0o700); err != nil {
		t.Fatal(err)
	}
	first := &diskExportStage{parent: firstParent, target: filepath.Base(firstPath),
		path: firstPath, committed: true, syncParent: func(file *os.File) error { return file.Sync() }}

	secondRoot := t.TempDir()
	secondParent, err := os.Open(secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(secondRoot, "second.qcow2")
	if err := os.WriteFile(secondPath, []byte("second"), 0o444); err != nil {
		t.Fatal(err)
	}
	secondSyncs := 0
	second := &diskExportStage{parent: secondParent, target: filepath.Base(secondPath),
		path: secondPath, committed: true, syncParent: func(file *os.File) error {
			secondSyncs++
			return file.Sync()
		}}

	if err := cleanupDiskExports([]*diskExportStage{first, second}, false); err == nil {
		t.Fatal("cleanup succeeded despite the first-stage unlink failure")
	}
	if first.parent == nil || !first.committed {
		t.Fatalf("first-stage failure discarded retry state: %+v", first)
	}
	if second.parent != nil || second.committed || secondSyncs != 1 {
		t.Fatalf("unaffected second stage was not finalized independently: stage=%+v syncs=%d",
			second, secondSyncs)
	}
	if _, err := os.Stat(secondPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unaffected second output remained after cleanup: %v", err)
	}

	if err := os.Remove(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstPath, []byte("first"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := cleanupDiskExports([]*diskExportStage{first}, false); err != nil {
		t.Fatal(err)
	}
}

func TestDiskExportCleanupAggregatesIndependentStageFailures(t *testing.T) {
	firstFailure := errors.New("first output directory sync failed")
	secondFailure := errors.New("second output directory sync failed")
	newStage := func(t *testing.T, target string, syncErr error) *diskExportStage {
		t.Helper()
		root := t.TempDir()
		parent, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, target)
		if err := os.WriteFile(path, []byte(target), 0o444); err != nil {
			t.Fatal(err)
		}
		return &diskExportStage{parent: parent, target: target, path: path, committed: true,
			syncParent: func(*os.File) error { return syncErr }}
	}
	first := newStage(t, "first.qcow2", firstFailure)
	second := newStage(t, "second.qcow2", secondFailure)

	err := cleanupDiskExports([]*diskExportStage{first, second}, false)
	if !errors.Is(err, firstFailure) || !errors.Is(err, secondFailure) {
		t.Fatalf("cleanup did not aggregate both stage failures: %v", err)
	}
	for _, stage := range []*diskExportStage{first, second} {
		if stage.parent == nil || !stage.committed || !stage.committedRemoved {
			t.Fatalf("sync failure discarded retry state: %+v", stage)
		}
		stage.syncParent = func(file *os.File) error { return file.Sync() }
	}
	if err := cleanupDiskExports([]*diskExportStage{first, second}, false); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []*diskExportStage{first, second} {
		if stage.parent != nil || stage.committed || stage.committedRemoved {
			t.Fatalf("cleanup retry retained stale state: %+v", stage)
		}
	}
}

func TestDiskExportCleanupCloseFailureDoesNotBlockFailedRun(t *testing.T) {
	workspace := t.TempDir()
	transaction, err := beginDiskAttemptTransaction(workspace)
	if err != nil {
		t.Fatal(err)
	}
	outputDirectory := filepath.Join(workspace, localDiskOutputDirectory)
	if err := os.Mkdir(outputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(outputDirectory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outputDirectory, "node-image.qcow2")
	if err := os.WriteFile(path, []byte("synthetic-qcow2"), 0o444); err != nil {
		t.Fatal(err)
	}
	closeFailure := errors.New("output directory close failed")
	closeCalled := false
	stage := &diskExportStage{parent: parent, target: filepath.Base(path), path: path, committed: true,
		syncParent: func(file *os.File) error { return file.Sync() },
		closeParent: func(file *os.File) error {
			closeCalled = true
			_ = file.Close()
			return closeFailure
		}}
	if err := transaction.rollback(false, []*diskExportStage{stage}); err != nil {
		t.Fatalf("post-sync close failure blocked rollback: %v", err)
	}
	if !closeCalled || stage.parent != nil {
		t.Fatalf("output directory descriptor was not finalized: called=%t stage=%+v", closeCalled, stage)
	}
	cause := nvkrerr.New(nvkrerr.CodeVerifyFailed, "synthetic disk commit failure")
	record := filepath.Join(workspace, "lifecycle.json")
	failed, err := recordRunFailure(t.Context(), record, testRun(), cause,
		"recording failed run after local disk commit rollback")
	if !errors.Is(err, cause) || failed == nil || failed.Status != RunFailed {
		t.Fatalf("failed Run = %+v, error=%v", failed, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back disk remained visible: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, diskAttemptMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction marker remained after rollback: %v", err)
	}
	raw, readErr := os.ReadFile(record)
	if readErr != nil || !bytes.Contains(raw, []byte(`"status":"failed"`)) {
		t.Fatalf("durable failed Run = %q, error=%v", raw, readErr)
	}
}

func TestDiskAttemptTransactionMarksIncompleteAndRollsBackLinkedRecord(t *testing.T) {
	workspace := t.TempDir()
	transaction, err := beginDiskAttemptTransaction(workspace)
	if err != nil {
		t.Fatal(err)
	}
	assertCode(t, describeExistingAttempt(workspace, "attempt"), nvkrerr.CodeImmutableViolation)
	record := filepath.Join(workspace, "lifecycle.json")
	linked, err := writeRunRecordState(t.Context(), record, testRun())
	if err != nil || !linked {
		t.Fatal(err)
	}
	if err := describeExistingAttempt(workspace, "attempt"); err == nil ||
		!strings.Contains(err.Error(), "immutable lifecycle record") ||
		!strings.Contains(err.Error(), "incomplete local disk commit cleanup") {
		t.Fatalf("committed attempt with retained marker classification = %v", err)
	}
	if err := transaction.rollback(linked, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{record, filepath.Join(workspace, diskAttemptMarker)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback retained %s: %v", path, err)
		}
	}
	err = describeExistingAttempt(workspace, "attempt")
	if err == nil || !strings.Contains(err.Error(), "interrupted before its lifecycle record") {
		t.Fatalf("interrupted attempt classification = %v", err)
	}
}

func TestExecuteLocalPreflightsDiskBeforeAttemptReservation(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, _ := diskLifecycleFixture(t, "preflight-rejected")
	sentinel := nvkrerr.New(nvkrerr.CodeEngineCapability, "synthetic VM host rejection")
	runFactoryCalled := false
	beginCalled := false
	run, err := executeLocalWithDependencies(t.Context(), prepared,
		LocalOptions{AttemptsRoot: ".nvkr/runs"}, localExecutionDependencies{
			admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error {
				return sentinel
			},
			runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
				runFactoryCalled = true
				return nil, nil
			},
			writeRecordState: writeRunRecordState,
			beginDiskCommit: func(string) (*diskAttemptTransaction, error) {
				beginCalled = true
				return nil, nil
			},
		})
	if run != nil || !errors.Is(err, sentinel) || runFactoryCalled || beginCalled {
		t.Fatalf("preflight rejection: run=%+v error=%v runFactory=%t begin=%t",
			run, err, runFactoryCalled, beginCalled)
	}
	if _, statErr := os.Stat(".nvkr"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("disk preflight reserved attempt storage: %v", statErr)
	}
}

func TestExecuteLocalRetainsCommittedDiskWhenMarkerCleanupFails(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, exporter := diskLifecycleFixture(t, "marker-cleanup-failed")
	sentinel := errors.New("marker unlink failed")
	run, err := executeLocalWithDependencies(t.Context(), prepared,
		LocalOptions{AttemptsRoot: ".nvkr/runs"}, localExecutionDependencies{
			admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
			runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
				return exporter, nil
			},
			writeRecordState: writeRunRecordState,
			beginDiskCommit: func(workspace string) (*diskAttemptTransaction, error) {
				transaction, err := beginDiskAttemptTransaction(workspace)
				if err != nil {
					return nil, err
				}
				original := transaction.unlink
				transaction.unlink = func(directory int, name string, flags int) error {
					if name == diskAttemptMarker {
						return sentinel
					}
					return original(directory, name, flags)
				}
				return transaction, nil
			},
		})
	if run == nil || run.Status != RunComplete || !errors.Is(err, sentinel) {
		t.Fatalf("completed Run after marker cleanup failure = %+v, error=%v", run, err)
	}
	workspace := filepath.Join(".nvkr", "runs", "marker-cleanup-failed")
	record, readErr := os.ReadFile(filepath.Join(workspace, "lifecycle.json"))
	if readErr != nil || !bytes.Contains(record, []byte(`"status":"complete"`)) {
		t.Fatalf("committed lifecycle record = %q, error=%v", record, readErr)
	}
	path, pathErr := LocalDiskOutputPath(workspace, run.Observed[0].Name)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if payload, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(payload, exporter.payload) {
		t.Fatalf("committed disk = %q, error=%v", payload, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, diskAttemptMarker)); statErr != nil {
		t.Fatalf("cleanup marker was not retained: %v", statErr)
	}
	classification := describeExistingAttempt(workspace, "marker-cleanup-failed")
	if classification == nil || !strings.Contains(classification.Error(), "immutable lifecycle record") ||
		!strings.Contains(classification.Error(), "incomplete local disk commit cleanup") {
		t.Fatalf("completed-but-uncleaned attempt classification = %v", classification)
	}
}

func TestDiskAttemptTransactionFaultsRemainRollbackable(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*diskAttemptTransaction, error)
	}{
		{name: "marker unlink", inject: func(transaction *diskAttemptTransaction, sentinel error) {
			original := transaction.unlink
			transaction.unlink = func(directory int, name string, flags int) error {
				if name == diskAttemptMarker {
					transaction.unlink = original
					return sentinel
				}
				return original(directory, name, flags)
			}
		}},
		{name: "marker directory sync", inject: func(transaction *diskAttemptTransaction, sentinel error) {
			original := transaction.syncRoot
			transaction.syncRoot = func(*os.File) error {
				transaction.syncRoot = original
				return sentinel
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			transaction, err := beginDiskAttemptTransaction(workspace)
			if err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(workspace, "lifecycle.json")
			if linked, err := writeRunRecordState(t.Context(), record, testRun()); err != nil || !linked {
				t.Fatalf("link record: linked=%t error=%v", linked, err)
			}
			sentinel := errors.New(test.name + " failed")
			test.inject(transaction, sentinel)
			if err := transaction.complete(); !errors.Is(err, sentinel) {
				t.Fatalf("complete error = %v", err)
			}
			if err := transaction.rollback(true, nil); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{record, filepath.Join(workspace, diskAttemptMarker)} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rollback retained %s: %v", path, err)
				}
			}
		})
	}
}

func TestDiskAttemptTransactionIgnoresCloseFailureAfterCommitPoint(t *testing.T) {
	workspace := t.TempDir()
	transaction, err := beginDiskAttemptTransaction(workspace)
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(workspace, "lifecycle.json")
	if linked, err := writeRunRecordState(t.Context(), record, testRun()); err != nil || !linked {
		t.Fatalf("link record: linked=%t error=%v", linked, err)
	}
	transaction.closeRoot = func(file *os.File) error {
		_ = file.Close()
		return errors.New("close failed after commit")
	}
	if err := transaction.complete(); err != nil {
		t.Fatalf("post-commit close changed successful outcome: %v", err)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("committed record missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, diskAttemptMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit marker remained visible: %v", err)
	}
}

func TestExecuteLocalRecordsFailureAfterLinkedRecordSyncFault(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, exporter := diskLifecycleFixture(t, "record-sync-fault")
	sentinel := errors.New("record directory sync failed")
	closeSentinel := errors.New("rollback root close failed")
	closeCalled := false
	run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{AttemptsRoot: ".nvkr/runs"},
		localExecutionDependencies{
			admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
			runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
				return exporter, nil
			},
			writeRecordState: func(ctx context.Context, path string, run *Run) (bool, error) {
				return writeRunRecordStateWithSync(ctx, path, run, func(*os.File) error { return sentinel })
			},
			beginDiskCommit: func(workspace string) (*diskAttemptTransaction, error) {
				transaction, err := beginDiskAttemptTransaction(workspace)
				if err != nil {
					return nil, err
				}
				transaction.closeRoot = func(file *os.File) error {
					closeCalled = true
					_ = file.Close()
					return closeSentinel
				}
				return transaction, nil
			},
		})
	if !errors.Is(err, sentinel) || errors.Is(err, closeSentinel) || !closeCalled ||
		run == nil || run.Status != RunFailed {
		t.Fatalf("failed Run = %+v, error=%v", run, err)
	}
	workspace := filepath.Join(".nvkr", "runs", "record-sync-fault")
	if _, err := os.Stat(filepath.Join(workspace, diskAttemptMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction marker remained after clean rollback: %v", err)
	}
	path, pathErr := LocalDiskOutputPath(workspace, prepared.run.Expected[0].Name)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disk remained after clean rollback: %v", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(workspace, "lifecycle.json"))
	if readErr != nil || !bytes.Contains(raw, []byte(`"status":"failed"`)) {
		t.Fatalf("durable failed Run = %q, error=%v", raw, readErr)
	}
}

func TestDiskLifecycleUsesRunLocalAndRetainsDiskAfterResultClose(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, exporter := diskLifecycleFixture(t, "disk-attempt")
	called := false
	run, err := executeLocalWithDependencies(t.Context(), prepared,
		LocalOptions{AttemptsRoot: ".nvkr/runs"}, localExecutionDependencies{
			admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
			runFactory: func(_ context.Context, _ *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
				called = true
				if options.Workspace != ".nvkr/runs/disk-attempt" {
					t.Fatalf("factory workspace = %q", options.Workspace)
				}
				return exporter, nil
			},
			writeRecordState: writeRunRecordState,
			beginDiskCommit:  beginDiskAttemptTransaction,
		})
	if err != nil || !called || !exporter.closed || exporter.exports != 1 || run == nil || run.Status != RunComplete {
		t.Fatalf("local disk lifecycle: called=%t closed=%t exports=%d run=%+v error=%v",
			called, exporter.closed, exporter.exports, run, err)
	}
	path, err := LocalDiskOutputPath(".nvkr/runs/disk-attempt", run.Observed[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(raw, exporter.payload) {
		t.Fatalf("durable disk after local result close = %q, %v", raw, readErr)
	}
	if _, err := os.Stat(".nvkr/runs/disk-attempt/lifecycle.json"); err != nil {
		t.Fatalf("immutable successful attempt record: %v", err)
	}
}

func TestLocalNonDiskRunDoesNotStartDiskCommitTransaction(t *testing.T) {
	t.Chdir(t.TempDir())
	definition := testDefinition(t)
	environment := testEnvironment()
	environment.Publication = nil
	plan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	runDescription, err := PrepareRun(definition, environment, plan, "factory")
	if err != nil {
		t.Fatal(err)
	}
	prepared := &PreparedRun{plan: plan, run: runDescription}
	exporter := &fakeDiskExporter{}
	beginCalled := false
	run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{AttemptsRoot: ".nvkr"},
		localExecutionDependencies{
			admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
			runFactory: func(ctx context.Context, plan *factory.Plan, _ factory.LocalRunOptions) (localExecutionResult, error) {
				payload := []byte("synthetic kubectl")
				input := planArtifact(t, plan, "artifact/cli/kubectl")
				if err := os.MkdirAll(filepath.Dir(input.Path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(input.Path, payload, 0o755); err != nil {
					t.Fatal(err)
				}
				result := planArtifact(t, plan, "result/cli/kubectl")
				writeCanonicalTestFile(t, result.Path, fixtureProducerResult(t, plan, input, input.Path))
				if err := factory.ExecuteNode(ctx, plan, "package/cli", nil, nil); err != nil {
					t.Fatal(err)
				}
				return exporter, nil
			},
			writeRecordState: writeRunRecordState,
			beginDiskCommit: func(string) (*diskAttemptTransaction, error) {
				beginCalled = true
				return nil, errors.New("ordinary output started a disk transaction")
			},
		})
	if err != nil || beginCalled || run == nil || run.Status != RunComplete {
		t.Fatalf("ordinary local Run = %+v, beginCalled=%t, error=%v", run, beginCalled, err)
	}
	if _, err := os.Stat(".nvkr/factory/lifecycle.json"); err != nil {
		t.Fatalf("ordinary immutable Run record missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(".nvkr/factory", diskAttemptMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary Run created a disk transaction marker: %v", err)
	}
}

func planArtifact(t *testing.T, plan *factory.Plan, id string) factory.Artifact {
	t.Helper()
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == id {
			return artifact
		}
	}
	t.Fatalf("plan omits artifact %q", id)
	return factory.Artifact{}
}

func diskLifecycleFixture(t *testing.T, attempt string) (*PreparedRun, *fakeDiskExporter) {
	return diskLifecycleFixtureWithProducer(t, attempt, false)
}

func diskLifecycleFixtureWithProducer(t *testing.T, attempt string, packer bool) (*PreparedRun, *fakeDiskExporter) {
	t.Helper()
	definition := diskDefinition(t)
	definition.Catalog.Base = "catalog"
	definition.Outputs[0].Components = []string{"nvke-node-non-fde"}
	environment := testEnvironment()
	environment.Publication = nil
	environment.ExecutionClasses = []execution.Class{{
		Name: "amd64-kvm", RunsOn: "linux-amd64-cpu16", HostPlatform: execution.HostPlatformLinuxAMD64,
		Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536},
		Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, MaxTimeoutSeconds: 10800,
	}}
	component := catalog.Component{
		Platforms: []string{"linux/amd64"}, FromSource: true,
		BuilderType: catalog.BuilderVMImage, OutputType: OutputDiskImage,
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		BuildRequirements: &execution.Requirements{
			Resources:            &execution.ResourceRequest{CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768},
			AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			Capabilities:         []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}},
			TimeoutSeconds:       7200,
		},
		Repo: "https://example.test/nvke", Tag: "v1.0.0", Commit: strings.Repeat("a", 40),
		TreeHash: strings.Repeat("b", 40), ToolchainImages: map[string]string{
			"amd64": "example.test/vm@sha256:" + strings.Repeat("c", 64),
		},
		BuildCommand: []string{"make", "image", "ARCH={{.Arch}}"}, ArtifactPath: "out/node-amd64.qcow2", SourceDate: 1,
	}
	if packer {
		component.BuildCommand = nil
		component.Packer = &catalog.PackerConfig{
			ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
			ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
			MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
		}
	}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{
		"nvke-node-non-fde": component,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	runDescription, err := PrepareRun(definition, environment, plan, attempt)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &PreparedRun{plan: plan, run: runDescription}
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("synthetic-qcow2")
	exporter := &fakeDiskExporter{payload: payload, observation: factory.DiskPayloadObservation{
		Digest: pipeline.FileDigest(payload), Size: int64(len(payload)),
		BuildDigest: plan.LifecycleOutputs[0].BuildDigest,
		Components:  append([]string(nil), plan.LifecycleOutputs[0].Components...),
		Producer:    "build-nvke-node-non-fde", Component: "nvke-node-non-fde",
		Format: "qcow2", Encryption: "none", Outcome: "succeeded", PlanDigest: planDigest,
	}}
	return prepared, exporter
}
