// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

func TestArtifactCheckReceiptReleaseMembersUseVerifiedProvenance(t *testing.T) {
	request, consumer, verifier := newQualificationFixture(t)
	receipt, report, err := consumer.checkQualification(t.Context(), request, verifier.source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	var predicate provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(verifier.predicate, []byte{'\n'}), &predicate); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(verifier.predicate)
	members, err := receipt.ReleaseMembers()
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != len(report.Outputs) {
		t.Fatalf("members=%+v outputs=%+v", members, report.Outputs)
	}
	for i, member := range members {
		if member.Output != report.Outputs[i].Name || member.SourceLane != sourceLanes[predicate.BuildDefinition.BuildType] ||
			member.ProvenanceDigest != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Fatalf("member %d does not project verified provenance: %+v", i, member)
		}
	}
	members[0].SourceLane = SourceLaneAcquired + "-forged"
	again, err := receipt.ReleaseMembers()
	if err != nil || again[0].SourceLane == members[0].SourceLane {
		t.Fatalf("returned members alias receipt state: %+v (%v)", again, err)
	}
	if err := receipt.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = receipt.ReleaseMembers()
	assertCode(t, err, e.CodeGateUnmet)
	_, err = (&ArtifactCheckReceipt{}).ReleaseMembers()
	assertCode(t, err, e.CodeGateUnmet)
}

func TestQualificationReleaseMembersMapClosedSourceLanes(t *testing.T) {
	request, _, verifier := newQualificationFixture(t)
	var base provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(verifier.predicate, []byte{'\n'}), &base); err != nil {
		t.Fatal(err)
	}
	predicateWith := func(t *testing.T, buildType string) []byte {
		t.Helper()
		predicate := base
		predicate.BuildDefinition.BuildType = buildType
		raw, err := canonical.Marshal(predicate)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	output := ArtifactCheckOutput{Name: "cli", Reference: request.Predicates[1].Reference}
	for _, tc := range []struct {
		name      string
		predicate func(t *testing.T) []byte
		lane      string
		code      e.Code
	}{
		{name: "hermetic", predicate: func(t *testing.T) []byte { return predicateWith(t, provenance.BuildTypeHermeticBuild) }, lane: SourceLaneHermeticBuild},
		{name: "wrapped", predicate: func(t *testing.T) []byte { return predicateWith(t, provenance.BuildTypeWrappedBuild) }, lane: SourceLaneWrappedBuild},
		{name: "acquired", predicate: func(t *testing.T) []byte { return predicateWith(t, provenance.BuildTypeAcquired) }, lane: SourceLaneAcquired},
		{name: "reproduction build type", predicate: func(t *testing.T) []byte { return predicateWith(t, provenance.BuildTypeOCIHermetic) }, code: e.CodeProvenanceMissing},
		{name: "malformed", predicate: func(*testing.T) []byte { return []byte(`{"buildDefinition":`) }, code: e.CodeProvenanceMissing},
		{name: "missing", code: e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			predicates := map[string][]byte{}
			if tc.predicate != nil {
				predicates[output.Reference] = tc.predicate(t)
			}
			members, err := qualificationReleaseMembers([]ArtifactCheckOutput{output}, predicates)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != 1 || members[0].Output != output.Name || members[0].SourceLane != tc.lane {
				t.Fatalf("members=%+v", members)
			}
		})
	}
}
