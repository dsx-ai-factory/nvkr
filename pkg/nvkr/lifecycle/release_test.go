// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type releaseVerifier struct {
	calls  int
	record sign.VerificationRecord
	err    error
}

func (*releaseVerifier) SignAndAttest(context.Context, sign.Subject, []byte) (sign.SigningRecord, error) {
	return sign.SigningRecord{}, nvkrerr.New(nvkrerr.CodeUsage, "verification-only test adapter")
}

func (verifier *releaseVerifier) Verify(_ context.Context, _ sign.Subject, _ sign.VerifyPolicy) (sign.VerificationRecord, error) {
	verifier.calls++
	return verifier.record, verifier.err
}

func TestConsumerVerifiesExactSignedArtifactPublicationClosure(t *testing.T) {
	layout, digest := testPublicationLayout(t)
	reference := "registry.example.test/product@" + digest
	verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: digest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	consumer := &Consumer{verifier: verifier, materialize: func(ctx context.Context, got string) (*delivery.Publication, error) {
		if got != reference {
			t.Fatalf("reference = %s", got)
		}
		return delivery.OpenPublication(ctx, layout, digest)
	}}
	policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
		IdentityRegexp: "^builder$", Issuer: "issuer"}
	verified, err := consumer.Verify(context.Background(), reference, policy)
	if err != nil {
		t.Fatal(err)
	}
	description, err := verified.Description()
	if err != nil {
		t.Fatal(err)
	}
	if description.Subject != reference || description.Scope != ScopeArtifactPublication || len(description.Artifacts) != 1 || verifier.calls != 1 {
		t.Fatalf("description=%+v calls=%d", description, verifier.calls)
	}
	if description.Artifacts[0].Reference != "registry.example.test/tools@"+description.Artifacts[0].Digest {
		t.Fatal("verified view omits exact artifact reference", description)
	}
	if err := verified.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := verified.Description(); err == nil {
		t.Fatal("closed release capability remained usable")
	} else {
		assertCode(t, err, nvkrerr.CodeGateUnmet)
	}
}

func TestConsumerRejectsNoncanonicalReferenceBeforeAdapters(t *testing.T) {
	verifier := &releaseVerifier{}
	materializeCalls := 0
	consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (*delivery.Publication, error) {
		materializeCalls++
		return nil, nvkrerr.New(nvkrerr.CodeInternal, "materializer must not run")
	}}
	policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
		IdentityRegexp: "^builder$", Issuer: "issuer"}
	for _, reference := range []string{
		"ubuntu@sha256:" + repeat("a", 64),
		"docker.io/library/ubuntu@sha256:" + repeat("a", 64),
		"ghcr.io/example/release:v1@sha256:" + repeat("a", 64),
	} {
		t.Run(reference, func(t *testing.T) {
			_, err := consumer.Verify(t.Context(), reference, policy)
			assertCode(t, err, nvkrerr.CodeUnverifiedRef)
		})
	}
	if verifier.calls != 0 || materializeCalls != 0 {
		t.Fatalf("noncanonical reference reached adapters: verifier=%d materializer=%d", verifier.calls, materializeCalls)
	}
}

func TestExactInputCapabilitiesMustBePackageIssued(t *testing.T) {
	verified := &VerifiedRelease{valid: true, publication: &delivery.Publication{}}
	if _, err := verified.RequireExactInputs(&ExactInputExpectation{}); err == nil {
		t.Fatal("caller-minted exact-input expectation was accepted")
	} else {
		assertCode(t, err, nvkrerr.CodeGateUnmet)
	}
	if _, err := (&ExactInputReceipt{}).Description(); err == nil {
		t.Fatal("caller-minted exact-input receipt was accepted")
	} else {
		assertCode(t, err, nvkrerr.CodeGateUnmet)
	}
	consumer := &Consumer{verifier: &releaseVerifier{}, materialize: func(context.Context, string) (*delivery.Publication, error) {
		t.Fatal("caller-minted expectation reached publication adapters")
		return nil, nil
	}}
	_, _, err := consumer.VerifyExactInputs(t.Context(), verified.release.Subject, VerificationPolicy{
		Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^builder$", Issuer: "issuer",
	}, &ExactInputExpectation{})
	assertCode(t, err, nvkrerr.CodeGateUnmet)
}

func TestVerifiedReleaseRequiresExactDefinitionEnvironmentAndPlan(t *testing.T) {
	fixture := newExactInputFixture(t)
	for _, test := range []struct {
		name           string
		mutateInputs   func(*testing.T, *Definition, *Environment, **factory.Plan)
		mutateVerified func(*testing.T, *VerifiedRelease)
		prepareCode    nvkrerr.Code
		verifyCode     nvkrerr.Code
	}{
		{name: "exact"},
		{name: "different definition", mutateInputs: func(_ *testing.T, definition *Definition, _ *Environment, _ **factory.Plan) {
			definition.Metadata.Name = "other-product"
		}, prepareCode: nvkrerr.CodeUnverifiedRef},
		{name: "different environment", mutateInputs: func(_ *testing.T, _ *Definition, environment *Environment, _ **factory.Plan) {
			environment.Metadata.Name = "other-environment"
		}, prepareCode: nvkrerr.CodeUnverifiedRef},
		{name: "plan environment inconsistency", mutateInputs: func(_ *testing.T, _ *Definition, _ *Environment, plan **factory.Plan) {
			copyPlan := **plan
			copyPlan.EnvironmentDigest = "sha256:" + repeat("9", 64)
			*plan = &copyPlan
		}, prepareCode: nvkrerr.CodeUnverifiedRef},
		{name: "different valid plan", mutateInputs: func(t *testing.T, definition *Definition, environment *Environment, plan **factory.Plan) {
			other, err := Compile(t.Context(), "definition.yaml", *definition, "environment.yaml", *environment, "other-generator")
			if err != nil {
				t.Fatal(err)
			}
			*plan = other
		}, verifyCode: nvkrerr.CodeVerifyFailed},
		{name: "missing output", mutateVerified: func(_ *testing.T, verified *VerifiedRelease) {
			verified.release.Artifacts = verified.release.Artifacts[:1]
		}, verifyCode: nvkrerr.CodeReleaseIncomplete},
		{name: "unexpected output", mutateVerified: func(_ *testing.T, verified *VerifiedRelease) {
			unexpected := cloneArtifact(verified.release.Artifacts[0])
			unexpected.Name = "unexpected"
			verified.release.Artifacts = append(verified.release.Artifacts, unexpected)
		}, verifyCode: nvkrerr.CodeReleaseIncomplete},
		{name: "duplicate output", mutateVerified: func(_ *testing.T, verified *VerifiedRelease) {
			verified.release.Artifacts = append(verified.release.Artifacts, cloneArtifact(verified.release.Artifacts[0]))
		}, verifyCode: nvkrerr.CodeReleaseIncomplete},
		{name: "wrong build digest", mutateVerified: func(_ *testing.T, verified *VerifiedRelease) {
			verified.release.Artifacts[0].BuildDigest = "sha256:" + repeat("9", 64)
		}, verifyCode: nvkrerr.CodeVerifyFailed},
		{name: "wrong component", mutateVerified: func(_ *testing.T, verified *VerifiedRelease) {
			verified.release.Artifacts[0].Components[0] = "substituted"
		}, verifyCode: nvkrerr.CodeVerifyFailed},
		{name: "closed capability", mutateVerified: func(t *testing.T, verified *VerifiedRelease) {
			if err := verified.Close(); err != nil {
				t.Fatal(err)
			}
		}, verifyCode: nvkrerr.CodeGateUnmet},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			verified, definition, environment, plan := fixture.open(t)
			defer func() { _ = verified.Close() }()
			if test.mutateInputs != nil {
				test.mutateInputs(t, &definition, &environment, &plan)
			}
			expectation, err := PrepareExactInputs(definition, environment, plan)
			if test.prepareCode != "" {
				assertCode(t, err, test.prepareCode)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.mutateVerified != nil {
				test.mutateVerified(t, verified)
			}
			receipt, err := verified.RequireExactInputs(expectation)
			if test.verifyCode != "" {
				assertCode(t, err, test.verifyCode)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			description, err := receipt.Description()
			if err != nil {
				t.Fatal(err)
			}
			if description.ReleaseSubject != verified.release.Subject ||
				description.PolicyDigest != verified.release.PolicyDigest ||
				description.PlanDigest != expectation.prepared.PlanDigest ||
				description.ContentSubject != verified.release.ContentSubject {
				t.Fatalf("incomplete exact-input receipt: %+v", description)
			}
		})
	}
}

func TestConsumerVerifyExactInputsClosesPublicationOnMismatch(t *testing.T) {
	fixture := newExactInputFixture(t)
	otherPlan, err := Compile(t.Context(), "definition.yaml", fixture.definition, "environment.yaml", fixture.environment, "other-generator")
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, otherPlan)
	if err != nil {
		t.Fatal(err)
	}
	var held *delivery.Publication
	verified, receipt, err := fixture.consumer(t, &held).VerifyExactInputs(t.Context(), fixture.reference, exactInputPolicy(), expectation)
	assertCode(t, err, nvkrerr.CodeVerifyFailed)
	if verified != nil || receipt != nil {
		t.Fatalf("failed exact-input narrowing returned capabilities: verified=%v receipt=%v", verified != nil, receipt != nil)
	}
	if held == nil {
		t.Fatal("failed exact-input narrowing did not materialize the publication")
	}
	if _, err := held.Description(fixture.reference); err == nil {
		t.Fatal("failed exact-input narrowing left its publication open")
	} else {
		assertCode(t, err, nvkrerr.CodeGateUnmet)
	}
}

func TestPrepareExactInputsFreezesLifecycleOutputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*factory.Plan)
	}{
		{name: "output", mutate: func(plan *factory.Plan) {
			plan.LifecycleOutputs[0].Name = "mutated-output"
		}},
		{name: "nested components", mutate: func(plan *factory.Plan) {
			plan.LifecycleOutputs[0].Components[0] = "mutated-component"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExactInputFixture(t)
			expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
			if err != nil {
				t.Fatal(err)
			}
			originalPlanDigest := expectation.prepared.PlanDigest
			test.mutate(fixture.plan)

			verified, err := fixture.consumer(t, nil).Verify(t.Context(), fixture.reference, exactInputPolicy())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = verified.Close() }()
			receipt, err := verified.RequireExactInputs(expectation)
			if err != nil {
				t.Fatal(err)
			}
			description, err := receipt.Description()
			if err != nil {
				t.Fatal(err)
			}
			if description.PlanDigest != originalPlanDigest {
				t.Fatalf("receipt plan digest = %s, want frozen %s", description.PlanDigest, originalPlanDigest)
			}
		})
	}
}

func TestVerifiedReleaseRejectsPublicationInputMetadataMismatch(t *testing.T) {
	fixture := newExactInputFixture(t)
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*delivery.ReleaseMetadata)
	}{
		{name: "definition", mutate: func(metadata *delivery.ReleaseMetadata) {
			metadata.DefinitionDigest = "sha256:" + repeat("9", 64)
		}},
		{name: "environment", mutate: func(metadata *delivery.ReleaseMetadata) {
			metadata.EnvironmentDigest = "sha256:" + repeat("9", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mismatched := fixture.withReleaseMetadata(t, test.mutate)
			verified, err := mismatched.consumer(t, nil).Verify(t.Context(), mismatched.reference, exactInputPolicy())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = verified.Close() }()
			if _, err := verified.RequireExactInputs(expectation); err == nil {
				t.Fatal("publication with mismatched input metadata satisfied exact-input expectation")
			} else {
				assertCode(t, err, nvkrerr.CodeVerifyFailed)
			}
		})
	}
}

type exactInputFixture struct {
	definition  Definition
	environment Environment
	plan        *factory.Plan
	request     delivery.PublicationRequest
	layout      string
	rootDigest  string
	reference   string
}

func newExactInputFixture(t *testing.T) exactInputFixture {
	t.Helper()
	plan := newExactInputPlan(t)
	return exactInputFixtureForPlan(t, plan.Definition, plan.Environment, plan)
}

func newExactInputPlan(t *testing.T) *factory.Plan {
	t.Helper()
	definition, environment := testDefinition(t), testEnvironment()
	definition.Outputs = append(definition.Outputs, DefinitionOutput{Name: "runtime", Platform: "linux/amd64",
		Components: []string{"kubeadm"}, Format: OutputExecutable})
	plan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func exactInputFixtureForPlan(t *testing.T, definition Definition, environment Environment, plan *factory.Plan) exactInputFixture {
	t.Helper()
	return exactInputFixtureForPlanAndInvocation(t, definition, environment, plan, "")
}

func exactInputFixtureForPlanAndInvocation(t *testing.T, definition Definition, environment Environment, plan *factory.Plan, invocation string, mutations ...func(*delivery.PublicationRequest)) exactInputFixture {
	t.Helper()
	builds := make(map[string]factory.BuildPlan, len(plan.Builds))
	for _, build := range plan.Builds {
		builds[build.Name] = build
	}
	artifacts := make([]Artifact, 0, len(definition.Outputs))
	objects := make([]delivery.ObjectInput, 0, len(definition.Outputs))
	subjects := make(map[string]string, len(definition.Outputs))
	for _, output := range definition.Outputs {
		component := output.Components[0]
		payload := []byte("exact " + component + " bytes")
		source := filepath.Join(t.TempDir(), component)
		if err := os.WriteFile(source, payload, 0o755); err != nil {
			t.Fatal(err)
		}
		bundlePath := filepath.Join(t.TempDir(), "bundle")
		bundle, err := delivery.BuildBinaryBundle(t.Context(), bundlePath, delivery.BinaryBundleRequest{
			OS: "linux", Arch: "amd64", Purpose: output.Name,
			Files: []delivery.BinaryFileInput{{Name: component, Path: source, InstallPath: "/usr/local/bin/" + component, Mode: 0o755}},
		})
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := delivery.PayloadDescriptor(t.Context(), bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, Artifact{Name: output.Name, Kind: OutputExecutable, Digest: descriptor.Digest,
			BuildDigest: builds[output.Name].BuildDigest, Components: []string{component}})
		objects = append(objects, delivery.ObjectInput{Name: output.Name, Kind: factory.PackageBinaryBundle,
			Path: bundlePath, Layout: true, ArtifactType: delivery.MediaBinaryBundle,
			SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest})
		subjects[component] = pipeline.FileDigest(payload)
	}
	request := testReleaseRequest(t, artifacts, objects, subjects)
	request.Release.InvocationID = invocation
	var err error
	request.Release.DefinitionDigest, err = definition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request.Release.PlanDigest, err = plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request.Release.EnvironmentDigest, err = environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range mutations {
		mutate(&request)
	}
	layout := filepath.Join(t.TempDir(), "publication")
	publication, err := delivery.BuildPublicationRoot(t.Context(), layout, request)
	if err != nil {
		t.Fatal(err)
	}
	layout, publication = attestFixtureLayout(t, layout)
	reference := "registry.example.test/product@" + publication.RootDigest
	return exactInputFixture{definition: definition, environment: environment, plan: plan,
		request: request, layout: layout, rootDigest: publication.RootDigest, reference: reference}
}

func (fixture exactInputFixture) open(t *testing.T) (*VerifiedRelease, Definition, Environment, *factory.Plan) {
	t.Helper()
	consumer := fixture.consumer(t, nil)
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	verified, receipt, err := consumer.VerifyExactInputs(t.Context(), fixture.reference, exactInputPolicy(), expectation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receipt.Description(); err != nil {
		t.Fatal(err)
	}
	return verified, fixture.definition, fixture.environment, fixture.plan
}

func (fixture exactInputFixture) consumer(t *testing.T, captured **delivery.Publication) *Consumer {
	t.Helper()
	verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: fixture.reference, Digest: fixture.rootDigest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	return &Consumer{verifier: verifier, materialize: func(ctx context.Context, reference string) (*delivery.Publication, error) {
		if reference != fixture.reference {
			return nil, nvkrerr.New(nvkrerr.CodeUnverifiedRef, "fixture received an unexpected publication reference")
		}
		publication, err := delivery.OpenPublication(ctx, fixture.layout, fixture.rootDigest)
		if err == nil && captured != nil {
			*captured = publication
		}
		return publication, err
	}}
}

func (fixture exactInputFixture) withReleaseMetadata(t *testing.T, mutate func(*delivery.ReleaseMetadata)) exactInputFixture {
	t.Helper()
	request := fixture.request
	metadata := *request.Release
	mutate(&metadata)
	request.Release = &metadata
	layout := filepath.Join(t.TempDir(), "publication")
	publication, err := delivery.BuildPublicationRoot(t.Context(), layout, request)
	if err != nil {
		t.Fatal(err)
	}
	layout, publication = attestFixtureLayout(t, layout)
	fixture.request = request
	fixture.layout = layout
	fixture.rootDigest = publication.RootDigest
	fixture.reference = "registry.example.test/product@" + publication.RootDigest
	return fixture
}

func exactInputPolicy() VerificationPolicy {
	return VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^builder$", Issuer: "issuer"}
}

func TestConsumerRejectsRecordForWrongObservedSigner(t *testing.T) {
	_, digest := testPublicationLayout(t)
	reference := "registry.example.test/product@" + digest
	verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: digest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "attacker", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (*delivery.Publication, error) {
		t.Fatal("wrong signer reached closure materialization")
		return nil, nil
	}}
	_, err := consumer.Verify(context.Background(), reference, VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
		IdentityRegexp: "^builder$", Issuer: "issuer"})
	assertCode(t, err, nvkrerr.CodeUntrustedSigner)
}

func TestConsumerRejectsWeakerEvidenceBeforeMaterialization(t *testing.T) {
	reference := "registry.example.test/product@sha256:" + repeat("8", 64)
	tests := []struct {
		name   string
		mutate func(*sign.VerificationRecord, *VerificationPolicy)
		want   nvkrerr.Code
	}{
		{name: "dry-run", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) {
			record.DryRun, record.SignatureVerified, record.AttestationVerified = true, false, false
			record.Ran = nil
			record.WouldRun = [][]string{{"cosign", "verify"}}
		}, want: nvkrerr.CodeSignatureInvalid},
		{name: "missing signature", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) { record.SignatureVerified = false }, want: nvkrerr.CodeSignatureInvalid},
		{name: "missing attestation", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) { record.AttestationVerified = false }, want: nvkrerr.CodeSignatureInvalid},
		{name: "different subject", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) {
			record.Digest = "sha256:" + repeat("9", 64)
			record.Ref = "registry.example.test/product@" + record.Digest
		}, want: nvkrerr.CodeSignatureInvalid},
		{name: "different issuer", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) { record.ObservedIssuer = "other" }, want: nvkrerr.CodeUntrustedSigner},
		{name: "static lane", mutate: func(_ *sign.VerificationRecord, policy *VerificationPolicy) { policy.Lane = LaneUnqualified }, want: nvkrerr.CodeTrustPolicyMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^builder$", Issuer: "issuer"}
			record := sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion, Ref: reference,
				Digest: "sha256:" + repeat("8", 64), Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
				SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}
			test.mutate(&record, &policy)
			verifier := &releaseVerifier{record: record}
			consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (*delivery.Publication, error) {
				t.Fatal("weaker evidence reached closure materialization")
				return nil, nil
			}}
			capability, err := consumer.Verify(context.Background(), reference, policy)
			assertCode(t, err, test.want)
			if capability != nil {
				t.Fatal("rejected evidence returned a live capability")
			}
			if test.name == "static lane" && verifier.calls != 0 {
				t.Fatal("static lane reached verifier")
			}
		})
	}
}

func TestConsumerRejectsStrongerOrUnconfiguredPolicyBeforeAdapters(t *testing.T) {
	digest := "sha256:" + repeat("8", 64)
	reference := "registry.example.test/product@" + digest
	tests := []struct {
		name   string
		policy VerificationPolicy
		want   nvkrerr.Code
	}{
		{name: "full pipeline release", policy: VerificationPolicy{Lane: LanePipeline, Scope: ScopeRelease, IdentityRegexp: "builder", Issuer: "issuer"}, want: nvkrerr.CodeEngineCapability},
		{name: "qualified", policy: VerificationPolicy{Lane: LaneQualified, Scope: ScopeRelease}, want: nvkrerr.CodeEngineCapability},
		{name: "qualified producer-only minimum", policy: VerificationPolicy{Lane: LaneQualified, Scope: ScopeRelease, MinimumDiskEvidence: DiskEvidenceBuildProvenance}, want: nvkrerr.CodeTrustPolicyMissing},
		{name: "missing explicit scope", policy: VerificationPolicy{Lane: LanePipeline, IdentityRegexp: "builder", Issuer: "issuer"}, want: nvkrerr.CodeTrustPolicyMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier := &releaseVerifier{}
			consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (*delivery.Publication, error) {
				t.Fatal("stronger policy reached materialization adapter")
				return nil, nil
			}}
			_, err := consumer.Verify(context.Background(), reference, test.policy)
			assertCode(t, err, test.want)
			if verifier.calls != 0 {
				t.Fatalf("verifier called %d times", verifier.calls)
			}
		})
	}
}

func TestConsumerReconstructsAPTAndHelmArtifactsFromOneReleaseReference(t *testing.T) {
	ctx := context.Background()
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definition, err := LoadDefinition(ctx, "testdata/examples/ai-factory/definition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(ctx, "testdata/examples/ai-factory/environment-local.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(ctx, "testdata/examples/ai-factory/definition.yaml", definition,
		"testdata/examples/ai-factory/environment-local.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Chdir(root)
	planned := map[string]factory.Artifact{}
	for _, artifact := range plan.Graph.Artifacts {
		planned[artifact.ID] = artifact
	}
	chartInput := planned["artifact/gpu-runtime/gpu-runtime-chart"].Path
	chartStage := filepath.Dir(chartInput)
	if err := os.MkdirAll(filepath.Join(chartStage, "helm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartStage, "helm", "Chart.yaml"), []byte("apiVersion: v2\nname: gpu-runtime\nversion: 1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := transportarchive.Pack(ctx, chartInput, chartStage, []string{"helm"}); err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(ctx, plan, "package/gpu-runtime", nil, nil); err != nil {
		t.Fatal(err)
	}
	helmPath := planned["package/gpu-runtime"].Path
	helm, helmRaw, err := delivery.ReadArtifact(ctx, helmPath)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "agent")
	if err := os.WriteFile(binary, []byte("agent bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(root, "agent_1.0.0_amd64.deb")
	//nolint:staticcheck // Compatibility assembler supplies a deterministic fixture for the public APT consumer.
	if _, err := assemble.Deb(binary, "agent", "1.0.0", 0, "amd64", deb); err != nil {
		t.Fatal(err)
	}
	transportRoot := filepath.Join(root, "transport")
	aptPath := filepath.Join(transportRoot, ".nvkr", "factory", "packages", "worker-apt")
	apt, err := delivery.BuildAPTRepository(ctx, aptPath, delivery.APTRequest{Distribution: "stable", Component: "main",
		Packages: []delivery.DebianPackageInput{{Path: deb}}})
	if err != nil {
		t.Fatal(err)
	}
	aptArchive := filepath.Join(transportRoot, "repository.tar")
	if err := transportarchive.Pack(ctx, aptArchive, transportRoot, []string{filepath.ToSlash(filepath.Join(".nvkr", "factory", "packages", "worker-apt"))}); err != nil {
		t.Fatal(err)
	}
	aptRaw, err := os.ReadFile(aptArchive)
	if err != nil {
		t.Fatal(err)
	}
	buildAPT, buildHelm := "sha256:"+repeat("5", 64), "sha256:"+repeat("6", 64)
	artifacts := []Artifact{
		{Name: "gpu-runtime", Kind: OutputHelm, Digest: helm.Digest, BuildDigest: buildHelm},
		{Name: "worker-apt", Kind: OutputAPT, Digest: apt.Digest, BuildDigest: buildAPT, Components: []string{"agent"}},
	}
	contentSubject, err := delivery.ContentDigest(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	helmIndex, err := os.ReadFile(filepath.Join(helmPath, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	debRaw, err := os.ReadFile(deb)
	if err != nil {
		t.Fatal(err)
	}
	objects := []delivery.ObjectInput{
		{Name: "gpu-runtime", Kind: factory.PackageHelm, Path: helmPath, Layout: true, ArtifactType: "application/vnd.nvkr.helm-bundle.v1", SemanticDigest: pipeline.FileDigest(helmRaw), BlobDigest: pipeline.FileDigest(helmIndex)},
		{Name: "worker-apt", Kind: factory.PackageAPT, ArtifactType: "application/vnd.nvkr.apt-repository.v1", LayerMediaType: "application/vnd.nvkr.apt-repository.v1.tar", SemanticDigest: apt.Digest, Bytes: aptRaw},
	}
	request := testReleaseRequest(t, artifacts, objects, map[string]string{"agent": pipeline.FileDigest(debRaw)})
	request.Objects[1].Output.TransportPath = ".nvkr/factory/packages/worker-apt"
	layout := filepath.Join(root, "release")
	publication, err := delivery.BuildPublicationRoot(ctx, layout, request)
	if err != nil {
		t.Fatal(err)
	}
	layout, publication = attestFixtureLayout(t, layout)
	reference := "registry.example.test/product@" + publication.RootDigest
	navigation, err := InspectRelease(ctx, reference, InspectionOptions{LayoutPath: layout})
	if err != nil {
		t.Fatal(err)
	}
	if navigation.Outputs[0].Kind != OutputHelm || navigation.Outputs[0].BuildSubjects == nil || len(navigation.Outputs[0].BuildSubjects) != 0 {
		t.Fatalf("component-free Helm output must describe an empty collection: %+v", navigation.Outputs[0])
	}
	rawNavigation, err := json.Marshal(navigation)
	if err != nil || !strings.Contains(string(rawNavigation), `"buildSubjects":[]`) {
		t.Fatalf("component-free Helm subjects must encode as an array: %s, %v", rawNavigation, err)
	}
	verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: publication.RootDigest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (*delivery.Publication, error) {
		return delivery.OpenPublication(ctx, layout, publication.RootDigest)
	}}
	verified, err := consumer.Verify(ctx, reference, VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^builder$", Issuer: "issuer"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = verified.Close() }()
	description, err := verified.Description()
	if err != nil {
		t.Fatal(err)
	}
	if description.ContentSubject != contentSubject || len(description.Artifacts) != 2 || description.Artifacts[0].Kind != OutputHelm || description.Artifacts[1].Kind != OutputAPT {
		t.Fatalf("Release projection = %+v", description)
	}
	badArtifacts := append([]Artifact(nil), artifacts...)
	badArtifacts[0].Digest = "sha256:" + repeat("9", 64)
	badContentSubject, err := delivery.ContentDigest(badArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	request.Release.ContentSubject = badContentSubject
	badLayout := filepath.Join(root, "tampered-release")
	badPublication, err := delivery.BuildPublicationRoot(ctx, badLayout, request)
	if err != nil {
		t.Fatal(err)
	}
	badLayout, badPublication = attestFixtureLayout(t, badLayout)
	badReference := "registry.example.test/product@" + badPublication.RootDigest
	badVerifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: badReference, Digest: badPublication.RootDigest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	badConsumer := &Consumer{verifier: badVerifier, materialize: func(context.Context, string) (*delivery.Publication, error) {
		return delivery.OpenPublication(ctx, badLayout, badPublication.RootDigest)
	}}
	if _, err := badConsumer.Verify(ctx, badReference, VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
		IdentityRegexp: "^builder$", Issuer: "issuer"}); err == nil {
		t.Fatal("consumer accepted a signed binding that substituted the factory-produced Helm payload")
	} else {
		assertCode(t, err, nvkrerr.CodeVerifyFailed)
	}
}

func testPublicationLayout(t *testing.T) (string, string) {
	t.Helper()
	source, _ := testPreparedPublicationLayout(t)
	layout, result := attestFixtureLayout(t, source)
	return layout, result.RootDigest
}

func testPreparedPublicationLayout(t *testing.T) (string, string) {
	return testPreparedPublicationLayoutForInvocation(t, "")
}

func testPreparedPublicationLayoutForInvocation(t *testing.T, invocation string) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "kubectl")
	if err := os.WriteFile(source, []byte("kubectl"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := delivery.BuildBinaryBundle(context.Background(), bundlePath, delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools",
		Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: source, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(context.Background(), bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Name: "tools", Kind: OutputExecutable, Digest: descriptor.Digest, BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}}
	request := testReleaseRequest(t, []Artifact{artifact}, []delivery.ObjectInput{{Name: "tools", Kind: factory.PackageBinaryBundle, Path: bundlePath, Layout: true, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest}}, map[string]string{"kubectl": pipeline.FileDigest([]byte("kubectl"))})
	if invocation != "" {
		request.Release.InvocationID = invocation
		request.Objects[0].Output.Publication = &delivery.PublicationOrigin{Ref: "registry.example.test/tools@" + descriptor.Digest,
			PreparedInvocationID: invocation, SignedInvocationID: invocation}
	}
	layout := filepath.Join(root, "publication")
	publication, err := delivery.BuildPublicationRoot(context.Background(), layout, request)
	if err != nil {
		t.Fatal(err)
	}
	return layout, publication.RootDigest
}

// testReleaseRequest creates real digest-bound evidence for consumer fixtures.
func testReleaseRequest(t *testing.T, artifacts []Artifact, objects []delivery.ObjectInput, subjects map[string]string) delivery.PublicationRequest {
	t.Helper()
	content, err := delivery.ContentDigest(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	request := delivery.PublicationRequest{Release: &delivery.ReleaseMetadata{DefinitionDigest: "sha256:" + repeat("6", 64),
		EnvironmentDigest: "sha256:" + repeat("7", 64), PlanDigest: "sha256:" + repeat("8", 64), ContentSubject: content}, Objects: objects}
	add := func(bom *sbom.BOM, output, component, stage, kind, subject, input string) {
		bound, err := sbom.ForSubject(bom, subject, stage, input)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bound.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		digest, err := bound.Digest()
		if err != nil {
			t.Fatal(err)
		}
		inspected := ""
		if input != subject {
			inspected = input
		}
		request.Objects = append(request.Objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(digest, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: digest, Bytes: raw, Evidence: &delivery.EvidenceBinding{Outputs: []string{output}, Component: component, Stage: stage, SubjectKind: kind, SubjectDigest: subject, InspectionInputDigest: inspected}})
	}
	for i, artifact := range artifacts {
		var bundleInventory *sbom.DependencyInventory
		if artifact.Kind == OutputBundle {
			_, raw, err := delivery.ReadArtifact(t.Context(), objects[i].Path)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "bundle.tar.gz")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			inventory, err := sbom.InspectDependencies(t.Context(), path, pipeline.FileDigest(raw), "bundle-archive", sbom.Scanner{})
			if err != nil {
				t.Fatal(err)
			}
			bundleInventory = &inventory
		}
		output := &delivery.OutputBinding{Repository: artifact.Name, Format: artifact.Kind, BuildDigest: artifact.BuildDigest, Components: artifact.Components}
		request.Objects[i].Output = output
		for _, component := range artifact.Components {
			subject := subjects[component]
			output.BuildSubjects = append(output.BuildSubjects, delivery.BuildSubject{Component: component, Digest: subject})
			bom, err := sbom.NewSubject(artifact.BuildDigest, component, subject)
			if err != nil {
				t.Fatal(err)
			}
			input, stage, kind := subject, "build-output", "file"
			if artifact.Kind == OutputOCI {
				subject, kind = artifact.Digest, "oci-manifest"
			}
			if artifact.Kind == OutputHelm || artifact.Kind == OutputBundle {
				stage, kind = "source-build", "source-archive"
			}
			if bundleInventory != nil {
				bom, err = sbom.WithDependencies(bom, bundleInventory.Components, bundleInventory.Method, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			add(bom, artifact.Name, component, stage, kind, subject, input)
		}
		if artifact.Kind == OutputHelm || artifact.Kind == OutputBundle {
			descriptor, raw, err := delivery.ReadArtifact(context.Background(), objects[i].Path)
			if err != nil {
				t.Fatal(err)
			}
			// subject is the OCI manifest digest; input is the digest of the actual
			// scanned bytes (the chart tarball layer), matching releaseEvidenceObjects.
			subject := descriptor.Digest
			input := pipeline.FileDigest(raw)
			path := filepath.Join(t.TempDir(), "chart.tar")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			payloadKind, subjectKind := "tarball", "helm-bundle"
			if artifact.Kind == OutputBundle {
				payloadKind, subjectKind = "bundle-archive", "bundle"
			}
			inventory, err := sbom.InspectDependencies(context.Background(), path, input, payloadKind, sbom.Scanner{})
			if err != nil {
				t.Fatal(err)
			}
			bom, err := sbom.NewSubject(artifact.BuildDigest, artifact.Name, subject)
			if err != nil {
				t.Fatal(err)
			}
			bom, err = sbom.WithDependencies(bom, inventory.Components, inventory.Method, inventory.ScannerDigest)
			if err != nil {
				t.Fatal(err)
			}
			add(bom, artifact.Name, artifact.Name, "packaging", subjectKind, subject, input)
		}
	}
	return request
}

func inspectTestPublication(t *testing.T, ctx context.Context, root, digest string) ([]Artifact, string, error) {
	t.Helper()
	publication, err := delivery.OpenPublication(ctx, root, digest)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = publication.Close() }()
	return publication.Artifacts("registry.example.test/product@" + digest)
}

func TestConsumerRejectsDifferentCheckedPublicationSubject(t *testing.T) {
	layout, digest := testPublicationLayout(t)
	reference := "registry.example.test/product@sha256:" + repeat("9", 64)
	verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: referenceDigest(reference), Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	var held *delivery.Publication
	consumer := &Consumer{verifier: verifier, materialize: func(ctx context.Context, _ string) (*delivery.Publication, error) {
		publication, err := delivery.OpenPublication(ctx, layout, digest)
		held = publication
		return publication, err
	}}
	verified, err := consumer.Verify(t.Context(), reference, VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^builder$", Issuer: "issuer"})
	assertCode(t, err, nvkrerr.CodeVerifyFailed)
	if verified != nil {
		t.Fatal("substituted checked publication returned authorization")
	}
	_, _, err = held.Artifacts("registry.example.test/product@" + digest)
	assertCode(t, err, nvkrerr.CodeGateUnmet)
}

func TestVerificationPolicyBindsLaneDiskEvidenceDefault(t *testing.T) {
	for _, test := range []struct {
		lane, scope string
		minimum     DiskEvidenceRequirement
	}{
		{LanePipeline, ScopeArtifactPublication, DiskEvidenceBuildProvenance},
		{LaneQualified, ScopeRelease, DiskEvidenceStrict},
	} {
		t.Run(test.lane, func(t *testing.T) {
			base := VerificationPolicy{Lane: test.lane, Scope: test.scope,
				IdentityRegexp: "^builder$", Issuer: "issuer"}
			implicit, err := base.Digest()
			if err != nil {
				t.Fatal(err)
			}
			explicit := base
			explicit.MinimumDiskEvidence = test.minimum
			explicitDigest, err := explicit.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if explicitDigest != implicit {
				t.Fatalf("omitted default digest %s differs from explicit %s digest %s", implicit, test.minimum, explicitDigest)
			}
			if test.lane == LaneQualified {
				return
			}
			stricter := base
			stricter.MinimumDiskEvidence = DiskEvidenceStrict
			strictDigest, err := stricter.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if strictDigest == implicit {
				t.Fatal("strict minimum did not change policy identity")
			}
		})
	}
}

func TestConsumerDiskEvidenceMinimumFailsClosed(t *testing.T) {
	strict := VerificationPolicy{MinimumDiskEvidence: DiskEvidenceStrict}
	producerAllowed := VerificationPolicy{MinimumDiskEvidence: DiskEvidenceBuildProvenance}
	strictDisk := delivery.Output{Name: "disk", Kind: OutputDiskImage, Disk: &delivery.DiskOutput{}}
	producerDisk := delivery.Output{Name: "disk", Kind: OutputDiskImage, Disk: &delivery.DiskOutput{EvidenceMode: "build-provenance-v1"}}
	nonDisk := delivery.Output{Name: "tools", Kind: OutputExecutable}
	tests := []struct {
		name     string
		policy   VerificationPolicy
		artifact delivery.Output
		want     nvkrerr.Code
	}{
		{name: "pipeline default admits producer evidence", artifact: producerDisk},
		{name: "qualified default requires inspected disk", policy: VerificationPolicy{Lane: LaneQualified}, artifact: producerDisk, want: nvkrerr.CodeGateUnmet},
		{name: "explicit strict requires inspected disk", policy: strict, artifact: producerDisk, want: nvkrerr.CodeGateUnmet},
		{name: "strict inventory satisfies strict policy", policy: strict, artifact: strictDisk},
		{name: "strict inventory satisfies producer minimum", policy: producerAllowed, artifact: strictDisk},
		{name: "explicit producer minimum admits producer evidence", policy: producerAllowed, artifact: producerDisk},
		{name: "non-disk output is unaffected", artifact: nonDisk},
		{name: "missing disk metadata fails closed", artifact: delivery.Output{Name: "disk", Kind: OutputDiskImage}, want: nvkrerr.CodeVerifyFailed},
		{name: "unknown evidence mode fails closed", policy: producerAllowed, artifact: delivery.Output{Name: "disk", Kind: OutputDiskImage, Disk: &delivery.DiskOutput{EvidenceMode: "unknown"}}, want: nvkrerr.CodeVerifyFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireDiskEvidence(test.policy, []delivery.Output{test.artifact})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCode(t, err, test.want)
		})
	}
}

func TestConsumerDiskEvidenceMinimumRejectsAnyWeakMember(t *testing.T) {
	artifacts := []delivery.Output{
		{Name: "node-amd64", Kind: OutputDiskImage, Disk: &delivery.DiskOutput{}},
		{Name: "node-arm64", Kind: OutputDiskImage, Disk: &delivery.DiskOutput{EvidenceMode: string(DiskEvidenceBuildProvenance)}},
	}
	assertCode(t, requireDiskEvidence(VerificationPolicy{MinimumDiskEvidence: DiskEvidenceStrict}, artifacts), nvkrerr.CodeGateUnmet)
}

func TestVerificationPolicyRejectsUnknownDiskEvidenceMinimum(t *testing.T) {
	policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
		IdentityRegexp: "^builder$", Issuer: "issuer", MinimumDiskEvidence: "skip-everything"}
	assertCode(t, policy.Validate(), nvkrerr.CodeTrustPolicyMissing)
}

func TestConsumerEnforcesDiskEvidencePolicyAgainstPublication(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			for _, mode := range []struct {
				name, value, inspection, encryption string
			}{{name: "strict"}, {name: "producer-only", value: string(DiskEvidenceBuildProvenance)},
				{name: "public-bootstrap FDE", value: string(DiskEvidenceBuildProvenance), encryption: "luks2"},
				{name: "historical producer-only", value: string(DiskEvidenceBuildProvenance), inspection: "deferred-m3"}} {
				t.Run(mode.name, func(t *testing.T) {
					layout, digest := testDiskPublication(t, arch, mode.value, mode.inspection, mode.encryption)
					reference := "registry.example.test/product@" + digest
					for _, minimum := range []DiskEvidenceRequirement{"", DiskEvidenceBuildProvenance, DiskEvidenceStrict} {
						name := "default-producer-minimum"
						if minimum != "" {
							name = "explicit-" + string(minimum)
						}
						t.Run(name, func(t *testing.T) {
							verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
								Ref: reference, Digest: digest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
								SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
							var held *delivery.Publication
							consumer := &Consumer{verifier: verifier, materialize: func(ctx context.Context, got string) (*delivery.Publication, error) {
								if got != reference {
									t.Fatalf("materialized reference = %q, want %q", got, reference)
								}
								publication, err := delivery.OpenPublication(ctx, layout, digest)
								held = publication
								return publication, err
							}}
							policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^builder$", Issuer: "issuer", MinimumDiskEvidence: minimum}
							verified, err := consumer.Verify(t.Context(), reference, policy)
							accepted := mode.value == "" || minimum != DiskEvidenceStrict
							if !accepted {
								assertCode(t, err, nvkrerr.CodeGateUnmet)
								if verified != nil || verifier.calls != 1 || held == nil {
									t.Fatalf("rejected result=%v verifier calls=%d publication=%v", verified, verifier.calls, held)
								}
								if _, _, err := held.Artifacts(reference); err == nil {
									t.Fatal("rejected publication remained open")
								} else {
									assertCode(t, err, nvkrerr.CodeGateUnmet)
								}
								return
							}
							if err != nil {
								t.Fatal(err)
							}
							if verifier.calls != 1 || held == nil {
								t.Fatalf("accepted verifier calls=%d publication=%v", verifier.calls, held)
							}
							if err := verified.Close(); err != nil {
								t.Fatal(err)
							}
						})
					}
				})
			}
		})
	}
}

func testDiskPublication(t *testing.T, arch, evidenceMode, inspection string, selectedEncryption ...string) (string, string) {
	t.Helper()
	encryption := "none"
	if len(selectedEncryption) != 0 && selectedEncryption[0] != "" {
		encryption = selectedEncryption[0]
	}
	ctx := t.Context()
	payload := []byte("synthetic qcow2 envelope for consumer policy")
	payloadPath := filepath.Join(t.TempDir(), "node.qcow2")
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	payloadDigest := pipeline.FileDigest(payload)
	planDigest := "sha256:" + repeat("a", 64)
	buildDigest := "sha256:" + repeat("b", 64)
	layout := filepath.Join(t.TempDir(), "disk")
	transport, err := delivery.BuildDiskArtifact(ctx, layout, delivery.DiskArtifactRequest{Path: payloadPath,
		Digest: payloadDigest, Size: int64(len(payload)), Platform: "linux/" + arch, Format: "qcow2",
		Encryption: encryption, ArtifactType: delivery.MediaDiskImage})
	if err != nil {
		t.Fatal(err)
	}
	held, err := delivery.OpenSingleDiskArtifact(ctx, layout, "linux/"+arch, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	diskInspection := held.Inspection()
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	disk := &delivery.DiskOutput{EvidenceMode: evidenceMode, PayloadDigest: payloadDigest,
		Producer: "packer", Component: "image", Format: "qcow2", Encryption: encryption, Size: int64(len(payload)),
		PlanDigest: planDigest, Outcome: "succeeded"}
	artifact := Artifact{Name: "node-image", Kind: OutputDiskImage, Digest: diskInspection.ManifestDigest,
		BuildDigest: buildDigest, Components: []string{"image"}, Disk: disk}
	input := delivery.ObjectInput{Name: artifact.Name, Kind: OutputDiskImage, Path: layout, Layout: true,
		ArtifactType: delivery.MediaDiskImage, SemanticDigest: payloadDigest, BlobDigest: transport.RootDigest,
		OS: "linux", Arch: arch}
	request := testReleaseRequest(t, []Artifact{artifact}, []delivery.ObjectInput{input}, map[string]string{"image": payloadDigest})
	request.Objects[0].Output.Disk = disk
	bom, err := sbom.NewSubject(buildDigest, "image", payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	if evidenceMode == string(DiskEvidenceBuildProvenance) {
		bom, err = sbom.ForDiskBuildProvenance(bom, payloadDigest)
		if err != nil {
			t.Fatal(err)
		}
		if inspection != "" {
			for i := range bom.Metadata.Props {
				if bom.Metadata.Props[i].Name == "nvkr:diskInspection" {
					bom.Metadata.Props[i].Value = inspection
				}
			}
		}
	} else {
		bom.Metadata.Component.Type = "operating-system"
		bom, err = sbom.ForSubject(bom, payloadDigest, "build-output", "")
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "fixture-package", Version: "1"}}, "disk-reconciled/v1", buildDigest)
		if err != nil {
			t.Fatal(err)
		}
		bom.Metadata.Props = append(bom.Metadata.Props,
			sbom.Property{Name: "nvkr:diskInspectionRuntimeDigest", Value: buildDigest},
			sbom.Property{Name: "nvkr:diskInspectorDigest", Value: buildDigest},
			sbom.Property{Name: "nvkr:diskCodecDigest", Value: buildDigest})
		sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
	}
	raw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	bomDigest, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request.Objects[1].Name = "sbom-" + strings.TrimPrefix(bomDigest, "sha256:")
	request.Objects[1].SemanticDigest = bomDigest
	request.Objects[1].Bytes = raw
	root := filepath.Join(t.TempDir(), "publication")
	prepared, err := delivery.BuildPublicationRoot(ctx, root, request)
	if err != nil {
		t.Fatal(err)
	}
	attested, result := attestFixtureLayout(t, root)
	if result.RootDigest == prepared.RootDigest {
		t.Fatal("attested publication unexpectedly retained the unsigned root identity")
	}
	return attested, result.RootDigest
}
