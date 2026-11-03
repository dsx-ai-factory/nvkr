// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
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

func TestRunGenerateProjectionPilotMatchesInBinaryGeneration(t *testing.T) {
	lifecycleCommandRepo(t)
	generate := []string{"run", "generate", "--definition", "docs/examples/nvke-tutorial/definition.yaml",
		"--environment", "docs/examples/nvke-tutorial/environment-github.yaml"}
	inBinary, pilot := t.TempDir(), t.TempDir()
	if _, err := invokeLifecycleCLI(t, append(generate, "--root", inBinary)...); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeLifecycleCLI(t, append(generate, "--root", pilot, "--projection-pilot")...); err != nil {
		t.Fatal(err)
	}
	if want, got := generatedTree(t, inBinary), generatedTree(t, pilot); !reflect.DeepEqual(got, want) {
		t.Fatal("pilot generation differs from in-binary generation")
	}
	for _, check := range [][]string{
		append(generate, "--root", inBinary, "--check", "--projection-pilot"),
		append(generate, "--root", pilot, "--check"),
	} {
		if output, err := invokeLifecycleCLI(t, check...); err != nil || !strings.Contains(output, "generated run is current") {
			t.Fatalf("%v: %s %v", check, output, err)
		}
	}
	_, err := invokeLifecycleCLI(t, append(generate, "--root", pilot, "--runtime", "--check", "--projection-pilot")...)
	assertLifecycleCommandCode(t, err, e.CodeEngineCapability)
	if err := os.WriteFile(filepath.Join(pilot, ".github", "workflows", "nvkr-run.yml"), []byte("drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = invokeLifecycleCLI(t, append(generate, "--root", pilot, "--check", "--projection-pilot")...)
	assertLifecycleCommandCode(t, err, e.CodePipelineDrift)
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
