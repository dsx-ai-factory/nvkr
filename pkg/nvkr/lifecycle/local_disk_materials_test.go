// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestLocalDiskMaterialsBindEveryObservedOutput(t *testing.T) {
	for _, mode := range []string{"exact", "wrong output", "wrong plan", "missing components", "observer failure", "canceled", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			prepared, _ := diskLifecycleFixture(t, "material-attempt")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			raw, err := collectLocalDiskMaterials(ctx, prepared.plan, prepared.run.Attempt,
				func(_ context.Context, _ *factory.Plan, slot factory.LifecycleOutputPlan) (factory.DiskMaterialRecord, error) {
					calls++
					value := factory.DiskMaterialRecord{Output: slot.Name, PlanDigest: prepared.run.PlanDigest,
						BuildDigest: slot.BuildDigest, Components: append([]string(nil), slot.Components...)}
					switch mode {
					case "wrong output":
						value.Output = "sibling"
					case "wrong plan":
						value.PlanDigest = "sha256:" + repeat("9", 64)
					case "missing components":
						value.Components = nil
					case "observer failure":
						return value, e.New(e.CodeProvenanceMissing, "no live consumed-material trace")
					case "canceled":
						cancel()
					case "oversized":
						value.Coverage = strings.Repeat("x", 4<<20)
					}
					return value, nil
				})
			if calls != 1 {
				t.Fatalf("material observation calls = %d", calls)
			}
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				var record localDiskMaterials
				if err := canonical.UnmarshalExact(raw, &record); err != nil {
					t.Fatal(err)
				}
				if record.Attempt != prepared.run.Attempt || record.PlanDigest != prepared.run.PlanDigest || len(record.Disks) != 1 {
					t.Fatalf("material record lost its attempt or exact scope: %+v", record)
				}
				return
			}
			code := e.CodeVerifyFailed
			switch mode {
			case "observer failure", "oversized":
				code = e.CodeProvenanceMissing
			case "canceled":
				code = e.CodeTimeout
			}
			assertCode(t, err, code)
			if len(raw) != 0 {
				t.Fatal("failed collection exposed partial material records")
			}
		})
	}
}

func TestCapturedDiskRunRequiresLiveMaterialOwnerBeforeDiskExport(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, exporter := diskLifecycleFixture(t, "material-attempt")
	// The fixture enables input retention; it does not claim a replayable input
	// closure. The test stops at the concrete factory-owner admission boundary.
	prepared.inputs = "{}"
	run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{AttemptsRoot: ".nvkr/runs"}, localExecutionDependencies{
		admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
		runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
			return exporter, nil
		},
		writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
	})
	assertCode(t, err, e.CodeEngineCapability)
	if run == nil || run.Status != RunFailed || !exporter.closed || exporter.exports != 0 {
		t.Fatalf("missing material owner was not terminal before disk export: run=%+v closed=%t exports=%d", run, exporter.closed, exporter.exports)
	}
	path := filepath.Join(".nvkr/runs/material-attempt", localDiskMaterialsFile)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing live material owner exposed evidence: %v", err)
	}
}
