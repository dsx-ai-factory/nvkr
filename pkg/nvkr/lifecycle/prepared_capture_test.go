// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func capturedLocalFixture(t *testing.T) (string, *PreparedRun) {
	t.Helper()
	root := t.TempDir()
	definition, environment := testDefinition(t), testEnvironment()
	if err := os.CopyFS(filepath.Join(root, "catalog"), os.DirFS(definition.Catalog.Base)); err != nil {
		t.Fatal(err)
	}
	definition.Catalog.Base = "catalog"
	environment.Publication = nil
	writeCanonicalTestFile(t, filepath.Join(root, "definition.json"), definition)
	writeCanonicalTestFile(t, filepath.Join(root, "environment.json"), environment)
	prepared, err := PrepareCapturedFiles(t.Context(), root, "definition.json", "environment.json", "test", "captured")
	if err != nil {
		t.Fatal(err)
	}
	return root, prepared
}

func TestPrepareCapturedFilesReplaysOriginalInputs(t *testing.T) {
	root, prepared := capturedLocalFixture(t)
	// A later workspace edit must not change the captured build inputs.
	if err := os.WriteFile(filepath.Join(root, "catalog", "registry.yaml"), []byte("invalid edited catalog"), 0o600); err != nil {
		t.Fatal(err)
	}
	var snapshot factory.InputSnapshot
	if err := canonical.UnmarshalExact([]byte(prepared.inputs), &snapshot); err != nil {
		t.Fatal(err)
	}
	replayed, err := factory.CompileSnapshot(t.Context(), snapshot, "test")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := replayed.Digest()
	if err != nil || digest != prepared.Description().PlanDigest {
		t.Fatalf("captured inputs did not reproduce original Plan: %s, %v", digest, err)
	}
	if _, err := PrepareCapturedFiles(t.Context(), root, "definition.json", "environment.json", "test", "other"); err == nil {
		t.Fatal("fresh preparation ignored the invalid current catalog")
	}
}

func TestExecuteLocalHandsCapturedInputsToFactory(t *testing.T) {
	for _, mode := range []string{"build failure", "dry run", "admission failure"} {
		t.Run(mode, func(t *testing.T) {
			root, prepared := capturedLocalFixture(t)
			attempts := filepath.Join(root, "attempts")
			path := filepath.Join(attempts, "captured", "compile-inputs.json")
			calls := 0
			cause := e.New(e.CodeBuildFailed, "fixture producer failed")
			_, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{AttemptsRoot: attempts, DryRun: mode == "dry run"}, localExecutionDependencies{
				admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error {
					if mode == "admission failure" {
						return e.New(e.CodeEngineCapability, "fixture capability unavailable")
					}
					return nil
				},
				runFactory: func(_ context.Context, _ *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
					calls++
					if options.CompileInputs != prepared.inputs {
						t.Fatal("factory did not receive the original captured inputs")
					}
					if mode == "dry run" {
						return nil, nil
					}
					if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
						t.Fatalf("lifecycle populated the unowned factory workspace: %v", statErr)
					}
					return nil, cause
				},
				writeRecordState: writeRunRecordState,
				beginDiskCommit:  beginDiskAttemptTransaction,
			})
			switch mode {
			case "build failure":
				if !errors.Is(err, cause) || calls != 1 {
					t.Fatalf("lost producer failure: calls=%d err=%v", calls, err)
				}
			case "dry run":
				if err != nil || calls != 1 {
					t.Fatalf("dry run failed: calls=%d err=%v", calls, err)
				}
			case "admission failure":
				assertCode(t, err, e.CodeEngineCapability)
				if calls != 0 {
					t.Fatal("unavailable capability started producer")
				}
			}
			if mode != "build failure" {
				if _, statErr := os.Stat(attempts); !os.IsNotExist(statErr) {
					t.Fatalf("non-execution created attempt storage: %v", statErr)
				}
			}
		})
	}
}
