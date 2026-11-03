// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// TestMain lets this test binary serve the hidden adapter command that
// --projection-pilot launches from os.Executable with an empty environment.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == projectionAdapterCommand {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The same reference plugin, selected in-process and out-of-process, writes
// and checks exactly the in-binary bundle.
func TestRunGenerateProjectionPilotMatchesInBinaryGeneration(t *testing.T) {
	lifecycleCommandRepo(t)
	previous, selected := selectProjectionPilot, []string{}
	selectProjectionPilot = func(ctx context.Context, transport string) (context.Context, error) {
		selected = append(selected, transport)
		return previous(ctx, transport)
	}
	t.Cleanup(func() { selectProjectionPilot = previous })
	generate := []string{"run", "generate", "--definition", "docs/examples/nvke-tutorial/definition.yaml",
		"--environment", "docs/examples/nvke-tutorial/environment-github.yaml"}
	inBinary := t.TempDir()
	if _, err := invokeLifecycleCLI(t, append(generate, "--root", inBinary)...); err != nil {
		t.Fatal(err)
	}
	// The generated bootstrap argv, with no pilot flag, never selects a plugin.
	if output, err := invokeLifecycleCLI(t, "run", "generate", "--engine", "github", "--definition", "docs/examples/nvke-tutorial/definition.yaml",
		"--environment", "docs/examples/nvke-tutorial/environment-github.yaml", "--root", inBinary, "--check"); err != nil || !strings.Contains(output, "generated run is current") {
		t.Fatalf("bootstrap check: %s %v", output, err)
	}
	if len(selected) != 0 {
		t.Fatalf("unflagged generation selected projection transports %v", selected)
	}
	want := generatedTree(t, inBinary)
	for _, transport := range []string{"in-process", "out-of-process"} {
		t.Run(transport, func(t *testing.T) {
			pilot, selector := t.TempDir(), "--projection-pilot="+transport
			selected = selected[:0]
			if _, err := invokeLifecycleCLI(t, append(generate, "--root", pilot, selector)...); err != nil {
				t.Fatal(err)
			}
			if len(selected) != 1 || selected[0] != transport {
				t.Fatalf("pilot generation selected %v, want [%s]", selected, transport)
			}
			if got := generatedTree(t, pilot); !reflect.DeepEqual(got, want) {
				t.Fatal("pilot generation differs from in-binary generation")
			}
			for _, check := range [][]string{
				append(generate, "--root", inBinary, "--check", selector),
				append(generate, "--root", pilot, "--check"),
			} {
				if output, err := invokeLifecycleCLI(t, check...); err != nil || !strings.Contains(output, "generated run is current") {
					t.Fatalf("%v: %s %v", check, output, err)
				}
			}
			// Runtime refresh refuses the selection itself, before reading inputs.
			_, err := invokeLifecycleCLI(t, append(generate, "--root", pilot, "--runtime", "--check", selector)...)
			assertLifecycleCommandCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), "selected projection adapter") {
				t.Fatalf("runtime refusal is not the projection refusal: %v", err)
			}
			if err := os.WriteFile(filepath.Join(pilot, ".github", "workflows", "nvkr-run.yml"), []byte("drifted\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = invokeLifecycleCLI(t, append(generate, "--root", pilot, "--check", selector)...)
			assertLifecycleCommandCode(t, err, e.CodePipelineDrift)
		})
	}
	_, err := invokeLifecycleCLI(t, append(generate, "--root", t.TempDir(), "--projection-pilot=grpc")...)
	assertLifecycleCommandCode(t, err, e.CodeUsage)
}

func generatedTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		relative, _ := filepath.Rel(root, path)
		tree[relative] = string(raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return tree
}
