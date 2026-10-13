// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestFileConfigurationRejectsUnsupportedAndOverflowBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    FileConfiguration
	}{
		{"unknown schema", FileConfiguration{SchemaVersion: "future", RunTimeoutSeconds: 1, CommandTimeoutSeconds: 1, CleanupTimeoutSeconds: 1}},
		{"duration overflow", FileConfiguration{SchemaVersion: ConfigurationSchema, RunTimeoutSeconds: 1 << 62, CommandTimeoutSeconds: 1, CleanupTimeoutSeconds: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := canonical.Marshal(tc.c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "configuration.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := RunFiles(t.Context(), path)
			var coded *e.Error
			if result != nil || !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatal("unsupported configuration reached producer")
			}
		})
	}
}

func TestPrivateRecordPreservesOriginalAndCannotOverwrite(t *testing.T) {
	r := &Result{PrivateWorkspace: t.TempDir(), rawJUnit: []byte("original failed result")}
	if err := r.SavePrivateDescription(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(r.PrivateWorkspace, "review-junit.xml"))
	if err != nil || string(raw) != "original failed result" {
		t.Fatal("original changed", err)
	}
	err = r.SavePrivateDescription(t.Context())
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeFetchFailed {
		t.Fatalf("overwrite admission=%v", err)
	}
}

func TestPrivateConfigurationModeAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOptions(t.Context(), path)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || !strings.Contains(err.Error(), "owner-only") {
		t.Fatal(err)
	}
}
