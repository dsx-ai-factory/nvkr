// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalCapturedInputsInitializeBeforeRunners(t *testing.T) {
	for _, mode := range []string{"new attempt", "unowned directory", "changed Plan", "invalid snapshot", "dry run"} {
		t.Run(mode, func(t *testing.T) {
			_, plan, snapshot := captureSnapshotFixture(t)
			t.Chdir(t.TempDir())
			const workspace = ".nvkr/captured"
			raw, err := canonical.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			options := withLocalDefaults(LocalRunOptions{Workspace: workspace, CompileInputs: string(raw), DryRun: mode == "dry run"})
			switch mode {
			case "unowned directory":
				mustWriteWorkspaceFixture(t, filepath.Join(workspace, "canary"), []byte("preserve"))
			case "changed Plan":
				for i := range snapshot.Files {
					if snapshot.Files[i].Path == snapshotDefinition {
						snapshot.Files[i].Data = bytes.ReplaceAll(snapshot.Files[i].Data, []byte("snapshot-fixture"), []byte("different-fixture"))
					}
				}
				changed, err := canonical.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				options.CompileInputs = string(changed)
			case "invalid snapshot":
				options.CompileInputs = "{}"
			}
			called := false
			stop := e.New(e.CodeBuildFailed, "stop before producer execution")
			result, err := runLocal(t.Context(), plan, options, localRunDeps{
				executeNode: func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error {
					t.Fatal("producer must not execute in initialization test")
					return nil
				},
				prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
					called = true
					got, readErr := os.ReadFile(filepath.Join(workspace, "compile-inputs.json"))
					if readErr != nil || string(got) != string(raw) {
						t.Fatalf("runner lacks original inputs: %v", readErr)
					}
					if _, err := os.Stat(filepath.Join(workspace, localWorkspaceMarker)); err != nil {
						t.Fatal(err)
					}
					assertFactoryErrorCode(t, retainLocalCompileInputs(t.Context(), workspace, "replacement"), e.CodeImmutableViolation)
					got, readErr = os.ReadFile(filepath.Join(workspace, "compile-inputs.json"))
					if readErr != nil || string(got) != string(raw) {
						t.Fatal("attempt inputs were overwritten")
					}
					return nil, stop
				},
			})
			switch mode {
			case "new attempt":
				if !called || !errors.Is(err, stop) {
					t.Fatalf("runner called=%t error=%v", called, err)
				}
			case "dry run":
				if called || err != nil || result == nil {
					t.Fatalf("dry-run called=%t error=%v", called, err)
				}
				if err := result.Close(); err != nil {
					t.Fatal(err)
				}
			default:
				if called || err == nil {
					t.Fatalf("invalid inputs reached runner: %t %v", called, err)
				}
				if mode == "unowned directory" {
					assertFactoryErrorCode(t, err, e.CodeImmutableViolation)
				}
				if mode == "changed Plan" {
					assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				}
				if mode == "invalid snapshot" {
					assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				}
			}
			if mode != "new attempt" {
				if _, err := os.Stat(filepath.Join(workspace, "compile-inputs.json")); !os.IsNotExist(err) {
					t.Fatalf("unexpected snapshot: %v", err)
				}
			}
		})
	}
}
