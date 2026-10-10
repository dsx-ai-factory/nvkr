// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"encoding/json"
	"testing"
)

func TestEvidenceEnumsHaveStrictJSONContract(t *testing.T) {
	var gap EvidenceGapDeclaration
	if err := json.Unmarshal([]byte(`{"artifactDigest":"`+digest('a')+`","missingTypes":["sbom"],"reason":"evidence:none"}`), &gap); err != nil {
		t.Fatal(err)
	}
	if gap.Reason != EvidenceNone || len(gap.MissingTypes) != 1 || gap.MissingTypes[0] != EvidenceSBOM {
		t.Fatalf("closed evidence enum failed round trip: %+v", gap)
	}
	if err := json.Unmarshal([]byte(`{"artifactDigest":"`+digest('a')+`","missingTypes":["raw-report"],"reason":"trust-me"}`), &gap); err == nil {
		t.Fatal("unknown string values must not become evidence or waiver authority")
	}
}
