// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/attest"
	"github.com/urfave/cli/v2"
)

func TestEvidenceOutputIsExplicitlyUnqualified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	cmd := &cli.App{
		Flags: []cli.Flag{&cli.StringFlag{Name: "evidence-out"}},
		Action: func(c *cli.Context) error {
			return writeQualifiedEvidence(c, &attest.EvidenceBundle{
				SchemaVersion: attest.EvidenceSchemaVersion,
				Verdict:       attest.VerdictReproduced,
			})
		},
	}

	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = oldStdout })

	runErr := cmd.Run([]string{"test", "--evidence-out", path})
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	out, readErr := io.ReadAll(readEnd)
	if closeErr := readEnd.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	message := string(out)
	if !strings.Contains(message, "UNQUALIFIED DIAGNOSTIC") ||
		!strings.Contains(message, "authenticated reproduction receipt") {
		t.Fatalf("evidence output did not state its non-authoritative boundary: %q", message)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"verdict":"reproduced"`) {
		t.Fatalf("diagnostic evidence payload was not written: %s", raw)
	}
}
