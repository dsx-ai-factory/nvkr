// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
)

func tektonPolicyFixture(t *testing.T) QualificationPolicy {
	t.Helper()
	local := localPolicyFixture(t)
	return QualificationPolicy{SchemaVersion: QualificationPolicySchema, Cosign: QualificationCosignPolicy{Digest: local.CosignDigest}, Smoke: &local.Smoke,
		Tekton: &qualificationpolicy.Tekton{APIServer: "https://cluster.example.invalid", Namespace: "nvkr", Platform: local.Platform, PlanDigest: local.PlanDigest, PublisherKey: local.BuilderKey, SmokeKey: local.SmokeKey, QualifierKey: local.QualifierKey}}
}

func TestTektonQualificationPolicyRefusesMixedTrust(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*QualificationPolicy)
		code e.Code
	}{
		{"exact cluster", func(*QualificationPolicy) {}, ""},
		{"hosted source", func(p *QualificationPolicy) { p.Source.Repository = "owner/repository" }, e.CodeTrustPolicyMissing},
		{"hosted qualifier", func(p *QualificationPolicy) { p.QualificationWorkflow = ".github/workflows/qualify.yml" }, e.CodeTrustPolicyMissing},
		{"hosted worker", func(p *QualificationPolicy) { p.Smoke.Workflow = ".github/workflows/smoke.yml" }, e.CodeTrustPolicyMissing},
		{"missing Plan", func(p *QualificationPolicy) { p.Tekton.PlanDigest = "" }, e.CodeTrustPolicyMissing},
		{"insecure API", func(p *QualificationPolicy) { p.Tekton.APIServer = "http://cluster.example.invalid" }, e.CodeTrustPolicyMissing},
		{"same role keys", func(p *QualificationPolicy) { p.Tekton.QualifierKey = p.Tekton.PublisherKey }, e.CodeTrustPolicyMissing},
		{"foreign smoke platform", func(p *QualificationPolicy) { p.Tekton.Platform = "linux/arm64" }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tektonPolicyFixture(t)
			tc.edit(&p)
			err := p.Validate()
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTektonSmokeOriginsRequireExactRunAndRole(t *testing.T) {
	policy := tektonPolicyFixture(t)
	uri := "https://cluster.example.invalid/apis/tekton.dev/v1/namespaces/nvkr/pipelineruns/run?uid=11111111-1111-1111-1111-111111111111"
	builder, _ := policy.Tekton.Identity("publisher")
	worker, _ := policy.Tekton.Identity("smoke")
	source := qualificationorigin.Description{Provider: "tekton", InvocationID: uri, BuilderID: builder}
	current := qualificationorigin.Description{Provider: "tekton", InvocationID: uri, BuilderID: worker, Task: "nvkr-smoke", TaskUID: "22222222-2222-2222-2222-222222222222"}
	for _, tc := range []struct {
		name string
		edit func(*qualificationorigin.Description)
		code e.Code
	}{
		{"exact", func(*qualificationorigin.Description) {}, ""},
		{"foreign Run", func(d *qualificationorigin.Description) {
			d.InvocationID = "https://cluster.example.invalid/apis/tekton.dev/v1/namespaces/nvkr/pipelineruns/foreign?uid=33333333-3333-3333-3333-333333333333"
		}, e.CodeUntrustedSigner},
		{"invented GitHub context", func(d *qualificationorigin.Description) { d.Repository = "owner/repo" }, e.CodeVerifyFailed},
		{"publisher as worker", func(d *qualificationorigin.Description) { d.BuilderID = builder }, e.CodeVerifyFailed},
		{"missing task UID", func(d *qualificationorigin.Description) { d.TaskUID = "" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := current
			tc.edit(&value)
			err := matchNativeSmokeOrigins(source, value, policy)
			if tc.code == "" && err != nil {
				t.Fatal(err)
			}
			if tc.code != "" {
				assertCode(t, err, tc.code)
			}
		})
	}
	var empty TektonInvocation
	_, err := empty.Description()
	assertCode(t, err, e.CodeGateUnmet)
	assertCode(t, empty.RequireSigning(), e.CodeGateUnmet)
}
