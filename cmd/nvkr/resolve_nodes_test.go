// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestResolveNodesOutputPreservesIdentityFrame(t *testing.T) {
	queryArgs := []string{
		"--component", "kubectl", "--platform", "linux/amd64",
		"--data", filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"),
	}
	app := &cli.App{Commands: []*cli.Command{cmdResolve()}}
	nodes, err := runCLIWithCapturedStdout(t, app,
		append([]string{"nvkr", "resolve"}, append(queryArgs, "-o", "nodes")...)...)
	if err != nil {
		t.Fatalf("resolve -o nodes failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(nodes, &decoded); err != nil {
		t.Fatalf("nodes output is not JSON: %v\n%s", err, nodes)
	}
	if _, ok := decoded["nodes"].([]any); !ok {
		t.Fatalf("nodes output omitted the full execution graph: %s", nodes)
	}
	if decoded["platform"] != "linux/amd64" {
		t.Fatalf("nodes output lost target platform: %s", nodes)
	}
	for _, legacy := range []string{"criteria", "recipeID", "recipeId"} {
		if _, present := decoded[legacy]; present {
			t.Fatalf("nodes output contains removed %s", legacy)
		}
	}

}

func runCLIWithCapturedStdout(t *testing.T, app *cli.App, args ...string) ([]byte, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = output
	runErr := app.Run(args)
	os.Stdout = original
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return raw, runErr
}
