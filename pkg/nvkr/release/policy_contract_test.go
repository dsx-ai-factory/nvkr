// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"bytes"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func contractPromotionPolicy() PromotionPolicy {
	return PromotionPolicy{SchemaVersion: PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: digest('8'), ConformancePolicyDigest: digest('9'), EffectiveEpoch: 1, ExpiresEpoch: 20,
		Rules: []PromotionRule{{ArtifactKind: ArtifactHostPackage, Evidence: []EvidenceRequirement{{Type: EvidenceSBOM, WaivableReasons: []EvidenceGapReason{EvidenceNone, EvidenceNotDeclared}}}}}}
}

func TestPromotionPolicyInvalidRulesCannotProduceSigningBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PromotionPolicy)
		code e.Code
	}{
		{"compatibility pin", func(p *PromotionPolicy) { p.CompatibilityPolicyDigest = "latest" }, e.CodeUnverifiedRef},
		{"conformance pin", func(p *PromotionPolicy) { p.ConformancePolicyDigest = "latest" }, e.CodeUnverifiedRef},
		{"effective time", func(p *PromotionPolicy) { p.EffectiveEpoch = 0 }, e.CodeGateUnmet},
		{"empty lifetime", func(p *PromotionPolicy) { p.ExpiresEpoch = p.EffectiveEpoch }, e.CodeGateUnmet},
		{"empty rules", func(p *PromotionPolicy) { p.Rules = nil }, e.CodeInvalidCatalog},
		{"unknown artifact kind", func(p *PromotionPolicy) { p.Rules[0].ArtifactKind = ArtifactKind(99) }, e.CodeInvalidCatalog},
		{"unknown evidence", func(p *PromotionPolicy) { p.Rules[0].Evidence[0].Type = EvidenceType(99) }, e.CodeInvalidCatalog},
		{"duplicate evidence", func(p *PromotionPolicy) { p.Rules[0].Evidence = append(p.Rules[0].Evidence, p.Rules[0].Evidence[0]) }, e.CodeInvalidCatalog},
		{"unknown reason", func(p *PromotionPolicy) { p.Rules[0].Evidence[0].WaivableReasons = []EvidenceGapReason{99} }, e.CodeInvalidCatalog},
		{"duplicate reason", func(p *PromotionPolicy) {
			p.Rules[0].Evidence[0].WaivableReasons = []EvidenceGapReason{EvidenceNone, EvidenceNone}
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := contractPromotionPolicy()
			if err := policy.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.edit(&policy)
			if err := policy.Validate(); code(err) != tc.code {
				t.Fatalf("validation = %v; want %s", err, tc.code)
			}
			if raw, err := policy.Marshal(); len(raw) != 0 || code(err) != tc.code {
				t.Fatalf("signing bytes = %q, %v", raw, err)
			}
			if identity, err := policy.Digest(); identity != "" || code(err) != tc.code {
				t.Fatalf("identity = %q, %v", identity, err)
			}
		})
	}
}

func TestPromotionPolicyCanonicalizationPreservesAuthoredCollections(t *testing.T) {
	policy := contractPromotionPolicy()
	policy.Rules = append([]PromotionRule{{ArtifactKind: ArtifactOCIImage, Evidence: []EvidenceRequirement{{Type: EvidenceProvenance}, {Type: EvidenceSBOM}}}}, policy.Rules...)
	before := contractPromotionPolicy()
	before.Rules = append([]PromotionRule{{ArtifactKind: ArtifactOCIImage, Evidence: []EvidenceRequirement{{Type: EvidenceProvenance}, {Type: EvidenceSBOM}}}}, before.Rules...)
	raw, err := policy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy, before) {
		t.Fatal("canonicalization mutated authored rule order")
	}
	normalized := policy.normalized()
	canonical, err := normalized.Marshal()
	if err != nil || !bytes.Equal(raw, canonical) {
		t.Fatalf("canonicalization changed bytes: %v", err)
	}
	otherIdentity, err := normalized.Digest()
	if err != nil || identity != otherIdentity {
		t.Fatalf("order changed policy identity: %v", err)
	}
	normalized.Rules[0].Evidence[0].WaivableReasons[0] = EvidenceGapReason(99)
	if !reflect.DeepEqual(policy, before) {
		t.Fatal("normalized policy shares nested mutable waiver rules")
	}
}
