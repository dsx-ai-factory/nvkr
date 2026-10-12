// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

// evidenceTierDefinition is the ONE file allowed to spell the tier tokens. Every
// other site must go through the typed constants.
const evidenceTierDefinition = "evidence.go"

// TestEvidenceTierTokensAppearOnlyInTheEnumDefinition is the guard that keeps the
// tier from silently re-fragmenting into copies of a string. A qualification claim
// that is retyped at each site is one typo away from a document no gate matches,
// and the typo is invisible in review because both spellings "look right".
func TestEvidenceTierTokensAppearOnlyInTheEnumDefinition(t *testing.T) {
	tokens := []string{
		string(TierStaticUnqualified),
		string(TierPipelineAttested),
		string(TierTerminalObservation),
		string(TierLocalExecution),
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			name == evidenceTierDefinition || name == "evidence_test.go" {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, token := range tokens {
			if strings.Contains(string(raw), `"`+token+`"`) {
				t.Errorf("%s spells the evidence tier %q as a literal; use the EvidenceTier constant from %s",
					name, token, evidenceTierDefinition)
			}
		}
	}
	if scanned < 5 {
		t.Fatalf("scanned only %d package sources; the guard is not actually looking at the package", scanned)
	}
}

func TestEvidenceTierZeroValueIsTheLegacyTier(t *testing.T) {
	var unset EvidenceTier
	if unset.Normalize() != TierStaticUnqualified || unset.String() != string(TierStaticUnqualified) {
		t.Fatalf("zero-value tier normalized to %q, want %q", unset.Normalize(), TierStaticUnqualified)
	}
	if TierPipelineAttested.Normalize() != TierPipelineAttested {
		t.Fatal("a set tier must not be rewritten by normalization")
	}
}

func TestPlanTierIsAttestedOnlyForCosignSigning(t *testing.T) {
	if got := planTier(nil); got != TierStaticUnqualified {
		t.Fatalf("unsigned delivery tier = %q", got)
	}
	dryRun := &authored.Signing{Mode: SigningModeDryRun, Identity: "^local$", Issuer: "https://nvkr.dev/local"}
	if got := planTier(dryRun); got != TierStaticUnqualified {
		t.Fatalf("dry-run delivery tier = %q; a dry run signs nothing", got)
	}
	cosign := &authored.Signing{Mode: SigningModeCosign, Identity: "^local$", Issuer: "https://nvkr.dev/local"}
	if got := planTier(cosign); got != TierPipelineAttested {
		t.Fatalf("cosign delivery tier = %q", got)
	}
	if err := TierPipelineAttested.ValidatePlanTier(dryRun); err == nil {
		t.Fatal("a dry-run plan was allowed to claim pipeline-attested evidence")
	}
	if err := TierPipelineAttested.ValidatePlanTier(nil); err == nil {
		t.Fatal("an unsigned plan was allowed to claim pipeline-attested evidence")
	}
	if err := EvidenceTier("qualified-release").ValidatePlanTier(cosign); err == nil {
		t.Fatal("an unknown qualification tier was accepted")
	}
}
