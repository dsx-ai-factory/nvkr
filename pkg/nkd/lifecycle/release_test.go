package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/assemble"
	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/factory"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/sign"
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
	consumer := &Consumer{verifier: verifier, materialize: func(ctx context.Context, got string) (materializedRelease, error) {
		if got != reference {
			t.Fatalf("reference = %s", got)
		}
		inspection, err := delivery.Inspect(ctx, layout)
		return materializedRelease{inspection: inspection, path: layout, cleanup: func() {}}, err
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
	consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (materializedRelease, error) {
		t.Fatal("wrong signer reached closure materialization")
		return materializedRelease{}, nil
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
			consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (materializedRelease, error) {
				t.Fatal("weaker evidence reached closure materialization")
				return materializedRelease{}, nil
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
			consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (materializedRelease, error) {
				t.Fatal("stronger policy reached materialization adapter")
				return materializedRelease{}, nil
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
	aicrPath := planned["artifact/h100-runtime/aicr-bundle"].Path
	if err := os.MkdirAll(aicrPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aicrPath, "Chart.yaml"), []byte("apiVersion: v2\nname: gpu-runtime\nversion: 1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(ctx, plan, "package/gpu-runtime", nil, nil); err != nil {
		t.Fatal(err)
	}
	helmPath := planned["package/gpu-runtime"].Path
	helm, err := delivery.Inspect(ctx, helmPath)
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
	if err := pipeline.PackTransport(ctx, aptArchive, transportRoot, []string{filepath.ToSlash(filepath.Join(".nkd", "factory", "packages", "worker-apt"))}); err != nil {
		t.Fatal(err)
	}
	aptRaw, err := os.ReadFile(aptArchive)
	if err != nil {
		t.Fatal(err)
	}
	recipeAPT, recipeHelm := "sha256:"+repeat("5", 64), "sha256:"+repeat("6", 64)
	artifacts := []Artifact{
		{Name: "gpu-runtime", Kind: OutputHelm, Digest: helm.RootDigest, RecipeDigest: recipeHelm},
		{Name: "worker-apt", Kind: OutputAPT, Digest: apt.Digest, RecipeDigest: recipeAPT, Components: []string{"agent"}},
	}
	contentSubject, err := canonical.Digest(canonical.KindReleaseContent, releaseContent{Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	binding := factory.LifecycleBinding{SchemaVersion: factory.LifecycleBindingSchema,
		DefinitionDigest: "sha256:" + repeat("7", 64), EnvironmentDigest: "sha256:" + repeat("8", 64), ContentSubject: contentSubject,
		Outputs: []factory.LifecycleBoundOutput{
			{Name: "gpu-runtime", Kind: OutputHelm, Digest: helm.RootDigest, RecipeDigest: recipeHelm},
			{Name: "worker-apt", Kind: OutputAPT, Digest: apt.Digest, RecipeDigest: recipeAPT, Components: []string{"agent"}, TransportPath: ".nkd/factory/packages/worker-apt"},
		}}
	bindingRaw, err := canonical.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := canonical.Digest(canonical.KindLifecycleBinding, binding)
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(root, "release")
	publication, err := delivery.BuildPublicationRoot(ctx, layout, delivery.PublicationRequest{Objects: []delivery.ObjectInput{
		{Name: "gpu-runtime", Kind: factory.PackageHelm, Path: helmPath, Layout: true, ArtifactType: "application/vnd.nkd.helm-oci.v1", SemanticDigest: helm.RootDigest, BlobDigest: helm.RootDigest},
		{Name: "worker-apt", Kind: factory.PackageAPT, ArtifactType: "application/vnd.nkd.apt-repository.v1", LayerMediaType: "application/vnd.nkd.apt-repository.v1.tar", SemanticDigest: apt.Digest, BlobDigest: pipeline.FileDigest(aptRaw), Bytes: aptRaw},
		{Name: "nkd-lifecycle", Kind: "lifecycle-binding", ArtifactType: "application/vnd.nkd.lifecycle-binding.v1+json", LayerMediaType: "application/vnd.nkd.lifecycle-binding.v1+json", SemanticDigest: bindingDigest, Bytes: bindingRaw},
	}})
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product@" + publication.RootDigest
	verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: publication.RootDigest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (materializedRelease, error) {
		inspection, inspectErr := delivery.Inspect(ctx, layout)
		return materializedRelease{inspection: inspection, path: layout, cleanup: func() {}}, inspectErr
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
	badContentSubject, err := canonical.Digest(canonical.KindReleaseContent, releaseContent{Artifacts: badArtifacts})
	if err != nil {
		t.Fatal(err)
	}
	badBinding := binding
	badBinding.ContentSubject = badContentSubject
	badBinding.Outputs = append([]factory.LifecycleBoundOutput(nil), binding.Outputs...)
	badBinding.Outputs[0].Digest = badArtifacts[0].Digest
	badBindingRaw, err := canonical.Marshal(badBinding)
	if err != nil {
		t.Fatal(err)
	}
	badBindingDigest, err := canonical.Digest(canonical.KindLifecycleBinding, badBinding)
	if err != nil {
		t.Fatal(err)
	}
	badLayout := filepath.Join(root, "tampered-release")
	badPublication, err := delivery.BuildPublicationRoot(ctx, badLayout, delivery.PublicationRequest{Objects: []delivery.ObjectInput{
		{Name: "gpu-runtime", Kind: factory.PackageHelm, Path: helmPath, Layout: true, ArtifactType: "application/vnd.nkd.helm-oci.v1", SemanticDigest: helm.RootDigest, BlobDigest: helm.RootDigest},
		{Name: "worker-apt", Kind: factory.PackageAPT, ArtifactType: "application/vnd.nkd.apt-repository.v1", LayerMediaType: "application/vnd.nkd.apt-repository.v1.tar", SemanticDigest: apt.Digest, BlobDigest: pipeline.FileDigest(aptRaw), Bytes: aptRaw},
		{Name: "nkd-lifecycle", Kind: "lifecycle-binding", ArtifactType: "application/vnd.nkd.lifecycle-binding.v1+json", LayerMediaType: "application/vnd.nkd.lifecycle-binding.v1+json", SemanticDigest: badBindingDigest, Bytes: badBindingRaw},
	}})
	if err != nil {
		t.Fatal(err)
	}
	badReference := "registry.example.test/product@" + badPublication.RootDigest
	badVerifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: badReference, Digest: badPublication.RootDigest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	badConsumer := &Consumer{verifier: badVerifier, materialize: func(context.Context, string) (materializedRelease, error) {
		inspection, inspectErr := delivery.Inspect(ctx, badLayout)
		return materializedRelease{inspection: inspection, path: badLayout, cleanup: func() {}}, inspectErr
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
	artifact := Artifact{Name: "tools", Kind: OutputExecutable, Digest: bundle.RootDigest,
		RecipeDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}}
	contentSubject, err := canonical.Digest(canonical.KindReleaseContent, releaseContent{Artifacts: []Artifact{artifact}})
	if err != nil {
		t.Fatal(err)
	}
	binding := factory.LifecycleBinding{SchemaVersion: factory.LifecycleBindingSchema,
		DefinitionDigest: "sha256:" + repeat("6", 64), EnvironmentDigest: "sha256:" + repeat("7", 64),
		ContentSubject: contentSubject, Outputs: []factory.LifecycleBoundOutput{{Name: "tools", Kind: OutputExecutable,
			Digest: bundle.RootDigest, RecipeDigest: artifact.RecipeDigest, Components: []string{"kubectl"}}}}
	bindingRaw, err := canonical.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := canonical.Digest(canonical.KindLifecycleBinding, binding)
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(root, "publication")
	publication, err := delivery.BuildPublicationRoot(context.Background(), layout, delivery.PublicationRequest{Objects: []delivery.ObjectInput{{
		Name: "tools", Kind: "binary-bundle", Path: bundlePath, Layout: true, ArtifactType: delivery.MediaBinaryBundle,
		SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest,
	}, {Name: "nkd-lifecycle", Kind: "lifecycle-binding", ArtifactType: "application/vnd.nkd.lifecycle-binding.v1+json",
		LayerMediaType: "application/vnd.nkd.lifecycle-binding.v1+json", SemanticDigest: bindingDigest, Bytes: bindingRaw}}})
	if err != nil {
		t.Fatal(err)
	}
	return layout, publication.RootDigest
}
