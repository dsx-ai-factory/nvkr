// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func compositeReleaseFixtures(t *testing.T) (composite, member exactInputFixture) {
	t.Helper()
	compositeLifecycleFixture(t, nil)
	alpha, err := CompileFiles(t.Context(), "defs/alpha.yaml", compositeTestEnv, "composite-test")
	if err != nil {
		t.Fatal(err)
	}
	return compositeReleaseFixture(t), exactInputFixtureForPlan(t, alpha.Definition, alpha.Environment, alpha)
}

// compositeReleaseFixture publishes the composite at compositeTestPath.
func compositeReleaseFixture(t *testing.T) exactInputFixture {
	t.Helper()
	plan, err := CompileFiles(t.Context(), compositeTestPath, compositeTestEnv, "composite-test")
	if err != nil {
		t.Fatal(err)
	}
	return exactInputFixtureForPlanAndInvocation(t, plan.Definition, plan.Environment, plan, "", func(request *delivery.PublicationRequest) {
		request.Release.DefinitionDigest, request.Release.Composition = plan.DefinitionDigest, plan.Composition.Clone()
	})
}

// TestCompositeReleaseCarriesVariants keeps a declared variant through
// publication metadata and the verified consumer view, and refuses to publish
// metadata that drops the declaration under the same composite identity.
func TestCompositeReleaseCarriesVariants(t *testing.T) {
	declareLifecycleVariant(t, compositeLifecycleFixture(t, nil))
	release := compositeReleaseFixture(t)
	verified, _, _, plan := release.open(t)
	defer func() { _ = verified.Close() }()
	record, err := verified.Composition()
	if err != nil || len(record.Variants) != 1 || !record.Equal(plan.Composition) {
		t.Fatalf("authenticated composition = %+v, %v", record, err)
	}
	record.Variants[0].Members[0] = "gamma"
	if again, err := verified.Composition(); err != nil || !again.Equal(plan.Composition) {
		t.Fatal("verified Release shares its declared variants")
	}
	expectation, err := PrepareExactInputs(release.definition, release.environment, release.plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verified.RequireExactInputs(expectation); err != nil {
		t.Fatalf("composite expectation against its own Release: %v", err)
	}
	// RequireMembers checks member pins only; the variants stay in the record.
	if err := verified.RequireMembers(plan.Composition.Members); err != nil {
		t.Fatal(err)
	}
	// Publication refuses metadata that drops the declaration under the same composite identity.
	request := release.request
	metadata := *request.Release
	metadata.Composition = metadata.Composition.Clone()
	metadata.Composition.Variants = nil
	request.Release = &metadata
	_, err = delivery.BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "publication"), request)
	assertCode(t, err, e.CodeVerifyFailed)
}

func TestCompositeConsumerExpectations(t *testing.T) {
	composite, member := compositeReleaseFixtures(t)
	expect := func(fixture exactInputFixture) *ExactInputExpectation {
		expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
		if err != nil {
			t.Fatal(err)
		}
		return expectation
	}
	// The effective Definition compiled as a plain Definition is a different form.
	effective, err := Compile(t.Context(), compositeTestPath, composite.definition, compositeTestEnv, composite.environment, "composite-test")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Composition != nil {
		t.Fatal("plain compilation of the effective Definition produced a composition record")
	}
	effectiveExpectation, err := PrepareExactInputs(composite.definition, composite.environment, effective)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		release     exactInputFixture
		expectation *ExactInputExpectation
		code        e.Code
	}{
		{"composite expectation matches composite Release", composite, expect(composite), ""},
		{"member expectation against composite Release", composite, expect(member), e.CodeVerifyFailed},
		{"effective plain expectation against composite Release", composite, effectiveExpectation, e.CodeVerifyFailed},
		{"composite expectation against member Release", member, expect(composite), e.CodeVerifyFailed},
		{"member expectation matches member Release", member, expect(member), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verified, _, _, _ := tc.release.open(t)
			defer func() { _ = verified.Close() }()
			receipt, err := verified.RequireExactInputs(tc.expectation)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			description, err := receipt.Description()
			if err != nil || description.DefinitionDigest != tc.expectation.prepared.DefinitionDigest {
				t.Fatalf("receipt = %+v, %v", description, err)
			}
		})
	}

	// Attribution that is structurally valid but not the Plan's record is
	// refused when the consumer narrows to its exact composite expectation.
	swapped := composite.withReleaseMetadata(t, func(metadata *delivery.ReleaseMetadata) {
		record := metadata.Composition.Clone()
		record.Outputs[0].Member, record.Outputs[1].Member = record.Outputs[1].Member, record.Outputs[0].Member
		metadata.Composition = record
	})
	verified, err := swapped.consumer(t, nil).Verify(t.Context(), swapped.reference, exactInputPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = verified.Close() }()
	_, err = verified.RequireExactInputs(expect(composite))
	assertCode(t, err, e.CodeVerifyFailed)
}

func TestCompositeConsumerMemberSet(t *testing.T) {
	composite, member := compositeReleaseFixtures(t)
	verified, _, _, plan := composite.open(t)
	defer func() { _ = verified.Close() }()
	members := plan.Composition.Members
	record, err := verified.Composition()
	if err != nil || !record.Equal(plan.Composition) {
		t.Fatalf("authenticated composition = %+v, %v", record, err)
	}
	for _, tc := range []struct {
		name     string
		required []MemberIdentity
		code     e.Code
	}{
		{"exact set in any order", []MemberIdentity{members[1], members[0]}, ""},
		{"missing member", members[:1], e.CodeVerifyFailed},
		{"extra member", append(append([]MemberIdentity(nil), members...), MemberIdentity{Name: "gamma", Digest: members[0].Digest}), e.CodeVerifyFailed},
		{"different member digest", []MemberIdentity{members[0], {Name: members[1].Name, Digest: members[0].Digest}}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verified.RequireMembers(tc.required)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCode(t, err, tc.code)
		})
	}
	plain, _, _, _ := member.open(t)
	defer func() { _ = plain.Close() }()
	assertCode(t, plain.RequireMembers(members), e.CodeVerifyFailed)
	if record, err := plain.Composition(); err != nil || record != nil {
		t.Fatalf("plain Release exposes a composition record: %+v, %v", record, err)
	}
	_, err = plain.MemberView("alpha")
	assertCode(t, err, e.CodeVerifyFailed)
}

func TestCompositeMemberViewGrantsNothing(t *testing.T) {
	composite, _ := compositeReleaseFixtures(t)
	verified, _, _, plan := composite.open(t)
	defer func() { _ = verified.Close() }()
	view, err := verified.MemberView("beta")
	if err != nil || view.Member != "beta" || len(view.Artifacts) != 1 || view.Artifacts[0].Name != "beta-tools" {
		t.Fatalf("member view = %+v, %v", view, err)
	}
	_, err = verified.MemberView("gamma")
	assertCode(t, err, e.CodeVerifyFailed)

	// The view is a copy: changing it cannot change the verified Release.
	view.Artifacts[0].Digest = "sha256:" + repeat("0", 64)
	release, err := verified.Description()
	if err != nil || release.Artifacts[1].Digest == view.Artifacts[0].Digest {
		t.Fatal("member view aliases the verified Release")
	}
	// Restoring authority from descriptions is impossible: an unissued
	// capability, even one carrying the member's artifacts, is refused.
	forged := &VerifiedRelease{release: Release{Subject: view.ReleaseSubject, Artifacts: view.Artifacts}}
	expectation, err := PrepareExactInputs(composite.definition, composite.environment, plan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = forged.RequireExactInputs(expectation)
	assertCode(t, err, e.CodeGateUnmet)
	assertCode(t, forged.RequireMembers(plan.Composition.Members), e.CodeGateUnmet)
}

// An output-local publication carries the complete composition record with a
// subset of outputs; Release issuance requires every composed output.
func TestCompositeReleaseIssuanceRequiresEveryOutput(t *testing.T) {
	composite, _ := compositeReleaseFixtures(t)
	verified, _, _, plan := composite.open(t)
	defer func() { _ = verified.Close() }()
	description, err := verified.publication.Description(verified.release.Subject)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireCompleteComposition(description); err != nil {
		t.Fatal(err)
	}
	if len(description.Outputs) != len(plan.Composition.Outputs) || len(description.Outputs) < 2 {
		t.Fatalf("fixture does not carry every composed output: %d", len(description.Outputs))
	}
	description.Outputs = description.Outputs[:1]
	assertCode(t, requireCompleteComposition(description), e.CodeReleaseIncomplete)
}
