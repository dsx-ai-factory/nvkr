// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/conformance"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func TestConformanceCLIRejectsSharedClusterBeforeToolWork(t *testing.T) {
	config := conformance.FileConfiguration{SchemaVersion: conformance.ConfigurationSchema, RunTimeoutSeconds: 60, CommandTimeoutSeconds: 30, CleanupTimeoutSeconds: 30, Observation: testobservation.Observation{Environment: testobservation.Environment{Role: "tenant"}}}
	raw, err := canonical.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	app := newApp()
	var output bytes.Buffer
	app.Writer = &output
	app.ErrWriter = &output
	err = app.RunContext(t.Context(), []string{"nvkr", "evidence", "conformance", "--configuration", path})
	if cliErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("proper-cluster producer unexpectedly admitted: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("rejected producer emitted partial result")
	}
}
