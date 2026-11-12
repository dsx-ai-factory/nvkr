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
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestGenerateFilesUsesLifecycleInputsForSupportedHostedEngines(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definition := "docs/examples/nvke-tutorial/definition.yaml"
	environments := map[string]string{
		factory.EngineGitHub: "docs/examples/nvke-tutorial/environment-github.yaml",
	}
	for engine, environment := range environments {
		t.Run(engine, func(t *testing.T) {
			root := t.TempDir()
			digest, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", engine, root, false)
			if err != nil {
				t.Fatal(err)
			}
			if !canonical.IsDigest(digest) {
				t.Fatalf("plan digest = %q", digest)
			}
			checked, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", engine, root, true)
			if err != nil {
				t.Fatal(err)
			}
			if checked != digest {
				t.Fatalf("checked digest %s != generated digest %s", checked, digest)
			}
		})
	}
}

// A selected projection adapter that cannot launch proves which paths consult
// the selection: top-level check and write render through it and write
// nothing on refusal, and runtime refresh refuses it before reading inputs.
func TestGenerateFilesHonorsProjectionSelectionOnlyAtTopLevel(t *testing.T) {
	selected := factory.WithProjectionAdapter(context.Background(),
		factory.ProjectionAdapter{Transport: factory.ProjectionOutOfProcess, Path: "/nonexistent/nvkr-projection-adapter", Release: "test"})
	untouched := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := GenerateRuntimeFiles(selected, "missing-definition.yaml", "missing-environment.yaml", "test", factory.EngineGitHub, untouched, true)
	assertCode(t, err, nvkrerr.CodeEngineCapability)

	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definition, environment := "docs/examples/nvke-tutorial/definition.yaml", "docs/examples/nvke-tutorial/environment-github.yaml"
	root := t.TempDir()
	if _, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", factory.EngineGitHub, root, false); err != nil {
		t.Fatal(err)
	}
	_, err = GenerateFiles(selected, definition, environment, "test (deadbeef)", factory.EngineGitHub, root, true)
	assertCode(t, err, nvkrerr.CodeEngineCapability)
	_, err = GenerateFiles(selected, definition, environment, "test (deadbeef)", factory.EngineGitHub, untouched, false)
	assertCode(t, err, nvkrerr.CodeEngineCapability)
	if _, statErr := os.Stat(untouched); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused projection touched output root: %v", statErr)
	}
}

func TestGenerateFilesRejectsUnsupportedEngineBeforeReadingInputsOrOutput(t *testing.T) {
	for _, engine := range []string{factory.EngineGitLab, factory.EngineArgo} {
		t.Run(engine, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			_, err := GenerateFiles(context.Background(), "missing-definition.yaml", "missing-environment.yaml", "test", engine, root, false)
			assertCode(t, err, nvkrerr.CodeEngineCapability)
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsupported generation touched output root: %v", statErr)
			}
		})
	}
}
