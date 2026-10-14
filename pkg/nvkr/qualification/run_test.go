// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func TestQualificationRunPolicyAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*lifecycle.QualificationPolicy)
		code e.Code
	}{
		{"independent policy", func(*lifecycle.QualificationPolicy) {}, ""},
		{"missing smoke", func(p *lifecycle.QualificationPolicy) { p.Smoke = nil }, e.CodeTrustPolicyMissing},
		{"missing qualifier", func(p *lifecycle.QualificationPolicy) { p.QualificationWorkflow = "" }, e.CodeTrustPolicyMissing},
		{"builder is qualifier", func(p *lifecycle.QualificationPolicy) { p.QualificationWorkflow = p.Source.Workflow }, e.CodeTrustPolicyMissing},
		{"required external suite", func(p *lifecycle.QualificationPolicy) { p.RequiredSuites = []string{"nvke"} }, e.CodeEngineCapability},
		{"contradictory external suite", func(p *lifecycle.QualificationPolicy) {
			p.RequiredSuites = []string{"nvke"}
			p.OutsideMVPSuites = []string{"nvke"}
		}, e.CodeTrustPolicyMissing},
		{"no verifier pin", func(p *lifecycle.QualificationPolicy) { p.Cosign.Digest = "" }, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := lifecycle.QualificationPolicy{SchemaVersion: lifecycle.QualificationPolicySchema,
				Source:                qualificationorigin.SourcePolicy{Repository: "example/nvkr", Workflow: ".github/workflows/build.yml", DefaultBranch: "main"},
				QualificationWorkflow: ".github/workflows/qualify.yml", Cosign: lifecycle.QualificationCosignPolicy{Path: "cosign", Digest: "sha256:" + strings.Repeat("a", 64)}}
			policy.Smoke = &lifecycle.QualificationSmokePolicy{Workflow: ".github/workflows/smoke.yml", Images: map[string]string{"linux/amd64": "registry.example.test/smoke@sha256:" + strings.Repeat("b", 64)}, Tools: map[string]string{"helm": "helm-version"}}
			tc.edit(&policy)
			err := admitRunPolicy(policy)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, tc.code)
			}
		})
	}
}

func TestQualificationRunCancellationBeforePolicyIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := PublishTriggeredRun(ctx, "/missing/qualification.yaml", lifecycle.QualificationRuntimeOptions{})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: result=%v err=%v", result, err)
	}
	requireCode(t, err, e.CodeTimeout)
}

func TestQualificationRunRequiresGeneratedPolicyBeforeSigningOrProviderIO(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("qualification owner requires Linux")
	}
	root := t.TempDir()
	t.Chdir(root)
	policy := workerPolicyFixture()
	policy.Source.Workflow = ".github/workflows/nvkr-run.yml"
	policy.Cosign.Path = "/must-not-execute"
	raw, err := canonical.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "qualification.json")
	if err := os.WriteFile(policyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "must-not-create")
	result, err := PublishTriggeredRun(t.Context(), policyPath, lifecycle.QualificationRuntimeOptions{WorkspaceRoot: workspace})
	requireCode(t, err, e.CodePipelineDrift)
	if result != nil {
		t.Fatal("missing generated policy returned publication diagnostics")
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing generated policy touched artifact workspace: %v", err)
	}
}

func TestQualificationSmokeFailurePreservesCompleteArtifactDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  e.Code
		cause error
	}{
		{"worker download", e.CodeFetchFailed, errors.New("worker artifact unavailable")},
		{"worker signature", e.CodeSignatureInvalid, errors.New("wrong worker signature")},
		{"canceled", e.CodeTimeout, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := evidenceFixture().ArtifactChecks
			before, err := canonical.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			original := e.Wrap(tc.code, tc.cause, "authenticate worker evidence")
			publication, err := smokeFailurePublication(&report, original)
			requireCode(t, err, tc.code)
			if err != original || !errors.Is(err, tc.cause) {
				t.Fatal("original coded error or cause changed")
			}
			if publication == nil || publication.ArtifactChecks != &report || publication.Successful || publication.Evidence != nil || publication.EvidenceDigest != "" || len(publication.PublishedSubjects) != 0 {
				t.Fatalf("failed authentication claimed publication: %+v", publication)
			}
			after, err := canonical.Marshal(publication.ArtifactChecks)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("smoke refusal altered actual artifact results")
			}
			if len(publication.Verdicts) != len(report.Outputs) {
				t.Fatal("smoke refusal lost known outputs")
			}
			for i, verdict := range publication.Verdicts {
				output := report.Outputs[i]
				if output.Overall != "pass" || verdict.Output != output.Name || verdict.Subject != output.Subject || verdict.Status != "fail" || verdict.Check != "delivered-tool-smoke-authentication" || !strings.Contains(verdict.Reason, string(tc.code)) || strings.Contains(verdict.Reason, "execution failed") {
					t.Fatalf("wrong smoke authentication diagnostics: %+v", verdict)
				}
			}
		})
	}
	original := e.New(e.CodeGateUnmet, "no authenticated artifact scope")
	if publication, err := smokeFailurePublication(nil, original); publication != nil || err != original {
		t.Fatal("missing scope invented diagnostics or changed error")
	}
}
