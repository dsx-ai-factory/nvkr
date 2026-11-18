// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type bootMembershipFixture struct {
	expected    delivery.BootInventoryExpectation
	expectation *BootInputExpectation
	verifier    *objectIntegrationVerifier
	policy      VerificationPolicy
	paths       map[string]string
	sources     map[string]string
	reads       []string
}

// This fixture tests the original-document join with real immutable layouts and
// the existing synthetic cryptographic runner. It does not prove disk/EFI boot,
// Sigstore service acceptance. Inputs and phase bounds come from a real v7 Plan.
func newBootMembershipFixture(t *testing.T, servingOrigin ...string) *bootMembershipFixture {
	t.Helper()
	selection := definition.BootSelection{SchemaVersion: definition.BootSelectionSchemaVersion,
		Disks:   []definition.BootDisk{{Output: "disk", Platform: "linux/amd64"}},
		Imagers: []definition.BootImager{{Output: "imager", Member: "kubectl", Platform: "linux/amd64", Profile: "synthetic-v1"}}}
	destination := definition.ObjectStoreDestination{Name: "boot", Mode: definition.ObjectStoreBootMode, Provider: "s3",
		S3:      &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-boot", Prefix: "boot", ExpectedBucketOwner: "123456789012", RoleRef: "github:BOOT_ROLE"},
		Serving: &definition.BootServing{Profile: definition.BootServingProfile, Origin: "https://boot.example.test", Prefix: "/boot", ReaderMode: "anonymous"}}
	if len(servingOrigin) != 0 {
		destination.S3.Endpoint, destination.S3.Addressing = servingOrigin[0], "path"
		destination.Serving.Origin = servingOrigin[0]
	}
	plan := compiledBootMembershipPlan(t, selection, destination)
	expectation, err := PrepareBootInputs(plan)
	if err != nil {
		t.Fatal(err)
	}
	prepared := expectation.exact.prepared
	metadata := delivery.ReleaseMetadata{DefinitionDigest: prepared.DefinitionDigest, EnvironmentDigest: prepared.EnvironmentDigest, PlanDigest: prepared.PlanDigest, InvocationID: objectOriginInvocation("3")}
	f := &bootMembershipFixture{expectation: expectation, paths: map[string]string{}, sources: map[string]string{}}
	f.policy = objectIntegrationPolicy()
	f.policy.Scope = ScopeSelectedBootArtifacts
	signer, err := sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: "synthetic-cosign", Identity: f.policy.IdentityRegexp, Issuer: f.policy.Issuer,
		Runner: sign.RunnerFunc(func(_ context.Context, _ string, args []string, out, _ io.Writer) error {
			if args[0] == "version" {
				_, err := io.WriteString(out, `{"gitVersion":"v3.1.3"}`)
				return err
			}
			if args[0] != "verify-blob-attestation" {
				t.Fatal("unexpected fixture subprocess", args)
			}
			return nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	f.verifier = &objectIntegrationVerifier{signer: signer, bundles: map[string][2][]byte{}, seen: map[string]bool{}}
	certificate := objectIntegrationCertificate(t)
	addProof := func(reference, attempt string, original delivery.ReleaseMetadata) {
		digest := referenceDigest(reference)
		f.verifier.bundles[reference] = [2][]byte{objectIntegrationBundle(t, certificate, digest, sign.CosignSignaturePredicateType, []byte(`{}`)), objectIntegrationBundle(t, certificate, digest, sign.ProvenancePredicateType, objectOriginPredicate(t, original, attempt))}
	}
	root := delivery.PublicationRoot{SchemaVersion: delivery.PublicationRootSchema, Qualification: delivery.QualificationDiagnostic, Release: &metadata}
	sources := map[string]string{}
	var artifacts []Artifact
	for _, name := range []string{"disk", "imager", "unused"} {
		component := name
		if name != "disk" {
			component = "kubectl"
		}
		payload := []byte("opaque synthetic " + name + " bytes")
		input := filepath.Join(t.TempDir(), "input")
		if err := os.WriteFile(input, payload, 0600); err != nil {
			t.Fatal(err)
		}
		rawDigest, build := pipeline.FileDigest(payload), pipeline.FileDigest([]byte("build-"+name))
		for _, output := range plan.LifecycleOutputs {
			if output.Name == name {
				build = output.BuildDigest
			}
		}
		artifactPath := filepath.Join(t.TempDir(), "artifact")
		kind, packageKind, media := OutputExecutable, factory.PackageBinaryBundle, delivery.MediaBinaryBundle
		var built delivery.Result
		var disk *delivery.DiskOutput
		if name == "disk" {
			kind, packageKind, media = OutputDiskImage, "disk-image", delivery.MediaDiskImage
			built, err = delivery.BuildDiskArtifact(t.Context(), artifactPath, delivery.DiskArtifactRequest{Path: input, Digest: rawDigest, Size: int64(len(payload)), Platform: "linux/amd64", Format: "qcow2", Encryption: "none", ArtifactType: media})
			disk = &delivery.DiskOutput{EvidenceMode: sbom.DiskBuildProvenance, PayloadDigest: rawDigest, Producer: "build-disk", Component: name, Format: "qcow2", Encryption: "none", Size: int64(len(payload)), PlanDigest: metadata.PlanDigest, Outcome: "succeeded"}
		} else {
			binary := delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: name, Files: []delivery.BinaryFileInput{{Name: component, Path: input, InstallPath: "/boot/" + name, Mode: 0644}}}
			if name == "imager" {
				binary.Boot = &delivery.BinaryBootBinding{Member: component, Profile: "synthetic-v1"}
			}
			built, err = delivery.BuildBinaryBundle(t.Context(), artifactPath, binary)
		}
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := delivery.PayloadDescriptor(t.Context(), artifactPath)
		if err != nil {
			t.Fatal(err)
		}
		artifact := Artifact{Name: name, Kind: kind, Digest: descriptor.Digest, BuildDigest: build, Components: []string{component}, Disk: disk}
		artifacts = append(artifacts, artifact)
		localMetadata := metadata
		attempt := "1"
		if name == "imager" {
			attempt = "2"
		}
		localMetadata.InvocationID = objectOriginInvocation(attempt)
		localMetadata.ContentSubject, err = delivery.ContentDigest([]delivery.Output{artifact})
		if err != nil {
			t.Fatal(err)
		}
		bom, err := sbom.NewSubject(build, component, rawDigest)
		if err != nil {
			t.Fatal(err)
		}
		if disk != nil {
			bom, err = sbom.ForDiskBuildProvenance(bom, rawDigest)
		} else {
			bom, err = sbom.ForSubject(bom, rawDigest, "build-output", rawDigest)
		}
		if err != nil {
			t.Fatal(err)
		}
		bomDigest, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		bomRaw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic := built.SemanticDigest
		if disk != nil {
			semantic = rawDigest
		}
		localRequest := delivery.PublicationRequest{Release: &localMetadata, Objects: []delivery.ObjectInput{
			{Name: name, Kind: packageKind, Path: artifactPath, Layout: true, ArtifactType: media, SemanticDigest: semantic, BlobDigest: built.RootDigest, OS: "linux", Arch: "amd64",
				Output: &delivery.OutputBinding{Repository: name, Format: kind, BuildDigest: build, Components: []string{component}, Disk: disk, BuildSubjects: []delivery.BuildSubject{{Component: component, Digest: rawDigest}}}},
			{Name: "sbom-" + strings.TrimPrefix(bomDigest, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: bomDigest, Bytes: bomRaw,
				Evidence: &delivery.EvidenceBinding{Outputs: []string{name}, Component: component, Stage: "build-output", SubjectKind: "file", SubjectDigest: rawDigest}},
		}}
		path := filepath.Join(t.TempDir(), "local")
		if _, err := delivery.BuildPublicationRoot(t.Context(), path, localRequest); err != nil {
			t.Fatal(err)
		}
		path, final := attestFixtureLayout(t, path)
		inspection, err := delivery.Inspect(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		repository := "registry.example.test/product/releases-" + name
		reference := repository + "@" + final.RootDigest
		for _, object := range inspection.Root.Objects {
			if object.Output != nil {
				binding := *object.Output
				binding.Publication = &delivery.PublicationOrigin{Ref: reference, PreparedInvocationID: localMetadata.InvocationID, SignedInvocationID: localMetadata.InvocationID}
				object.Output = &binding
			}
			root.Objects = append(root.Objects, object)
			sources[object.Name] = path
		}
		if name != "unused" {
			f.sources[reference] = path
			if f.expectation.repositories[name] != repository {
				t.Fatal("fixture publication differs from compiled repository")
			}
			sparse := filepath.Join(t.TempDir(), "sparse")
			if _, err := delivery.CopySelectedMetadataWithExpectedRoot(t.Context(), path, sparse, final.RootDigest, []string{name}); err != nil {
				t.Fatal(err)
			}
			f.paths[reference] = sparse
			addProof(reference, attempt, localMetadata)
			addProof("registry.example.test/product/"+name+"@"+descriptor.Digest, attempt, localMetadata)
		}
	}
	metadata.ContentSubject, err = delivery.ContentDigest(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(root.Objects, func(a, b delivery.PublishedObject) int { return strings.Compare(a.Name, b.Name) })
	aggregatePath := filepath.Join(t.TempDir(), "aggregate")
	aggregate, err := delivery.BuildMetadataRoot(t.Context(), aggregatePath, root, sources)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product/releases@" + aggregate.RootDigest
	sparse := filepath.Join(t.TempDir(), "selected-aggregate")
	if _, err := delivery.CopySelectedMetadataWithExpectedRoot(t.Context(), aggregatePath, sparse, aggregate.RootDigest, selectedBootNames(selection)); err != nil {
		t.Fatal(err)
	}
	f.paths[reference] = sparse
	addProof(reference, "3", metadata)
	f.expected = delivery.BootInventoryExpectation{RootReference: reference, Invocation: metadata.InvocationID, DefinitionDigest: metadata.DefinitionDigest, EnvironmentDigest: metadata.EnvironmentDigest, PlanDigest: metadata.PlanDigest, Selection: selection, Destination: destination}
	return f
}

func compiledBootMembershipPlan(t *testing.T, selection definition.BootSelection, destination definition.ObjectStoreDestination) *factory.Plan {
	t.Helper()
	t.Chdir(t.TempDir())
	base, _, _, _ := hostedDiskObservationFixture(t)
	input, environment := base.Definition.Clone(), base.Environment.Clone()
	input.Outputs = []definition.DefinitionOutput{
		{Name: "disk", Format: definition.OutputDiskImage, Platform: "linux/amd64", Components: []string{"disk"}},
		{Name: "imager", Format: definition.OutputExecutable, Platform: "linux/amd64", Components: []string{"kubectl"}},
		{Name: "unused", Format: definition.OutputExecutable, Platform: "linux/amd64", Components: []string{"kubectl"}},
	}
	environment.APIVersion, environment.GitHubARC = definition.EnvironmentBootAPIVersion, "native-kvm-v1"
	environment.Runtime.Runtime = "docker"
	for index := range environment.ExecutionClasses {
		environment.ExecutionClasses[index].RunsOn = "arc-amd64-kvm"
	}
	environment.Publication.Mode = "per-output-v1"
	environment.Publication.BootSelection = &selection
	environment.Publication.ObjectStores = []definition.ObjectStoreDestination{destination}
	disk := base.CatalogSnapshot.Components["nvke-node-non-fde"]
	// Reuse the complete executable producer already compiled by lifecycle's
	// kubectl fixture, including its source pins and host-package output type.
	snapshot := newExactInputPlan(t).CatalogSnapshot
	executable := snapshot.Components["kubectl"]
	snapshot.Tools = base.CatalogSnapshot.Tools
	snapshot.Components = map[string]catalog.Component{"disk": disk, "kubectl": executable}
	stack, err := catalog.ApplyStack(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), input, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != factory.BootPlanSchemaVersion {
		t.Fatal("fixture did not compile v7")
	}
	return plan
}

func (f *bootMembershipFixture) read(t *testing.T) bootMetadataReader {
	t.Helper()
	return func(ctx context.Context, reference string, names []string) (*delivery.SelectedMetadata, error) {
		f.reads = append(f.reads, reference)
		path, exists := f.paths[reference]
		if !exists {
			return nil, e.New(e.CodeVerifyFailed, "fixture forbids an unselected or foreign root")
		}
		owner, err := delivery.OpenSelectedMetadata(ctx, path, referenceDigest(reference), names, t.TempDir())
		if err == nil {
			t.Cleanup(func() { _ = owner.Close() })
		}
		return owner, err
	}
}

func TestBootMembershipAuthenticatesOnlySelectedOriginalProofs(t *testing.T) {
	f := newBootMembershipFixture(t)
	got, err := authenticateBootMembership(t.Context(), f.expected, f.expectation, f.policy, f.verifier, f.read(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.uses) != 2 || len(got.proofs) != 5 || len(got.publications) != 3 || len(f.reads) != 3 || len(f.verifier.seen) != 5 {
		t.Fatal("selected closure expanded or omitted originals")
	}
	if got.uses[0].Binding.Output != "disk" || got.uses[1].Binding.Member != "kubectl" || got.uses[1].SignedInvocation != objectOriginInvocation("2") {
		t.Fatal("raw member or retained invocation lost")
	}
}

func TestBootMembershipRejectsChangedExpectationsAndMissingOriginals(t *testing.T) {
	base := newBootMembershipFixture(t)
	for _, test := range []struct {
		name string
		edit func(*bootMembershipFixture)
		code e.Code
	}{
		{"definition", func(f *bootMembershipFixture) { f.expected.DefinitionDigest = pipeline.FileDigest([]byte("other")) }, e.CodeVerifyFailed},
		{"plan", func(f *bootMembershipFixture) { f.expected.PlanDigest = pipeline.FileDigest([]byte("other")) }, e.CodeVerifyFailed},
		{"member", func(f *bootMembershipFixture) { f.expected.Selection.Imagers[0].Member = "foreign" }, e.CodeVerifyFailed},
		{"platform", func(f *bootMembershipFixture) { f.expected.Selection.Disks[0].Platform = "linux/arm64" }, e.CodeVerifyFailed},
		{"profile", func(f *bootMembershipFixture) { f.expected.Selection.Imagers[0].Profile = "foreign" }, e.CodeVerifyFailed},
		{"repository", func(f *bootMembershipFixture) { f.expectation.repositories["disk"] = "registry.example.test/foreign" }, e.CodeVerifyFailed},
		{"future aggregate", func(f *bootMembershipFixture) { f.expected.Invocation = objectOriginInvocation("2") }, e.CodeVerifyFailed},
		{"later consumer attempt", func(f *bootMembershipFixture) { f.expected.Invocation = objectOriginInvocation("4") }, e.CodeVerifyFailed},
		{"aggregate proof", func(f *bootMembershipFixture) { delete(f.verifier.bundles, f.expected.RootReference) }, e.CodeProvenanceMissing},
		{"local root proof", func(f *bootMembershipFixture) {
			for reference := range f.verifier.bundles {
				if strings.Contains(reference, "/releases-disk@") {
					delete(f.verifier.bundles, reference)
				}
			}
		}, e.CodeProvenanceMissing},
		{"descriptor proof", func(f *bootMembershipFixture) {
			for reference := range f.verifier.bundles {
				if strings.Contains(reference, "/imager@") {
					delete(f.verifier.bundles, reference)
				}
			}
		}, e.CodeProvenanceMissing},
		{"disk evidence minimum", func(f *bootMembershipFixture) { f.policy.MinimumDiskEvidence = DiskEvidenceStrict }, e.CodeGateUnmet},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := *base
			f.expected.Selection = base.expected.Selection.Clone()
			expectation := *base.expectation
			expectation.repositories = maps.Clone(base.expectation.repositories)
			f.expectation = &expectation
			verifier := *base.verifier
			verifier.bundles, verifier.seen = maps.Clone(base.verifier.bundles), map[string]bool{}
			f.verifier = &verifier
			test.edit(&f)
			_, err := authenticateBootMembership(t.Context(), f.expected, f.expectation, f.policy, f.verifier, f.read(t))
			assertCode(t, err, test.code)
		})
	}
}

type bootObservedSignerVerifier struct {
	*objectIntegrationVerifier
	issuer bool
}

func (v *bootObservedSignerVerifier) VerifyAndRetain(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error) {
	record, proof, err := v.objectIntegrationVerifier.VerifyAndRetain(ctx, subject, policy)
	if v.issuer {
		record.ObservedIssuer = "https://foreign.example.test"
	} else {
		record.ObservedIdentity = "foreign@example.test"
	}
	return record, proof, err
}

func TestBootMembershipChecksObservedSigner(t *testing.T) {
	f := newBootMembershipFixture(t)
	for _, test := range []struct {
		name   string
		issuer bool
	}{{"identity", false}, {"issuer", true}} {
		t.Run(test.name, func(t *testing.T) {
			verifier := &bootObservedSignerVerifier{objectIntegrationVerifier: f.verifier, issuer: test.issuer}
			_, err := authenticateBootMembership(t.Context(), f.expected, f.expectation, f.policy, verifier, f.read(t))
			assertCode(t, err, e.CodeUntrustedSigner)
		})
	}
}

func (f *bootMembershipFixture) setAggregateSigningAttempt(t *testing.T, attempt string, foreignRun bool) {
	t.Helper()
	metadata := delivery.ReleaseMetadata{DefinitionDigest: f.expected.DefinitionDigest, EnvironmentDigest: f.expected.EnvironmentDigest, PlanDigest: f.expected.PlanDigest, InvocationID: f.expected.Invocation}
	predicate := objectOriginPredicate(t, metadata, attempt)
	if foreignRun {
		predicate = []byte(strings.ReplaceAll(string(predicate), "/runs/123/", "/runs/999/"))
	}
	bundles := f.verifier.bundles[f.expected.RootReference]
	bundles[1] = objectIntegrationBundle(t, objectIntegrationCertificate(t), referenceDigest(f.expected.RootReference), sign.ProvenancePredicateType, predicate)
	f.verifier.bundles[f.expected.RootReference] = bundles
}

func TestBootMembershipPreservesAggregateSigningRetries(t *testing.T) {
	base := newBootMembershipFixture(t)
	for _, test := range []struct {
		name, attempt  string
		foreign, valid bool
	}{
		{"same attempt", "3", false, true},
		{"later signing", "4", false, true},
		{"before assembly", "2", false, false},
		{"foreign run", "4", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := *base
			verifier := *base.verifier
			verifier.bundles = maps.Clone(base.verifier.bundles)
			f.verifier = &verifier
			f.setAggregateSigningAttempt(t, test.attempt, test.foreign)
			_, err := authenticateBootMembership(t.Context(), f.expected, f.expectation, f.policy, f.verifier, f.read(t))
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}
