// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const (
	runtimeFixtureEnvironment = "environment-runtime.yaml"
	runtimeFixturePlan        = ".github/nvkr-run-runtime/plan.json"
	runtimeFixtureWorkflow    = ".github/workflows/nvkr-run-runtime.yml"
)

// runtimeFixture adds a named runtime-Plan bundle (githubRuntimePlan) that shares
// the fixture catalog. Its committed Plan is generated at the current pins; the
// caller then moves the shared lock and regenerates production only, so the
// runtime bundle's Plan lags the pins exactly as its owner's contract allows.
func runtimeFixture(t *testing.T) Config {
	t.Helper()
	cfg, promote := maintenanceFixture(t)
	raw := append(readFixture(t, cfg.Environment), "githubBundle: runtime\ngithubRuntimePlan: true\n"...)
	writeFixture(t, runtimeFixtureEnvironment, raw)
	cfg.AdditionalRuns = []RunInput{{Definition: cfg.Definition, Environment: runtimeFixtureEnvironment}}
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, runtimeFixtureEnvironment, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
	promote("1.1.0")
	if _, err := catalog.UpdateSourcePins(t.Context(), catalog.SourcePinRequest{Directory: cfg.Catalog, Sources: cfg.Sources, SameRelease: cfg.SameRelease, Bump: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A runtime-Plan bundle compiles current pins at dispatch, so a committed Plan
// behind the lock is its valid baseline. The closure checks it through the runtime
// owner, which still refuses an edited workflow byte; an ordinary byte-exact check
// would call the valid baseline drift.
func TestClosureChecksRuntimePlanMembersThroughTheirOwner(t *testing.T) {
	cfg := runtimeFixture(t)
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, runtimeFixtureEnvironment, "test (deadbeef)", "github", ".", true); err == nil {
		t.Fatal("the fixture's runtime Plan does not lag the pins, so it cannot tell the two owners apart")
	}
	beforePlan, beforeWorkflow := readFixture(t, runtimeFixturePlan), readFixture(t, runtimeFixtureWorkflow)
	result, err := Refresh(t.Context(), cfg, "test (deadbeef)", true, "")
	if err != nil || result.Changed {
		t.Fatalf("a valid runtime baseline was refused: %+v %v", result, err)
	}
	if !bytes.Equal(beforePlan, readFixture(t, runtimeFixturePlan)) || !bytes.Equal(beforeWorkflow, readFixture(t, runtimeFixtureWorkflow)) {
		t.Fatal("check mode wrote the runtime bundle")
	}

	t.Run("edited workflow", func(t *testing.T) {
		writeFixture(t, runtimeFixtureWorkflow, append(beforeWorkflow, "# edited\n"...))
		t.Cleanup(func() { writeFixture(t, runtimeFixtureWorkflow, beforeWorkflow) })
		calls := 0
		_, err := refresh(t.Context(), cfg, "test (deadbeef)", true, "", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
			calls++
			return catalog.SourcePinUpdate{}, nil
		})
		assertCode(t, err, e.CodePipelineDrift)
		if calls != 0 {
			t.Fatal("an edited runtime workflow reached pin resolution")
		}
	})
}

// Regeneration goes through the runtime owner too: the refreshed runtime Plan
// follows the pins while its workflow keeps every byte, and the result rechecks.
func TestRefreshRegeneratesRuntimePlanMembersThroughTheirOwner(t *testing.T) {
	cfg := runtimeFixture(t)
	beforePlan, beforeWorkflow := readFixture(t, runtimeFixturePlan), readFixture(t, runtimeFixtureWorkflow)
	if _, err := regenerate(t.Context(), cfg, "test (deadbeef)", lifecycle.GenerateFiles); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(beforePlan, readFixture(t, runtimeFixturePlan)) {
		t.Fatal("the runtime Plan did not follow the pins")
	}
	if !bytes.Equal(beforeWorkflow, readFixture(t, runtimeFixtureWorkflow)) {
		t.Fatal("runtime regeneration changed the reviewed workflow")
	}
	if _, err := lifecycle.GenerateRuntimeFiles(t.Context(), cfg.Definition, runtimeFixtureEnvironment, "test (deadbeef)", "github", ".", true); err != nil {
		t.Fatalf("the regenerated runtime bundle does not recheck: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(runtimeFixturePlan)); err != nil {
		t.Fatal(err)
	}
}
