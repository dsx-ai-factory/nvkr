// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestQualificationFilesAdmissionBeforeIO(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil context", nil, e.CodeUsage},
		{"canceled", canceled, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, report, err := CheckQualificationFiles(tc.ctx, QualificationFilesOptions{})
			assertCode(t, err, tc.code)
			if receipt != nil || report != nil {
				t.Fatal("admission failure returned partial output")
			}
		})
	}
	want := e.CodeUsage
	if runtime.GOOS != "linux" {
		want = e.CodeEngineCapability
	}
	_, _, err := CheckQualificationFiles(t.Context(), QualificationFilesOptions{})
	assertCode(t, err, want)
	want = e.CodeTrustPolicyMissing
	if runtime.GOOS != "linux" {
		want = e.CodeEngineCapability
	}
	_, _, err = CheckQualificationFiles(t.Context(), QualificationFilesOptions{
		DefinitionPath: "/not-present/definition.yaml", EnvironmentPath: "/not-present/environment.yaml",
		PlanPath: "/not-present/plan.json", CandidatePath: "/not-present/candidate.json",
		Consumer: ConsumerOptions{CosignBinary: "/not-present/cosign", CosignDigest: "sha256:" + strings.Repeat("a", 64)},
	})
	assertCode(t, err, want)
}

func TestQualificationCandidateStrictSchema(t *testing.T) {
	reference := "registry.example/release@sha256:" + strings.Repeat("a", 64)
	raw, err := canonical.Marshal(QualificationCandidate{SchemaVersion: QualificationCandidateSchema, Reference: reference,
		Predicates: []QualificationPredicate{{Reference: reference, Predicate: []byte("{}")}}})
	if err != nil {
		t.Fatal(err)
	}
	valid := string(raw)
	for _, tc := range []struct {
		name, raw string
		pass      bool
	}{
		{"valid transport only", valid, true},
		{"unknown", strings.Replace(valid, `"schemaVersion":`, `"passed":true,"schemaVersion":`, 1), false},
		{"duplicate", strings.Replace(valid, `"schemaVersion":`, `"schemaVersion":"forged","schemaVersion":`, 1), false},
		{"wrong schema", strings.Replace(valid, "candidate/v1", "candidate/v2", 1), false},
		{"missing evidence", `{"schemaVersion":"nvkr.dev/qualification-candidate/v1","reference":"root","predicates":[]}`, false},
		{"malformed", "{", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidate.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := readQualificationCandidate(t.Context(), path)
			if tc.pass {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, e.CodeProvenanceMissing)
			}
		})
	}
}
