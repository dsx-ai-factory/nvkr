// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
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

func TestQualificationFilesRejectsCredentialHostBeforeInputAndProviderIO(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("qualification file adapter requires Linux")
	}
	var requests atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("WWW-Authenticate", `Basic realm="qualification-registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer registry.Close()
	reference := strings.TrimPrefix(registry.URL, "http://") + "/product/releases@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, host string
		code       e.Code
	}{
		{"missing independently selected host", "", e.CodeTrustPolicyMissing},
		{"candidate selects different host", "ghcr.io", e.CodeUntrustedSigner},
		{"host is URL", registry.URL, e.CodeTrustPolicyMissing},
		{"host includes namespace", "ghcr.io/product", e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			candidate := filepath.Join(root, "candidate.json")
			writeQualificationHandoffFile(t, candidate, qualificationHandoffJSON(t, QualificationCandidate{
				SchemaVersion: QualificationCandidateSchema, Reference: reference,
				Predicates: []QualificationPredicate{{Reference: reference, Predicate: []byte(`{}`)}},
			}))
			source := qualificationSourceFixture().value
			receipt, report, err := CheckQualificationFiles(t.Context(), QualificationFilesOptions{
				// Host admission must precede even input loads, compilation and
				// provider authentication; these inputs deliberately do not exist.
				DefinitionPath:  filepath.Join(root, "missing-definition.yaml"),
				EnvironmentPath: filepath.Join(root, "missing-environment.yaml"),
				PlanPath:        filepath.Join(root, "missing-plan.json"), CandidatePath: candidate,
				Source: qualificationorigin.SourceRequest{Repository: source.Repository, RunID: source.RunID,
					Attempt: source.Attempt, Commit: source.Commit, Workflow: source.Workflow, DefaultBranch: source.DefaultBranch},
				Consumer: ConsumerOptions{CosignBinary: filepath.Join(root, "must-not-execute"),
					CosignDigest: "sha256:" + strings.Repeat("b", 64), WorkspaceRoot: filepath.Join(root, "must-not-materialize"),
					Registry:   delivery.RegistryOptions{PlainHTTP: true, Username: "user", Password: "private"},
					Credential: sign.Credential{Registry: tc.host, Username: "user", Password: "private"}},
			})
			assertCode(t, err, tc.code)
			if receipt != nil || report != nil || requests.Load() != 0 {
				t.Fatal("refused credential host returned authority or contacted the candidate registry")
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("credential leaked into admission error")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "candidate.json" {
				t.Fatalf("refusal changed the workspace: %v, %v", entries, err)
			}
		})
	}
}
