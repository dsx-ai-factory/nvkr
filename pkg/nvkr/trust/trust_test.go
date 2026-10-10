// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package trust

import (
	"os"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func code(err error) nvkrerr.Code {
	if e, ok := err.(*nvkrerr.Error); ok {
		return e.Code
	}
	return ""
}

func TestPrecedence(t *testing.T) {
	s := Store{CLIIdentities: []string{"cli-id"}, OperatorIdentities: []string{"op-id"}, CatalogAdvisory: []string{"cat-id"}}
	if ids, src := s.Trusted(); src != "cli" || ids[0] != "cli-id" {
		t.Fatalf("CLI must win: %v %s", ids, src)
	}
	s.CLIIdentities = nil
	if _, src := s.Trusted(); src != "operator" {
		t.Fatal("operator store next")
	}
	s.OperatorIdentities = nil
	if _, src := s.Trusted(); src != "none" {
		t.Fatal("catalog-advisory must NOT be auto-trusted")
	}
	s.PromoteAdvisory = true
	if _, src := s.Trusted(); src != "catalog-advisory(promoted)" {
		t.Fatal("promoted advisory becomes trusted")
	}
}

// TestVerifierIsolation enforces ADR-04 §3: the measure-only verify path must not
// import the build engine (pkg/nvkr/pipeline) or the report-shaped attest package,
// directly OR transitively, so a shared defect cannot bias both artifact and proof.
func TestVerifierIsolation(t *testing.T) {
	forbidden := []string{
		`nvkr/pkg/nvkr/pipeline`,
		`nvkr/pkg/nvkr/attest`,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(ent.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range forbidden {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s imports %q — the verify path must be isolated from the builder (ADR-04 §3)", ent.Name(), bad)
			}
		}
	}
}
