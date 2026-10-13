// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// The fresh process executes main against a real fd 1 with a closed reader.
// Plan inspection and local dry-run perform no producer execution or deployment.
func TestCLIClosedStdoutPreservesCheckedStatus(t *testing.T) {
	if scenario := os.Getenv("NVKR_TEST_CLOSED_STDOUT"); scenario != "" {
		switch scenario {
		case "plan":
			os.Args = []string{"nvkr", "run", "plan", "--plan", ".github/nvkr-run/plan.json", "--json"}
		case "dry-run":
			os.Args = localPresentationArgs("--dry-run", "--ui=plain")
		case "JSON failure":
			os.Args = []string{"nvkr", "run", "local", "--definition", "missing", "--environment", "missing", "--attempt", "fresh", "--json"}
		case "inspection":
			os.Args = []string{"nvkr", "run", "inspect", "--record", os.Getenv("NVKR_TEST_RECORD_PATH"), "--json"}
		case "unchecked definition":
			os.Args = []string{"nvkr", "definition", "validate", "--definition", "definitions/nvke-v1/definition.yaml"}
		default:
			t.Fatal("unknown process fixture")
		}
		main()
		return
	}
	lifecycleCommandRepo(t)
	prepared, err := lifecycle.PrepareFiles(t.Context(), "definitions/nvke-v1/definition.yaml", "definitions/nvke-v1/environment-local.yaml", "test", "process-inspection")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(prepared.Description())
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "lifecycle.json")
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		code   e.Code
		signal bool
	}{
		{"plan", e.CodeInternal, false}, {"dry-run", e.CodeInternal, false}, {"JSON failure", e.CodeInvalidCatalog, false},
		{"inspection", e.CodeInternal, false}, {"unchecked definition", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIClosedStdoutPreservesCheckedStatus$")
			command.Env = append(os.Environ(), "NVKR_TEST_CLOSED_STDOUT="+test.name, "NVKR_TEST_RECORD_PATH="+recordPath)
			command.Stdout = writer
			err = command.Run()
			if test.signal {
				if command.ProcessState == nil {
					t.Fatalf("unchecked reporter has no process status: %v", err)
				}
				status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != syscall.SIGPIPE {
					t.Fatalf("unchecked reporter's SIGPIPE behavior changed: %v, state=%v", err, command.ProcessState)
				}
				return
			}
			want := e.ExitCode(e.New(test.code, ""))
			if command.ProcessState == nil || command.ProcessState.ExitCode() != want {
				t.Fatalf("closed stdout replaced coded result: %v, state=%v, want %d", err, command.ProcessState, want)
			}
		})
	}
}
