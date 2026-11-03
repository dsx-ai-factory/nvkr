// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"errors"
	"os"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestQualificationGenerationRefusesCompositeBeforeWork(t *testing.T) {
	t.Chdir(t.TempDir())
	composite := "apiVersion: nvkr.dev/composite-definition/v1\nkind: CompositeDefinition\nmetadata: {name: product}\n" +
		"members: [{name: alpha, definition: alpha.yaml, digest: sha256:0000000000000000000000000000000000000000000000000000000000000000}]\n"
	if err := os.WriteFile("composite.yaml", []byte(composite), 0o600); err != nil {
		t.Fatal(err)
	}
	// No policy, Environment, member or catalog exists: refusal must come first.
	_, err := GenerateFiles(t.Context(), "composite.yaml", "environment.yaml", "policy.yaml", "composite-test", factory.EngineGitHub, "out", false)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("want engine-capability; got %v", err)
	}
	if _, err := os.Stat("out"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused generation wrote output: %v", err)
	}
}
