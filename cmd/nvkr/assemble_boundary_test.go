// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func TestAssembleUnavailableLanesFailBeforeIO(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("scripts", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("scripts/build-qcow2.sh", []byte("#!/bin/sh\ntouch disk-producer-ran\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "encryption before input",
			args: []string{"--format", "qcow2", "--encrypt", "--from", "missing.raw",
				"--from-raw", "also-missing.raw", "--manifest-out", "must-not-exist/manifest.json"},
		},
		{name: "raw producer", args: []string{"--format", "raw"}},
		{name: "iso producer", args: []string{"--format", "iso"}},
		{name: "qcow2 producer", args: []string{"--format", "qcow2"}},
		{name: "boot observer", args: []string{"--format", "qcow2", "--boot"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := &cli.App{Commands: []*cli.Command{cmdAssemble()}}
			args := append([]string{"nvkr", "assemble"}, test.args...)
			err := app.Run(args)
			if cliErrorCode(err) != nvkrerr.CodeEngineCapability {
				t.Fatalf("code = %q, want %q: %v", cliErrorCode(err), nvkrerr.CodeEngineCapability, err)
			}
			for _, path := range []string{
				"must-not-exist", "disk-producer-ran", ".nvkr",
			} {
				if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
					t.Fatalf("unsupported lane created %s before failing: %v", path, statErr)
				}
			}
		})
	}
}

func TestAssembleFromRawManifestRemainsSupported(t *testing.T) {
	root := t.TempDir()
	raw := filepath.Join(root, "node.raw")
	if err := os.WriteFile(raw, make([]byte, assemble.MinDiskBlockSizeBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "disk-manifest.json")
	app := &cli.App{Commands: []*cli.Command{cmdAssemble()}}
	err := app.Run([]string{
		"nvkr", "assemble", "--format", "raw", "--from-raw", raw, "--manifest-out", out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(out); statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("typed DiskManifest was not emitted: info=%v err=%v", info, statErr)
	}
}
