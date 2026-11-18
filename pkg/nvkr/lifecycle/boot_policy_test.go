// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func selectedBootPolicyFixture() VerificationPolicy {
	return VerificationPolicy{Lane: LanePipeline, Scope: ScopeSelectedBootArtifacts,
		IdentityRegexp: "^https://github.com/example/nvkr/.*$", Issuer: "https://token.actions.githubusercontent.com"}
}

func TestSelectedBootPolicySeparatesScopeBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*VerificationPolicy)
		code e.Code
	}{
		{name: "selected"},
		{name: "strict disk evidence", edit: func(p *VerificationPolicy) { p.MinimumDiskEvidence = DiskEvidenceStrict }},
		{name: "authenticity", edit: func(p *VerificationPolicy) { p.Scope = ScopeAuthenticity }, code: e.CodeEngineCapability},
		{name: "whole publication", edit: func(p *VerificationPolicy) { p.Scope = ScopeArtifactPublication }, code: e.CodeEngineCapability},
		{name: "release", edit: func(p *VerificationPolicy) { p.Scope = ScopeRelease }, code: e.CodeEngineCapability},
		{name: "qualified release", edit: func(p *VerificationPolicy) { p.Lane, p.Scope = LaneQualified, ScopeRelease }, code: e.CodeEngineCapability},
		{name: "missing signer", edit: func(p *VerificationPolicy) { p.IdentityRegexp = "" }, code: e.CodeTrustPolicyMissing},
		{name: "unknown evidence", edit: func(p *VerificationPolicy) { p.MinimumDiskEvidence = "checksum-only" }, code: e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := selectedBootPolicyFixture()
			if tc.edit != nil {
				tc.edit(&policy)
			}
			err := admitSelectedBootPolicy(policy)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireBootPolicyCode(t, err, tc.code)
			}
		})
	}
	policy := selectedBootPolicyFixture()
	var consumer *Consumer
	verified, err := consumer.Verify(t.Context(), "ghcr.io/example/release@sha256:"+strings.Repeat("a", 64), policy)
	requireBootPolicyCode(t, err, e.CodeEngineCapability)
	if verified != nil {
		t.Fatal("selected scope returned a full-publication capability")
	}
	selectedDigest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	policy.Scope = ScopeArtifactPublication
	fullDigest, err := policy.Digest()
	if err != nil || selectedDigest == fullDigest {
		t.Fatal("selected scope aliases whole-publication policy identity", err)
	}
}

func requireBootPolicyCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
