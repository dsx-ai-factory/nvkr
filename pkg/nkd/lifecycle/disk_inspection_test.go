package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestLocalInspectionRunsBeforeHeldDiskExportAndClose(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, exporter := diskLifecycleFixture(t, "inspected-attempt")
	inspector := &sbom.DiskInspectionRuntime{}
	called := false
	run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{
		AttemptsRoot: ".nkd/runs", DiskInspection: inspector,
	}, localExecutionDependencies{
		admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
		runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
			return exporter, nil
		},
		writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
		admitInspection: func(_ context.Context, plan *factory.Plan, runtime *sbom.DiskInspectionRuntime) error {
			if plan != prepared.plan || runtime != inspector {
				t.Fatal("admission received substituted Plan/runtime")
			}
			return nil
		},
		inspectDisks: func(_ context.Context, plan *factory.Plan, result localExecutionResult,
			runtime *sbom.DiskInspectionRuntime,
		) ([]factory.DiskInstalledInventoryDescription, error) {
			called = true
			if plan != prepared.plan || result != exporter || runtime != inspector || exporter.closed || exporter.exports != 0 {
				t.Fatal("inspection did not run with the original open execution before export")
			}
			return []factory.DiskInstalledInventoryDescription{{OutputName: prepared.run.Expected[0].Name,
				Subject: exporter.observation, Packages: []sbom.InstalledDiskPackage{{Name: "fixture", Version: "1", Format: "deb"}}}}, nil
		},
	})
	if err != nil || !called || run == nil || run.Status != RunComplete || !exporter.closed || exporter.exports != 1 {
		t.Fatalf("live ordering: run=%+v err=%v called=%t closed=%t exports=%d", run, err, called, exporter.closed, exporter.exports)
	}
	views := run.InstalledDiskInventories()
	if len(views) != 1 || views[0].Subject.Digest != exporter.observation.Digest {
		t.Fatalf("missing historical view: %+v", views)
	}
	views[0].Packages[0].Name = "changed"
	views[0].Subject.Components[0] = "changed"
	if fresh := run.InstalledDiskInventories(); fresh[0].Packages[0].Name == "changed" || fresh[0].Subject.Components[0] == "changed" {
		t.Fatal("caller changed diagnostic inventory")
	}
	raw, err := canonical.Marshal(run)
	if err != nil || strings.Contains(string(raw), "fixture") || strings.Contains(string(raw), "diskInventories") {
		t.Fatalf("private inspection escaped Run description: %s, %v", raw, err)
	}
	var decoded Run
	if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.InstalledDiskInventories()) != 0 {
		t.Fatal("decoded Run restored a live inventory")
	}
}

func TestLocalInspectionFailureClosesRunWithoutExport(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, exporter := diskLifecycleFixture(t, "failed-inspection")
	cause := e.New(e.CodeVerifyFailed, "inspection rejected fixture")
	run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{
		AttemptsRoot: ".nkd/runs", DiskInspection: &sbom.DiskInspectionRuntime{},
	}, localExecutionDependencies{
		admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
		runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
			return exporter, nil
		},
		writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
		admitInspection: func(context.Context, *factory.Plan, *sbom.DiskInspectionRuntime) error { return nil },
		inspectDisks: func(context.Context, *factory.Plan, localExecutionResult, *sbom.DiskInspectionRuntime) ([]factory.DiskInstalledInventoryDescription, error) {
			return nil, cause
		},
	})
	if run == nil || run.Status != RunFailed || !errors.Is(err, cause) || !exporter.closed || exporter.exports != 0 {
		t.Fatalf("failed inspection: run=%v err=%v closed=%t exports=%d", run, err, exporter.closed, exporter.exports)
	}
	path, err := LocalDiskOutputPath(filepath.Join(".nkd/runs", prepared.run.Attempt), prepared.run.Expected[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed scan retained a delivery file: %v", err)
	}
}

func TestLocalInspectionRejectsMissingAdmissionBeforeAttemptStorage(t *testing.T) {
	for _, mode := range []string{"unadmitted runtime", "no disk output"} {
		t.Run(mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var prepared *PreparedRun
			if mode == "no disk output" {
				definition, environment := testDefinition(t), testEnvironment()
				plan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test")
				if err != nil {
					t.Fatal(err)
				}
				run, err := PrepareRun(definition, environment, plan, "rejected-inspector")
				if err != nil {
					t.Fatal(err)
				}
				prepared = &PreparedRun{plan: plan, run: run}
			} else {
				prepared, _ = diskLifecycleFixture(t, "rejected-inspector")
			}
			called := false
			run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{
				AttemptsRoot: ".nkd/runs", DiskInspection: &sbom.DiskInspectionRuntime{},
			}, localExecutionDependencies{
				admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
				runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
					called = true
					return nil, nil
				},
				writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
			})
			assertCode(t, err, e.CodeEngineCapability)
			if called || run != nil {
				t.Fatal("unadmitted inspection started a build or returned a Run")
			}
			if mode == "no disk output" && !strings.Contains(err.Error(), "without a disk output") {
				t.Fatalf("non-disk Plan did not reach output admission: %v", err)
			}
			if _, err := os.Stat(".nkd"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unadmitted inspector created attempt storage: %v", err)
			}
		})
	}
}

func TestLocalInspectionDryRunDoesNotAdmitRuntimeOrIssueInventory(t *testing.T) {
	for _, tt := range []struct {
		name    string
		runtime *sbom.DiskInspectionRuntime
	}{
		{name: "omitted"},
		{name: "supplied", runtime: &sbom.DiskInspectionRuntime{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			prepared, _ := diskLifecycleFixture(t, "dry-inspection")
			original := prepared.Description()
			listed := false
			run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{
				AttemptsRoot: ".nkd/runs", DryRun: true, DiskInspection: tt.runtime,
			}, localExecutionDependencies{
				admitLocal: factory.AdmitLocal,
				runFactory: func(_ context.Context, _ *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
					if !options.DryRun {
						t.Fatal("dry-run invoked an executing factory")
					}
					listed = true
					return nil, nil
				},
				writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
				admitInspection: func(context.Context, *factory.Plan, *sbom.DiskInspectionRuntime) error {
					t.Fatal("dry-run admitted an inspection runtime")
					return nil
				},
				inspectDisks: func(context.Context, *factory.Plan, localExecutionResult, *sbom.DiskInspectionRuntime) ([]factory.DiskInstalledInventoryDescription, error) {
					t.Fatal("dry-run inspected a disk")
					return nil, nil
				},
			})
			if err != nil || !listed || run == nil {
				t.Fatalf("dry-run listing: run=%v err=%v listed=%t", run, err, listed)
			}
			if run.Status != original.Status || run.Lane != LaneUnqualified ||
				run.PlanDigest != original.PlanDigest || len(run.Observed) != 0 || len(run.InstalledDiskInventories()) != 0 {
				t.Fatalf("dry-run issued execution or inspection evidence: %+v", run)
			}
			if _, err := os.Stat(".nkd"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dry-run created attempt storage: %v", err)
			}
		})
	}
}
