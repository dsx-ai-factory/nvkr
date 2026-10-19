// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
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

func workerPolicyFixture() lifecycle.QualificationPolicy {
	digest := "sha256:" + strings.Repeat("a", 64)
	return lifecycle.QualificationPolicy{SchemaVersion: lifecycle.QualificationPolicySchema,
		Source:                qualificationorigin.SourcePolicy{Repository: "example/nvkr", Workflow: ".github/workflows/build.yml", DefaultBranch: "main"},
		QualificationWorkflow: ".github/workflows/qualify.yml", Cosign: lifecycle.QualificationCosignPolicy{Path: "cosign", Digest: digest},
		Smoke: &lifecycle.QualificationSmokePolicy{Workflow: ".github/workflows/smoke.yml", Images: map[string]string{"linux/amd64": "registry.example.test/smoke@" + digest}, Tools: map[string]string{"tool": "helm-version"}},
	}
}

func TestSmokeWorkerPolicyRequiresIndependentNativeRole(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(*lifecycle.QualificationPolicy)
		platform string
		code     e.Code
	}{
		{"separate role", func(*lifecycle.QualificationPolicy) {}, "linux/amd64", ""},
		{"missing smoke", func(p *lifecycle.QualificationPolicy) { p.Smoke = nil }, "linux/amd64", e.CodeTrustPolicyMissing},
		{"build role", func(p *lifecycle.QualificationPolicy) { p.Smoke.Workflow = p.Source.Workflow }, "linux/amd64", e.CodeTrustPolicyMissing},
		{"final role", func(p *lifecycle.QualificationPolicy) { p.Smoke.Workflow = p.QualificationWorkflow }, "linux/amd64", e.CodeTrustPolicyMissing},
		{"missing native image", func(*lifecycle.QualificationPolicy) {}, "linux/arm64", e.CodeEngineCapability},
		{"unknown platform", func(*lifecycle.QualificationPolicy) {}, "linux/386", e.CodeEngineCapability},
		{"missing cosign pin", func(p *lifecycle.QualificationPolicy) { p.Cosign.Digest = "" }, "linux/amd64", e.CodeUnpinnedInput},
		{"required external suite", func(p *lifecycle.QualificationPolicy) { p.RequiredSuites = []string{"external"} }, "linux/amd64", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := workerPolicyFixture()
			tc.edit(&policy)
			identity, digest, err := admitSmokeWorkerPolicy(policy, tc.platform)
			if tc.code != "" {
				requireCode(t, err, tc.code)
				if identity != "" || digest != "" {
					t.Fatal("invalid policy returned signer selection")
				}
				return
			}
			if err != nil || identity != "https://github.com/example/nvkr/.github/workflows/smoke.yml@refs/heads/main" || digest != policy.Cosign.Digest {
				t.Fatalf("selection %s %s %v", identity, digest, err)
			}
		})
	}
}

func workerReportFixture() lifecycle.NativeToolSmokeReport {
	fixture := evidenceFixture()
	checks := fixture.ArtifactChecks
	digest := checks.CatalogDigest
	checks.Inputs.ReleaseSubject = "registry.example.test/release@" + digest
	checks.Outputs = checks.Outputs[:1]
	checks.Outputs[0].Kind = lifecycle.OutputExecutable
	checks.Outputs[0].Platform = "linux/amd64"
	return lifecycle.NativeToolSmokeReport{SchemaVersion: lifecycle.NativeToolSmokeSchema, ArtifactChecks: checks, Invocation: fixture.Invocation,
		PolicyDigest: digest, Platform: "linux/amd64", Image: "registry.example.test/smoke@" + digest, RuntimeImage: digest, RuntimeIdentity: digest, Status: "pass",
		Members: []lifecycle.ToolSmokeMember{{Output: checks.Outputs[0].Name, Member: "helm", Subject: digest, BuildDigest: digest, PayloadDigest: digest, Size: 100, Platform: "linux/amd64", Profile: "helm-version", ExpectedVersion: "v1.2.3", ObservedVersion: "v1.2.3"}}}
}

type workerTestReceipt struct {
	report lifecycle.NativeToolSmokeReport
	closed bool
	after  func()
}

func (r *workerTestReceipt) Description() (lifecycle.NativeToolSmokeReport, error) {
	if r.closed {
		return lifecycle.NativeToolSmokeReport{}, e.New(e.CodeGateUnmet, "closed smoke receipt")
	}
	if r.after != nil {
		r.after()
	}
	return r.report, nil
}

func TestSmokeWorkerSignsOnlyLiveCanonicalPolicyBoundReport(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*workerTestReceipt)
		code e.Code
	}{
		{"exact", func(*workerTestReceipt) {}, ""},
		{"closed", func(r *workerTestReceipt) { r.closed = true }, e.CodeGateUnmet},
		{"failed", func(r *workerTestReceipt) { r.report.Status = "fail" }, e.CodeVerifyFailed},
		{"other policy", func(r *workerTestReceipt) { r.report.PolicyDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeGateUnmet},
		{"other platform", func(r *workerTestReceipt) { r.report.Platform = "linux/arm64" }, e.CodeGateUnmet},
		{"incomplete scope", func(r *workerTestReceipt) { r.report.Members = nil }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := &workerTestReceipt{report: workerReportFixture()}
			expected := receipt.report.PolicyDigest
			tc.edit(receipt)
			raw, err := liveSmokeWorkerDocument(t.Context(), receipt, expected, "linux/amd64")
			if tc.code != "" {
				requireCode(t, err, tc.code)
				if raw != nil {
					t.Fatal("failed receipt returned signing bytes")
				}
				return
			}
			want, err := receipt.report.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, want) {
				t.Fatal("signing bytes differ from owned report")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	receipt := &workerTestReceipt{report: workerReportFixture(), after: cancel}
	_, err := liveSmokeWorkerDocument(ctx, receipt, receipt.report.PolicyDigest, "linux/amd64")
	requireCode(t, err, e.CodeTimeout)
	_, err = liveSmokeWorkerDocument(t.Context(), nil, "", "linux/amd64")
	requireCode(t, err, e.CodeGateUnmet)
	_, err = liveSmokeWorkerDocument(t.Context(), &lifecycle.NativeToolSmokeReceipt{}, "", "linux/amd64")
	requireCode(t, err, e.CodeGateUnmet)
}

func TestTriggeredSmokeWorkerAdmitsBeforePolicyAndProviderIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := RunTriggeredSmokeWorker(ctx, "/missing/policy", lifecycle.QualificationRuntimeOptions{})
	requireCode(t, err, e.CodeTimeout)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled worker returned output or lost cause")
	}
	_, err = RunTriggeredSmokeWorker(nil, "/missing/policy", lifecycle.QualificationRuntimeOptions{}) //nolint:staticcheck // Nil-context refusal is the public contract.
	requireCode(t, err, e.CodeUsage)
	if runtime.GOOS != "linux" {
		_, err = RunTriggeredSmokeWorker(t.Context(), "/missing/policy", lifecycle.QualificationRuntimeOptions{})
		requireCode(t, err, e.CodeEngineCapability)
		return
	}
	policy := workerPolicyFixture()
	policy.Smoke.Images = map[string]string{"linux/" + runtime.GOARCH: policy.Smoke.Images["linux/amd64"]}
	root := t.TempDir()
	t.Chdir(root)
	policy.Cosign.Path = filepath.Join(root, "cosign")
	if err := os.WriteFile(policy.Cosign.Path, []byte("untrusted binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "policy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "must-not-create")
	_, err = RunTriggeredSmokeWorker(t.Context(), path, lifecycle.QualificationRuntimeOptions{WorkspaceRoot: workspace})
	requireCode(t, err, e.CodePipelineDrift)
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("failed generated-policy admission touched artifact/output workspace")
	}
}
