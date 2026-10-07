// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

type releaseVerifier struct {
	calls  int
	record sign.VerificationRecord
	err    error
}

func (*releaseVerifier) SignAndAttest(context.Context, sign.Subject, []byte) (sign.SigningRecord, error) {
	return sign.SigningRecord{}, nkderr.New(nkderr.CodeUsage, "verification-only test adapter")
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
		assertCode(t, err, nkderr.CodeGateUnmet)
	}
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
	assertCode(t, err, nkderr.CodeUntrustedSigner)
}

func TestConsumerRejectsWeakerEvidenceBeforeMaterialization(t *testing.T) {
	reference := "registry.example.test/product@sha256:" + repeat("8", 64)
	tests := []struct {
		name   string
		mutate func(*sign.VerificationRecord, *VerificationPolicy)
		want   nkderr.Code
	}{
		{name: "dry-run", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) {
			record.DryRun, record.SignatureVerified, record.AttestationVerified = true, false, false
			record.Ran = nil
			record.WouldRun = [][]string{{"cosign", "verify"}}
		}, want: nkderr.CodeSignatureInvalid},
		{name: "missing signature", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) { record.SignatureVerified = false }, want: nkderr.CodeSignatureInvalid},
		{name: "missing attestation", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) { record.AttestationVerified = false }, want: nkderr.CodeSignatureInvalid},
		{name: "different subject", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) {
			record.Digest = "sha256:" + repeat("9", 64)
			record.Ref = "registry.example.test/product@" + record.Digest
		}, want: nkderr.CodeSignatureInvalid},
		{name: "different issuer", mutate: func(record *sign.VerificationRecord, _ *VerificationPolicy) { record.ObservedIssuer = "other" }, want: nkderr.CodeUntrustedSigner},
		{name: "static lane", mutate: func(_ *sign.VerificationRecord, policy *VerificationPolicy) { policy.Lane = LaneUnqualified }, want: nkderr.CodeTrustPolicyMissing},
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
		want   nkderr.Code
	}{
		{name: "full pipeline release", policy: VerificationPolicy{Lane: LanePipeline, Scope: ScopeRelease, IdentityRegexp: "builder", Issuer: "issuer"}, want: nkderr.CodeEngineCapability},
		{name: "qualified", policy: VerificationPolicy{Lane: LaneQualified, Scope: ScopeRelease}, want: nkderr.CodeEngineCapability},
		{name: "missing explicit scope", policy: VerificationPolicy{Lane: LanePipeline, IdentityRegexp: "builder", Issuer: "issuer"}, want: nkderr.CodeTrustPolicyMissing},
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
	aptPath := filepath.Join(transportRoot, ".nkd", "factory", "packages", "worker-apt")
	apt, err := delivery.BuildAPTRepository(ctx, aptPath, delivery.APTRequest{Distribution: "stable", Component: "main",
		Packages: []delivery.DebianPackageInput{{Path: deb}}})
	if err != nil {
		t.Fatal(err)
	}
	aptArchive := filepath.Join(transportRoot, "repository.tar")
	if err := transportarchive.Pack(ctx, aptArchive, transportRoot, []string{filepath.ToSlash(filepath.Join(".nkd", "factory", "packages", "worker-apt"))}); err != nil {
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
		{Name: "gpu-runtime", Kind: factory.PackageHelm, Path: helmPath, Layout: true, ArtifactType: "application/vnd.nkd.helm-bundle.v1", SemanticDigest: pipeline.FileDigest(helmRaw), BlobDigest: pipeline.FileDigest(helmIndex)},
		{Name: "worker-apt", Kind: factory.PackageAPT, ArtifactType: "application/vnd.nkd.apt-repository.v1", LayerMediaType: "application/vnd.nkd.apt-repository.v1.tar", SemanticDigest: apt.Digest, Bytes: aptRaw},
	}
	request := testReleaseRequest(t, artifacts, objects, map[string]string{"agent": pipeline.FileDigest(debRaw)})
	request.Objects[1].Output.TransportPath = ".nkd/factory/packages/worker-apt"
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
		assertCode(t, err, nkderr.CodeVerifyFailed)
	}
}

func testPublicationLayout(t *testing.T) (string, string) {
	t.Helper()
	source, _ := testPreparedPublicationLayout(t)
	layout, result := attestFixtureLayout(t, source)
	return layout, result.RootDigest
}

func testPreparedPublicationLayout(t *testing.T) (string, string) {
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
	request := delivery.PublicationRequest{Release: &delivery.ReleaseMetadata{DefinitionDigest: "sha256:" + repeat("6", 64), EnvironmentDigest: "sha256:" + repeat("7", 64), ContentSubject: content}, Objects: objects}
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
			if artifact.Kind == OutputHelm {
				stage, kind = "source-build", "source-archive"
			}
			add(bom, artifact.Name, component, stage, kind, subject, input)
		}
		if artifact.Kind == OutputHelm {
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
			inventory, err := sbom.InspectDependencies(context.Background(), path, input, "tarball", sbom.Scanner{})
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
			add(bom, artifact.Name, artifact.Name, "packaging", "helm-bundle", subject, input)
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
	assertCode(t, err, nkderr.CodeVerifyFailed)
	if verified != nil {
		t.Fatal("substituted checked publication returned authorization")
	}
	_, _, err = held.Artifacts("registry.example.test/product@" + digest)
	assertCode(t, err, nkderr.CodeGateUnmet)
}
