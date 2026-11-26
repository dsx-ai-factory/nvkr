// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func TestAuthenticateRootVerifiesOnlyTheRootAndReadsItsClaims(t *testing.T) {
	invocation := qualificationSourceFixture().value.InvocationID
	planDigest := "sha256:" + strings.Repeat("c", 64)
	fixture := newExactInputFixture(t).withReleaseMetadata(t, func(m *delivery.ReleaseMetadata) {
		m.InvocationID = invocation
		m.PlanDigest = planDigest
	})
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err := delivery.ExportMetadata(t.Context(), fixture.layout, metadata); err != nil {
		t.Fatal(err)
	}
	verifier := &authenticityVerifier{}
	consumer := &Consumer{verifier: verifier, inspection: InspectionOptions{LayoutPath: metadata}}

	claims, err := consumer.authenticateRoot(t.Context(), fixture.reference, authenticityPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if claims.InvocationID != invocation || claims.PlanDigest != planDigest {
		t.Fatalf("claims = %+v, want the metadata's invocation and plan digest", claims)
	}
	if verifier.calls != 1 || verifier.bundles != 0 {
		t.Fatalf("verified %d subjects and %d bundles, want only the root", verifier.calls, verifier.bundles)
	}
	if len(claims.Outputs) == 0 {
		t.Fatal("the root's metadata lists outputs, and none was returned")
	}
	for _, o := range claims.Outputs {
		if o.Name == "" || o.Reference == "" || !canonical.IsDigest(o.Digest) {
			t.Errorf("output = %+v, want a name, a reference and a digest", o)
		}
	}
}

func TestRootOutputsRefusesWhatAPullByDigestCannotUse(t *testing.T) {
	good := delivery.PublicationOutput{Output: delivery.Output{Name: "a", Kind: "oci", Reference: "r/a@sha256:" + strings.Repeat("a", 64), Digest: "sha256:" + strings.Repeat("a", 64), OS: "linux", Arch: "arm64", Components: []string{"c"}}}
	tests := []struct {
		name   string
		mutate func(*delivery.PublicationOutput)
	}{
		{"no name", func(o *delivery.PublicationOutput) { o.Name = "" }},
		{"no reference", func(o *delivery.PublicationOutput) { o.Reference = "" }},
		{"a digest that is not one", func(o *delivery.PublicationOutput) { o.Digest = "sha256:short" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := good
			tc.mutate(&bad)
			outputs, err := rootOutputs([]delivery.PublicationOutput{bad})
			if err == nil {
				t.Fatalf("outputs were returned: %+v", outputs)
			}
			assertCode(t, err, e.CodeProvenanceMissing)
		})
	}
	if _, err := rootOutputs([]delivery.PublicationOutput{good, good}); err == nil {
		t.Fatal("an output listed twice was accepted")
	} else {
		assertCode(t, err, e.CodeAmbiguous)
	}
	outputs, err := rootOutputs([]delivery.PublicationOutput{good})
	if err != nil || len(outputs) != 1 || outputs[0].Platform != "linux/arm64" || outputs[0].Components[0] != "c" {
		t.Fatalf("outputs = %+v, %v", outputs, err)
	}
}

func TestAuthenticateRootFailsClosed(t *testing.T) {
	fixture := newExactInputFixture(t).withReleaseMetadata(t, func(m *delivery.ReleaseMetadata) {
		m.InvocationID = qualificationSourceFixture().value.InvocationID
	})
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err := delivery.ExportMetadata(t.Context(), fixture.layout, metadata); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	wrongScope := authenticityPolicy()
	wrongScope.Scope = ScopeArtifactPublication

	tests := []struct {
		name     string
		ctx      context.Context
		consumer *Consumer
		policy   VerificationPolicy
		code     e.Code
	}{
		{"a nil context", nil, &Consumer{verifier: &authenticityVerifier{}, inspection: InspectionOptions{LayoutPath: metadata}}, authenticityPolicy(), e.CodeUsage},
		{"a canceled context", canceled, &Consumer{verifier: &authenticityVerifier{}, inspection: InspectionOptions{LayoutPath: metadata}}, authenticityPolicy(), e.CodeTimeout},
		{"a scope that is not authenticity", t.Context(), &Consumer{verifier: &authenticityVerifier{}, inspection: InspectionOptions{LayoutPath: metadata}}, wrongScope, e.CodeEngineCapability},
		{"no signature adapter", t.Context(), &Consumer{inspection: InspectionOptions{LayoutPath: metadata}}, authenticityPolicy(), e.CodeEngineCapability},
		{"a root whose signature fails", t.Context(), &Consumer{verifier: &authenticityVerifier{verifyError: e.New(e.CodeSignatureInvalid, "bad signature")}, inspection: InspectionOptions{LayoutPath: metadata}}, authenticityPolicy(), e.CodeSignatureInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := tc.consumer.authenticateRoot(tc.ctx, fixture.reference, tc.policy)
			if err == nil {
				t.Fatalf("claims were returned: %+v", claims)
			}
			assertCode(t, err, tc.code)
			if claims.PlanDigest != "" || claims.InvocationID != "" || claims.Outputs != nil {
				t.Fatalf("a failure returned claims: %+v", claims)
			}
		})
	}
}

// releasePlanFixture is a real compiled Plan, the handoff that carries it, and
// the release reference and digest that handoff must match.
type releasePlanFixture struct {
	reference  string
	raw        []byte
	origin     qualificationorigin.Description
	planDigest string
	plan       *factory.Plan
}

func newReleasePlanFixture(t *testing.T) releasePlanFixture {
	t.Helper()
	_, handoff, plan := qualificationHandoffFixture(t)
	digest, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	return releasePlanFixture{reference: handoff.Candidate.Reference, raw: qualificationHandoffJSON(t, handoff),
		origin: handoff.Source, planDigest: digest, plan: plan}
}

// planProbe records what the private callbacks were asked to do.
type planProbe struct {
	rootCalls, openCalls, closes int
}

func (p *planProbe) root(claims releaseRootClaims, err error) authenticateRootFunc {
	return func(context.Context, string, VerificationPolicy) (releaseRootClaims, error) {
		p.rootCalls++
		return claims, err
	}
}

func (p *planProbe) handoff(origin qualificationorigin.Description, raw []byte, openErr, closeErr error) openHandoffFunc {
	return func(context.Context, qualificationorigin.RunLocator) (qualificationorigin.Description, []byte, func() error, error) {
		p.openCalls++
		if openErr != nil {
			return qualificationorigin.Description{}, nil, nil, openErr
		}
		return origin, raw, func() error { p.closes++; return closeErr }, nil
	}
}

func TestReadReleasePlanBindsTheHandoffToTheSignedPlanDigest(t *testing.T) {
	f := newReleasePlanFixture(t)
	probe := &planProbe{}
	claims := releaseRootClaims{PlanDigest: f.planDigest, InvocationID: f.origin.InvocationID,
		Outputs: []PublishedOutput{{Name: "a", Kind: "oci", Reference: "r/a@sha256:" + strings.Repeat("a", 64), Digest: "sha256:" + strings.Repeat("a", 64)}}}
	got, err := readReleaseBuildWithPolicy(t.Context(), f.reference, qualificationPolicyFixture(),
		probe.root(claims, nil),
		probe.handoff(f.origin, f.raw, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	plan := got.Plan
	if plan.SchemaVersion != ReleasePlanSchemaVersion || plan.Release != f.reference || plan.PlanDigest != f.planDigest || plan.Origin != f.origin {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Sources) == 0 {
		t.Fatal("no source was read from the Plan")
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Name != "a" {
		t.Fatalf("outputs = %+v, want the root's, kept apart from the Plan's sources", got.Outputs)
	}
	if probe.rootCalls != 1 || probe.openCalls != 1 || probe.closes != 1 {
		t.Fatalf("root=%d open=%d closes=%d, want one each", probe.rootCalls, probe.openCalls, probe.closes)
	}
}

func TestReadReleasePlanFailsClosed(t *testing.T) {
	f := newReleasePlanFixture(t)
	otherDigest := "sha256:" + strings.Repeat("f", 64)
	foreign := f.origin
	foreign.RunID = "999"
	foreignInvocation := "https://github.com/other/nvkr/actions/runs/42/attempts/2"

	valid := releaseRootClaims{PlanDigest: f.planDigest, InvocationID: f.origin.InvocationID}
	tests := []struct {
		name      string
		reference string
		claims    releaseRootClaims
		rootErr   error
		origin    qualificationorigin.Description
		raw       []byte
		openErr   error
		closeErr  error
		code      e.Code
		opened    bool
	}{
		{"the root does not authenticate", f.reference, releaseRootClaims{}, e.New(e.CodeUntrustedSigner, "wrong identity"), f.origin, f.raw, nil, nil, e.CodeUntrustedSigner, false},
		{"the root names no source attempt", f.reference, releaseRootClaims{PlanDigest: f.planDigest}, nil, f.origin, f.raw, nil, nil, e.CodeProvenanceMissing, false},
		{"the root names no plan digest", f.reference, releaseRootClaims{InvocationID: f.origin.InvocationID}, nil, f.origin, f.raw, nil, nil, e.CodeProvenanceMissing, false},
		{"the root names an attempt with no number", f.reference, releaseRootClaims{PlanDigest: f.planDigest, InvocationID: "https://github.com/example/nvkr/actions/runs/42"}, nil, f.origin, f.raw, nil, nil, e.CodeProvenanceMissing, false},
		{"the root names another repository", f.reference, releaseRootClaims{PlanDigest: f.planDigest, InvocationID: foreignInvocation}, nil, f.origin, f.raw, nil, nil, e.CodeUntrustedSigner, false},
		{"the attempt cannot be opened", f.reference, valid, nil, f.origin, nil, e.New(e.CodeFetchFailed, "no handoff"), nil, e.CodeFetchFailed, true},
		{"the handoff is empty", f.reference, valid, nil, f.origin, nil, nil, nil, e.CodeProvenanceMissing, true},
		{"the handoff is not a handoff", f.reference, valid, nil, f.origin, []byte(`{"unknown":1}`), nil, nil, e.CodeProvenanceMissing, true},
		{"the handoff is from another attempt", f.reference, valid, nil, foreign, f.raw, nil, nil, e.CodeVerifyFailed, true},
		{"the handoff binds another release", f.reference + "x", valid, nil, f.origin, f.raw, nil, nil, e.CodeUnverifiedRef, false},
		{"the Plan is not the one the release states", f.reference, releaseRootClaims{PlanDigest: otherDigest, InvocationID: f.origin.InvocationID}, nil, f.origin, f.raw, nil, nil, e.CodeVerifyFailed, true},
		{"the source will not close", f.reference, valid, nil, f.origin, f.raw, nil, errors.New("held"), e.CodeGateUnmet, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe := &planProbe{}
			got, err := readReleaseBuildWithPolicy(t.Context(), tc.reference, qualificationPolicyFixture(),
				probe.root(tc.claims, tc.rootErr), probe.handoff(tc.origin, tc.raw, tc.openErr, tc.closeErr))
			if err == nil {
				t.Fatalf("a description was returned: %+v", got)
			}
			assertCode(t, err, tc.code)
			if got.Plan.PlanDigest != "" || len(got.Plan.Sources) != 0 || len(got.Outputs) != 0 {
				t.Fatalf("a failure returned a description: %+v", got)
			}
			if opened := probe.openCalls > 0; opened != tc.opened {
				t.Fatalf("the attempt was opened = %v, want %v", opened, tc.opened)
			}
			if probe.openCalls == 1 && tc.openErr == nil && probe.closes != 1 {
				t.Fatalf("the source was closed %d times, want once", probe.closes)
			}
		})
	}
}

func TestReadReleasePlanAdmitsBeforeWork(t *testing.T) {
	f := newReleasePlanFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		reference string
		code      e.Code
	}{
		{"a nil context", nil, f.reference, e.CodeUsage},
		{"a canceled context", canceled, f.reference, e.CodeTimeout},
		{"a mutable reference", t.Context(), "registry.example.test/product:latest", e.CodeUnverifiedRef},
		{"a URL", t.Context(), "https://" + f.reference, e.CodeUnverifiedRef},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe := &planProbe{}
			_, err := readReleaseBuildWithPolicy(tc.ctx, tc.reference, qualificationPolicyFixture(),
				probe.root(releaseRootClaims{}, nil), probe.handoff(f.origin, f.raw, nil, nil))
			assertCode(t, err, tc.code)
			if probe.rootCalls != 0 || probe.openCalls != 0 {
				t.Fatal("admission failed after work started")
			}
		})
	}
	t.Run("missing dependencies", func(t *testing.T) {
		_, err := readReleaseBuildWithPolicy(t.Context(), f.reference, qualificationPolicyFixture(), nil, nil)
		assertCode(t, err, e.CodeInternal)
	})
}

func TestPlanSourcesReadsTheTagCommitAndComponentsEachBuildResolved(t *testing.T) {
	f := newReleasePlanFixture(t)
	sources, err := planSources(f.plan)
	if err != nil {
		t.Fatal(err)
	}
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	tagged, covered := 0, map[string]bool{}
	for i, source := range sources {
		if source.Repo == "" || !commit.MatchString(source.Commit) || len(source.Components) == 0 {
			t.Errorf("source %d = %+v, want a repository, a full commit and its components", i, source)
		}
		if source.Tag != "" {
			tagged++
		}
		if !slices.IsSorted(source.Components) {
			t.Errorf("source %d components %v are not sorted", i, source.Components)
		}
		if i > 0 && sources[i-1].Repo == source.Repo && sources[i-1].Commit == source.Commit && sources[i-1].Tag >= source.Tag {
			t.Errorf("sources %d and %d repeat or are out of order", i-1, i)
		}
		// Each value must be what the Plan's own catalog snapshot records for
		// that component, which is a different path from the one under test.
		for _, name := range source.Components {
			covered[name] = true
			c, ok := f.plan.CatalogSnapshot.Components[name]
			if !ok {
				t.Errorf("source %d names component %q, which the catalog snapshot lacks", i, name)
				continue
			}
			if c.Repo != source.Repo || c.Tag != source.Tag || c.Commit != source.Commit || c.TreeHash != source.TreeHash {
				t.Errorf("source %d disagrees with the catalog snapshot for %q: %+v versus repo=%q tag=%q commit=%q", i, name, source, c.Repo, c.Tag, c.Commit)
			}
		}
	}
	if tagged == 0 {
		t.Fatal("no source carries a tag, so the tag lookup proves nothing")
	}
	// Every component the Plan built from a pinned source must be represented.
	for _, build := range f.plan.Builds {
		for _, name := range build.Components {
			if c := f.plan.CatalogSnapshot.Components[name]; c.FromSource && c.Commit != "" && !covered[name] {
				t.Errorf("component %q is built from a pinned source and is missing from the sources", name)
			}
		}
	}
}

func TestPlanSourcesRefusesAPlanWithNothingToRead(t *testing.T) {
	f := newReleasePlanFixture(t)
	noBuilds := *f.plan
	noBuilds.Builds = nil
	if _, err := planSources(&noBuilds); err == nil {
		t.Error("a Plan with no builds was read")
	} else {
		assertCode(t, err, e.CodeProvenanceMissing)
	}
}

func TestPlanSourcesRefusesTwoTreesForOneCommit(t *testing.T) {
	f := newReleasePlanFixture(t)
	conflicted := *f.plan
	conflicted.Builds = append([]factory.BuildPlan(nil), f.plan.Builds...)
	var edited bool
	for i := range conflicted.Builds {
		resolved := append([]catalog.NamedComponent(nil), conflicted.Builds[i].ResolvedComponents...)
		for j := range resolved {
			if resolved[j].FromSource && resolved[j].Commit != "" && !edited {
				// A second component pinned at the same commit, with another tree.
				twin := resolved[j]
				twin.Name += "-twin"
				twin.TreeHash = strings.Repeat("0", 40)
				resolved = append(resolved, twin)
				edited = true
			}
		}
		conflicted.Builds[i].ResolvedComponents = resolved
	}
	if !edited {
		t.Fatal("the fixture builds nothing from a pinned source")
	}
	if _, err := planSources(&conflicted); err == nil {
		t.Fatal("two tree hashes for one commit were accepted")
	} else {
		assertCode(t, err, e.CodeVerifyFailed)
	}
}
