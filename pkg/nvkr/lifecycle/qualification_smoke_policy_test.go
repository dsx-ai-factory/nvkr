// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func nativeSmokePolicyFixture() QualificationPolicy {
	policy := qualificationPolicyFixture()
	policy.QualificationWorkflow = ".github/workflows/qualify.yml"
	policy.Cosign.Digest = ""
	policy.Cosign.PlatformDigests = map[string]string{"linux/amd64": "sha256:" + strings.Repeat("a", 64), "linux/arm64": "sha256:" + strings.Repeat("b", 64)}
	policy.Smoke = &QualificationSmokePolicy{Workflow: ".github/workflows/smoke.yml",
		Images:         map[string]string{"linux/amd64": "registry.example/smoke@sha256:" + strings.Repeat("c", 64), "linux/arm64": "registry.example/smoke@sha256:" + strings.Repeat("d", 64)},
		Tools:          map[string]string{"nvkectl": "nvkectl-version-json", "helm": "helm-version", "kubectl": "kubectl-client-version-json"},
		NonToolOutputs: []QualificationNonToolOutput{{Output: "deployment", Kind: OutputOCI, Components: []string{"server"}, Reason: "deployment-image"}}}
	return policy
}

func TestQualificationSmokePolicyRefusesAmbiguousTrust(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*QualificationPolicy)
		code e.Code
	}{
		{"distinct native workers", func(*QualificationPolicy) {}, ""},
		{"same signing role", func(p *QualificationPolicy) { p.Smoke.Workflow = p.QualificationWorkflow }, e.CodeTrustPolicyMissing},
		{"builder worker role", func(p *QualificationPolicy) { p.Smoke.Workflow = p.Source.Workflow }, e.CodeTrustPolicyMissing},
		{"remote reusable workflow", func(p *QualificationPolicy) { p.Smoke.Workflow = "other/repo/.github/workflows/smoke.yml@main" }, e.CodeTrustPolicyMissing},
		{"no signer workflow", func(p *QualificationPolicy) { p.QualificationWorkflow = "" }, e.CodeTrustPolicyMissing},
		{"no image", func(p *QualificationPolicy) { p.Smoke.Images = nil }, e.CodeTrustPolicyMissing},
		{"mutable image", func(p *QualificationPolicy) { p.Smoke.Images["linux/amd64"] = "registry.example/smoke:latest" }, e.CodeUnpinnedInput},
		{"image URL", func(p *QualificationPolicy) {
			p.Smoke.Images["linux/amd64"] = "https://" + p.Smoke.Images["linux/amd64"]
		}, e.CodeUnpinnedInput},
		{"image wrong digest", func(p *QualificationPolicy) {
			p.Smoke.Images["linux/amd64"] = "registry.example/smoke@sha512:" + strings.Repeat("c", 128)
		}, e.CodeUnpinnedInput},
		{"uncovered platform", func(p *QualificationPolicy) { delete(p.Cosign.PlatformDigests, "linux/arm64") }, e.CodeUnpinnedInput},
		{"ambiguous cosign pin", func(p *QualificationPolicy) { p.Cosign.Digest = p.Cosign.PlatformDigests["linux/amd64"] }, e.CodeUnpinnedInput},
		{"one binary for both architectures", func(p *QualificationPolicy) {
			p.Cosign.Digest = p.Cosign.PlatformDigests["linux/amd64"]
			p.Cosign.PlatformDigests = nil
		}, e.CodeUnpinnedInput},
		{"unsupported cosign architecture", func(p *QualificationPolicy) {
			p.Cosign.PlatformDigests["linux/ppc64le"] = "sha256:" + strings.Repeat("e", 64)
		}, e.CodeUnpinnedInput},
		{"no tool policy", func(p *QualificationPolicy) { p.Smoke.Tools = nil }, e.CodeTrustPolicyMissing},
		{"arbitrary command", func(p *QualificationPolicy) { p.Smoke.Tools["nvkectl"] = "sh -c true" }, e.CodeEngineCapability},
		{"wildcard tool", func(p *QualificationPolicy) { p.Smoke.Tools["*"] = "helm-version" }, e.CodeTrustPolicyMissing},
		{"wildcard exemption", func(p *QualificationPolicy) { p.Smoke.NonToolOutputs[0].Output = "*" }, e.CodeTrustPolicyMissing},
		{"exempt executable", func(p *QualificationPolicy) { p.Smoke.NonToolOutputs[0].Kind = OutputExecutable }, e.CodeTrustPolicyMissing},
		{"no exemption role", func(p *QualificationPolicy) { p.Smoke.NonToolOutputs[0].Reason = "" }, e.CodeTrustPolicyMissing},
		{"omitted component scope", func(p *QualificationPolicy) { p.Smoke.NonToolOutputs[0].Components = nil }, e.CodeTrustPolicyMissing},
		{"repeated component scope", func(p *QualificationPolicy) { p.Smoke.NonToolOutputs[0].Components = []string{"server", "server"} }, e.CodeTrustPolicyMissing},
		{"repeated exemption", func(p *QualificationPolicy) {
			p.Smoke.NonToolOutputs = append(p.Smoke.NonToolOutputs, p.Smoke.NonToolOutputs[0])
		}, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := nativeSmokePolicyFixture()
			tc.edit(&policy)
			err := policy.Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, tc.code)
			}
		})
	}
}

func TestQualificationPolicyIdentityBindsAllPlatforms(t *testing.T) {
	policy := nativeSmokePolicyFixture()
	policy.OutsideMVPSuites = []string{"zeta", "alpha"}
	policy.Smoke.NonToolOutputs = append(policy.Smoke.NonToolOutputs, QualificationNonToolOutput{Output: "apt", Kind: OutputAPT, Components: []string{"zeta", "alpha"}, Reason: "package-repository"})
	originalOrder := slices.Clone(policy.OutsideMVPSuites)
	want, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(originalOrder, policy.OutsideMVPSuites) || policy.Smoke.NonToolOutputs[0].Output != "deployment" || policy.Smoke.NonToolOutputs[1].Components[0] != "zeta" {
		t.Fatal("digest normalization mutated caller policy")
	}
	policy.Cosign.Path = "/another/runner/cosign"
	slices.Reverse(policy.OutsideMVPSuites)
	slices.Reverse(policy.Smoke.NonToolOutputs)
	slices.Reverse(policy.Smoke.NonToolOutputs[0].Components)
	got, err := policy.Digest()
	if err != nil || got != want {
		t.Fatalf("locator or set ordering changed security policy: %s %v", got, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*QualificationPolicy)
	}{
		{"other architecture verifier", func(p *QualificationPolicy) {
			p.Cosign.PlatformDigests["linux/arm64"] = "sha256:" + strings.Repeat("e", 64)
		}},
		{"other architecture image", func(p *QualificationPolicy) {
			p.Smoke.Images["linux/arm64"] = "registry.example/smoke@sha256:" + strings.Repeat("e", 64)
		}},
		{"worker signer", func(p *QualificationPolicy) { p.Smoke.Workflow = ".github/workflows/another-smoke.yml" }},
		{"tool profile", func(p *QualificationPolicy) { p.Smoke.Tools["nvkectl"] = "helm-version" }},
		{"exact exempt output", func(p *QualificationPolicy) { p.Smoke.NonToolOutputs[0].Output = "another" }},
		{"suite scope", func(p *QualificationPolicy) { p.OutsideMVPSuites = []string{"another"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := nativeSmokePolicyFixture()
			before, err := p.Digest()
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&p)
			after, err := p.Digest()
			if err != nil || before == after {
				t.Fatalf("policy change did not change identity: %v", err)
			}
		})
	}
}

func TestQualificationCosignSelectsNativePin(t *testing.T) {
	policy := nativeSmokePolicyFixture().Cosign
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		got, err := policy.ExecutableDigest(platform)
		if err != nil || got != policy.PlatformDigests[platform] {
			t.Fatalf("pin for %s: %s %v", platform, got, err)
		}
	}
	_, err := policy.ExecutableDigest("darwin/arm64")
	assertCode(t, err, e.CodeEngineCapability)
	delete(policy.PlatformDigests, "linux/arm64")
	_, err = policy.ExecutableDigest("linux/arm64")
	assertCode(t, err, e.CodeUnpinnedInput)
}
