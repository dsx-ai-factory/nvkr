// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestPublicationBinding(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	reference := "127.0.0.1:5000/quick/releases@" + digest
	predicate := []byte(`{"buildDefinition":{}}`)
	for _, tc := range []struct {
		name   string
		change func(*sign.SigningRecord, *sign.VerificationRecord)
		valid  bool
	}{
		{"matching unsigned records", func(*sign.SigningRecord, *sign.VerificationRecord) {}, true},
		{"changed predicate binding", func(s *sign.SigningRecord, _ *sign.VerificationRecord) { s.PredicateSHA256 = digest }, false},
		{"wrong publication", func(s *sign.SigningRecord, _ *sign.VerificationRecord) { s.Ref += "x" }, false},
		{"wrong verification subject", func(_ *sign.SigningRecord, v *sign.VerificationRecord) { v.Digest = "" }, false},
		{"wrong identity", func(_ *sign.SigningRecord, v *sign.VerificationRecord) { v.Identity = "other" }, false},
		{"real signing claim", func(s *sign.SigningRecord, _ *sign.VerificationRecord) { s.DryRun = false }, false},
		{"false signature claim", func(_ *sign.SigningRecord, v *sign.VerificationRecord) { v.SignatureVerified = true }, false},
		{"false attestation claim", func(_ *sign.SigningRecord, v *sign.VerificationRecord) { v.AttestationVerified = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := sign.SigningRecord{Ref: reference, Digest: digest, DryRun: true,
				Identity: "^quick-e2e-unsigned$", Issuer: "https://nvkr.dev/local",
				PredicateType: sign.ProvenancePredicateType, PredicateSHA256: pipeline.FileDigest(predicate)}
			v := sign.VerificationRecord{Ref: reference, Digest: digest, DryRun: true,
				Identity: s.Identity, Issuer: s.Issuer, PredicateType: s.PredicateType}
			tc.change(&s, &v)
			if err := checkPublication(reference, predicate, s, v); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}
