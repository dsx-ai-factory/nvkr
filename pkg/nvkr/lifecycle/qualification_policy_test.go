// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func qualificationPolicyFixture() QualificationPolicy {
	return QualificationPolicy{SchemaVersion: QualificationPolicySchema,
		Source: qualificationorigin.SourcePolicy{Repository: "example/nvkr", Workflow: ".github/workflows/build.yml", DefaultBranch: "main"},
		Cosign: QualificationCosignPolicy{Digest: "sha256:" + strings.Repeat("a", 64)}}
}

func TestQualificationPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*QualificationPolicy)
		code e.Code
	}{
		{"exact bootstrap version", func(p *QualificationPolicy) { p.Cosign.Version = "v3.0.2" }, ""},
		{"mutable bootstrap version", func(p *QualificationPolicy) { p.Cosign.Version = "latest" }, e.CodeUnpinnedInput},
		{"bootstrap URL injection", func(p *QualificationPolicy) { p.Cosign.Version = "v3.0.2/../../other" }, e.CodeUnpinnedInput},
		{"artifact checks only", func(*QualificationPolicy) {}, ""},
		{"independent qualifier", func(p *QualificationPolicy) { p.QualificationWorkflow = ".github/workflows/qualify.yml" }, ""},
		{"required suite declaration", func(p *QualificationPolicy) { p.RequiredSuites = []string{"nvke"} }, ""},
		{"unsupported schema", func(p *QualificationPolicy) { p.SchemaVersion = "future" }, e.CodeTrustPolicyMissing},
		{"missing repository", func(p *QualificationPolicy) { p.Source.Repository = "" }, e.CodeTrustPolicyMissing},
		{"unapproved branch", func(p *QualificationPolicy) { p.Source.DefaultBranch = "*" }, e.CodeTrustPolicyMissing},
		{"unapproved source workflow", func(p *QualificationPolicy) { p.Source.Workflow = "build.yml" }, e.CodeTrustPolicyMissing},
		{"same workflow", func(p *QualificationPolicy) { p.QualificationWorkflow = p.Source.Workflow }, e.CodeTrustPolicyMissing},
		{"invalid qualifier path", func(p *QualificationPolicy) { p.QualificationWorkflow = "../qualify.yml" }, e.CodeTrustPolicyMissing},
		{"missing pin", func(p *QualificationPolicy) { p.Cosign.Digest = "" }, e.CodeUnpinnedInput},
		{"uppercase pin", func(p *QualificationPolicy) { p.Cosign.Digest = "sha256:" + strings.Repeat("A", 64) }, e.CodeUnpinnedInput},
		{"path whitespace", func(p *QualificationPolicy) { p.Cosign.Path = " cosign" }, e.CodeTrustPolicyMissing},
		{"path control", func(p *QualificationPolicy) { p.Cosign.Path = "bin/\x00cosign" }, e.CodeTrustPolicyMissing},
		{"empty suite", func(p *QualificationPolicy) { p.OutsideMVPSuites = []string{""} }, e.CodeTrustPolicyMissing},
		{"suite whitespace", func(p *QualificationPolicy) { p.RequiredSuites = []string{"nvke "} }, e.CodeTrustPolicyMissing},
		{"duplicate outside suite", func(p *QualificationPolicy) { p.OutsideMVPSuites = []string{"nvke", "nvke"} }, e.CodeTrustPolicyMissing},
		{"duplicate required suite", func(p *QualificationPolicy) { p.RequiredSuites = []string{"nvke", "nvke"} }, e.CodeTrustPolicyMissing},
		{"contradictory suite", func(p *QualificationPolicy) {
			p.OutsideMVPSuites = []string{"nvke"}
			p.RequiredSuites = []string{"nvke"}
		}, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := qualificationPolicyFixture()
			tc.edit(&policy)
			err := policy.Validate()
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadQualificationPolicyStrictDocuments(t *testing.T) {
	valid := "schemaVersion: nvkr.dev/qualification-policy/v1\nsource:\n  repository: example/nvkr\n  workflow: .github/workflows/build.yml\n  defaultBranch: main\ncosign:\n  digest: sha256:" + strings.Repeat("a", 64) + "\n"
	rawJSON, err := json.Marshal(qualificationPolicyFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  string
		code e.Code
	}{
		{"YAML", valid, ""},
		{"JSON", string(rawJSON), ""},
		{"unknown field", valid + "passed: true\n", e.CodeTrustPolicyMissing},
		{"unknown nested field", strings.Replace(valid, "source:\n", "source:\n  passed: true\n", 1), e.CodeTrustPolicyMissing},
		{"duplicate YAML", valid + "schemaVersion: nvkr.dev/qualification-policy/v1\n", e.CodeTrustPolicyMissing},
		{"duplicate JSON", strings.TrimSuffix(string(rawJSON), "}") + `,"schemaVersion":"nvkr.dev/qualification-policy/v1"}`, e.CodeTrustPolicyMissing},
		{"second document", valid + "---\npassed: true\n", e.CodeTrustPolicyMissing},
		{"empty second document", valid + "---\n", e.CodeTrustPolicyMissing},
		{"numeric suite", valid + "requiredSuites: [123]\n", e.CodeTrustPolicyMissing},
		{"boolean suite", valid + "requiredSuites: [true]\n", e.CodeTrustPolicyMissing},
		{"null", "null\n", e.CodeTrustPolicyMissing},
		{"oversized", valid + "#" + strings.Repeat("x", maxQualificationPolicyBytes), e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.yaml")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			policy, err := LoadQualificationPolicy(t.Context(), path)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if policy.SchemaVersion != "" || policy.Source.Repository != "" || policy.Cosign.Digest != "" {
					t.Fatalf("invalid policy returned partial configuration: %+v", policy)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadQualificationPolicyExecutablePaths(t *testing.T) {
	for _, tc := range []struct{ name, configured, expected string }{
		{"default", "", "cosign"},
		{"bare command", "cosign-pinned", "cosign-pinned"},
		{"absolute", "/trusted/bin/cosign", "/trusted/bin/cosign"},
		{"relative", "tools/cosign", "tools/cosign"},
		{"explicit relative", "./cosign", "cosign"},
		{"parent relative", "../tools/cosign", "../tools/cosign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "policy.json")
			policy := qualificationPolicyFixture()
			policy.Cosign.Path = tc.configured
			raw, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadQualificationPolicy(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.expected
			if !filepath.IsAbs(tc.configured) && strings.ContainsRune(tc.configured, '/') {
				want = filepath.Join(root, tc.expected)
			}
			if loaded.Cosign.Path != want || loaded.Cosign.Digest != policy.Cosign.Digest {
				t.Fatalf("loaded cosign = %+v, want %s with unchanged pin", loaded.Cosign, want)
			}
		})
	}
}

func TestLoadQualificationPolicyAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil context", nil, e.CodeUsage},
		{"canceled before file IO", ctx, e.CodeTimeout},
		{"missing file", t.Context(), e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadQualificationPolicy(tc.ctx, filepath.Join(t.TempDir(), "missing"))
			assertCode(t, err, tc.code)
			if tc.ctx == ctx && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
		})
	}
}
