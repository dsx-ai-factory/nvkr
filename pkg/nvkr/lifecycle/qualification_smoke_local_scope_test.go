// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalSmokeScopeDoesNotRequireOrGrantHostedIdentity(t *testing.T) {
	plan, _, _, policy := nativeSmokeScopeFixture()
	hosted, err := qualificationSmokePlanScope(&plan, policy)
	if err != nil {
		t.Fatal(err)
	}
	local := *policy.Smoke
	local.Workflow = ""
	scope, err := toolSmokePlanScope(&plan, local)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scope, hosted) {
		t.Fatal("local execution changed exact membership or expected versions")
	}
	policy.Smoke = &local
	assertCode(t, ValidateQualificationSmokePlan(&plan, policy), e.CodeTrustPolicyMissing)
}

func TestLocalSmokeScopePreservesFailClosedAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*QualificationSmokePolicy)
		code e.Code
	}{
		{"missing image", func(p *QualificationSmokePolicy) { p.Images = nil }, e.CodeTrustPolicyMissing},
		{"mutable image", func(p *QualificationSmokePolicy) { p.Images["linux/amd64"] = "registry.example.test/smoke:latest" }, e.CodeUnpinnedInput},
		{"unsupported platform", func(p *QualificationSmokePolicy) {
			p.Images = map[string]string{"darwin/arm64": "registry.example.test/smoke@" + nativeSmokeTestDigest("1")}
		}, e.CodeUnpinnedInput},
		{"missing tool", func(p *QualificationSmokePolicy) { p.Tools = map[string]string{"other": "helm-version"} }, e.CodeGateUnmet},
		{"extra tool", func(p *QualificationSmokePolicy) { p.Tools["other"] = "helm-version" }, e.CodeReleaseIncomplete},
		{"arbitrary command", func(p *QualificationSmokePolicy) { p.Tools["tool"] = "shell" }, e.CodeEngineCapability},
		{"unselected exemption", func(p *QualificationSmokePolicy) {
			p.NonToolOutputs = []QualificationNonToolOutput{{Output: "other", Kind: OutputOCI, Components: []string{"image"}, Reason: "deployment-image"}}
		}, e.CodeReleaseIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, _, _, policy := nativeSmokeScopeFixture()
			policy.Smoke.Workflow = ""
			tc.edit(policy.Smoke)
			scope, err := toolSmokePlanScope(&plan, *policy.Smoke)
			assertCode(t, err, tc.code)
			if scope != nil {
				t.Fatal("failed admission returned partial execution scope")
			}
		})
	}
}

func TestLocalSmokeMembersRetainHostedPayloadBindings(t *testing.T) {
	plan, checks, bundles, policy := nativeSmokeScopeFixture()
	hosted, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Smoke.Workflow = ""
	planned, err := toolSmokePlanScope(&plan, *policy.Smoke)
	if err != nil {
		t.Fatal(err)
	}
	local, err := deriveToolSmokeMembers(planned, checks.Outputs, bundles)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(local, hosted) {
		t.Fatal("local scope changed held member identities or expected observations")
	}
	checks.Outputs[0].BuildDigest = nativeSmokeTestDigest("9")
	_, err = deriveToolSmokeMembers(planned, checks.Outputs, bundles)
	assertCode(t, err, e.CodeReleaseIncomplete)
}

func TestLocalSmokeProducerFormats(t *testing.T) {
	for _, format := range []string{"host-package", "raw", "tarball", "oci-image", "disk-image", ""} {
		t.Run(format, func(t *testing.T) {
			plan, checks, bundles, policy := nativeSmokeScopeFixture()
			plan.Builds[0].Graph.Nodes[0].OutputType = format
			members, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
			if format != "host-package" && format != "raw" {
				assertCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil || len(members) != 1 {
				t.Fatalf("members=%v error=%v", members, err)
			}
			bundle := bundles["cli"]
			bundle.Entries[0].Mode = 0o644
			bundles["cli"] = bundle
			_, err = deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
			assertCode(t, err, e.CodeEngineCapability)
		})
	}
}
