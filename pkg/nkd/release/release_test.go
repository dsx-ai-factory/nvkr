package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/attest"
	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/provenance"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
	"github.com/nhensley/nkdctl/pkg/nkd/sbom"
	"github.com/nhensley/nkdctl/pkg/nkd/status"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
	"github.com/nhensley/nkdctl/pkg/nkd/vuln"
)

type receiptAuthenticator struct {
	signers map[ReceiptRole]string
	err     error
}

func (authenticator receiptAuthenticator) AuthenticateReceipt(_ context.Context, _, _ []byte, role ReceiptRole) (string, error) {
	if authenticator.err != nil {
		return "", authenticator.err
	}
	if signer := authenticator.signers[role]; signer != "" {
		return signer, nil
	}
	return "", errors.New("role not authorized")
}

type qualifiedScanner struct {
	observation vuln.ScanObservation
}

type statusDocumentVerifier struct {
	signer string
	issuer string
}

func (verifier statusDocumentVerifier) VerifyDocument(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{verifier.signer}, OIDCIssuer: verifier.issuer,
		CertificateSHA256: digest('a'),
	}, nil
}

func (scanner qualifiedScanner) Scan(context.Context, vuln.QualifiedScanRequest) (vuln.ScanObservation, error) {
	return scanner.observation, nil
}

func code(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func digest(char byte) string { return "sha256:" + strings.Repeat(string(char), 64) }

func TestResolveSelectorRequiresCanonicalExactDigest(t *testing.T) {
	valid := digest('a')
	resolved, err := ResolveSelector("registry.example/release@"+valid, nil)
	if err != nil || resolved != valid {
		t.Fatalf("canonical digest selector: %q %v", resolved, err)
	}
	for name, selector := range map[string]string{
		"surrounding whitespace": " registry.example/release@" + valid,
		"missing name":           "@" + valid,
		"uppercase":              "registry.example/release@sha256:" + strings.Repeat("A", 64),
		"non hex":                "registry.example/release@sha256:" + strings.Repeat("z", 64),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveSelector(selector, nil); code(err) != nkderr.CodeUnverifiedRef {
				t.Fatalf("non-canonical selector must fail closed: %v", err)
			}
		})
	}
	for name, resolvedDigest := range map[string]string{
		"uppercase": "sha256:" + strings.Repeat("A", 64),
		"non hex":   "sha256:" + strings.Repeat("z", 64),
	} {
		t.Run("resolved "+name, func(t *testing.T) {
			if _, err := ResolveSelector("registry.example/release:stable", func(string) (string, error) {
				return resolvedDigest, nil
			}); code(err) != nkderr.CodeUnverifiedRef {
				t.Fatalf("resolver returned non-canonical digest: %v", err)
			}
		})
	}
}

func qualifiedReport(t *testing.T, recipeDigest, artifactDigest string,
	localSBOM sbom.LocalEvidenceReceipt,
) vuln.QualifiedReport {
	t.Helper()
	binding, err := localSBOM.Binding()
	if err != nil {
		t.Fatal(err)
	}
	if binding.RecipeDigest != recipeDigest || binding.ArtifactDigest != artifactDigest {
		t.Fatalf("local SBOM fixture does not bind vulnerability subject")
	}
	sbomBytes, err := localSBOM.ExactBytes()
	if err != nil {
		t.Fatal(err)
	}
	request := vuln.QualifiedScanRequest{
		Scanner: vuln.ScannerGrype, ScannerDigest: digest('7'), DatabaseDigest: digest('8'),
		RecipeDigest: recipeDigest, ArtifactDigest: artifactDigest,
		SBOMDigest: binding.EvidenceDigest, CanonicalSBOM: sbomBytes,
	}
	report, err := vuln.QualifyScan(context.Background(), request, qualifiedScanner{observation: vuln.ScanObservation{
		ScannerDigest: request.ScannerDigest, DatabaseDigest: request.DatabaseDigest,
		Result: []byte(`{"matches":[]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func verifiedStatus(t *testing.T, target, authority string, asOfEpoch int64) StatusBinding {
	t.Helper()
	receipt := verifiedStatusCheckpoint(t, target, authority, asOfEpoch, nil)
	binding, err := BindVerifiedStatusCheckpoint(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func verifiedStatusCheckpoint(t *testing.T, target, authority string, asOfEpoch int64,
	declaration *status.StatusDeclaration,
) status.CheckpointReceipt {
	return statusCheckpointFixture(t, target, authority, asOfEpoch, declaration, true)
}

func diagnosticStatusCheckpoint(t *testing.T, target, authority string, asOfEpoch int64) status.CheckpointReceipt {
	return statusCheckpointFixture(t, target, authority, asOfEpoch, nil, false)
}

func statusCheckpointFixture(t *testing.T, target, authority string, asOfEpoch int64,
	declaration *status.StatusDeclaration, releaseGrade bool,
) status.CheckpointReceipt {
	t.Helper()
	declarationDigests := []string{}
	declarationInputs := []trust.DocumentReceipt{}
	const signer = "status-signer"
	const issuer = "https://status-issuer.example.test"
	authenticator, err := trust.NewDocumentAuthenticator(
		statusDocumentVerifier{signer: signer, issuer: issuer},
		trust.JSONSchema[status.StatusCheckpoint](status.CheckpointSchemaVersion),
		trust.JSONSchema[status.StatusDeclaration](status.SchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleStatus,
		trust.Store{OperatorIdentities: []string{signer}}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	if declaration != nil {
		raw, err := declaration.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "status-declaration.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
			SourcePath: path, Role: trust.RoleStatus, Policy: rolePolicy,
		})
		if err != nil {
			t.Fatal(err)
		}
		digest, err := declaration.Digest()
		if err != nil {
			t.Fatal(err)
		}
		declarationDigests = []string{digest}
		declarationInputs = []trust.DocumentReceipt{document}
	}
	checkpoint := status.StatusCheckpoint{
		SchemaVersion: status.CheckpointSchemaVersion, Target: target, Authority: authority,
		Sequence: 0, DeclarationDigests: declarationDigests, IssuedEpoch: 1, ValidUntilEpoch: 100,
	}
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "status-checkpoint.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	documentReceipt, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RoleStatus, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpointDigest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	const timeAuthorityID = "release-test-clock"
	checkpointPolicy, err := status.NewCheckpointPolicy(target, authority, issuer, []string{signer})
	if releaseGrade {
		checkpointPolicy, err = status.NewReleaseCheckpointPolicy(target, authority, issuer,
			[]string{signer}, timeAuthorityID)
	}
	if err != nil {
		t.Fatal(err)
	}
	highWater, err := status.NewHighWaterMark(target, authority, 0, checkpointDigest)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointReceipt status.CheckpointReceipt
	if releaseGrade {
		timeAuthority, authorityErr := status.NewTimeAuthority(timeAuthorityID,
			status.EpochSourceFunc(func(context.Context) (int64, error) { return asOfEpoch, nil }))
		if authorityErr != nil {
			t.Fatal(authorityErr)
		}
		trustedTime, timeErr := timeAuthority.Now(context.Background())
		if timeErr != nil {
			t.Fatal(timeErr)
		}
		checkpointReceipt, err = status.VerifyCheckpointForRelease(context.Background(), status.ReleaseCheckpointRequest{
			Policy: checkpointPolicy, HighWater: highWater, Time: trustedTime, Checkpoint: documentReceipt,
			Declarations: declarationInputs,
		})
	} else {
		asOf, asOfErr := status.At(asOfEpoch)
		if asOfErr != nil {
			t.Fatal(asOfErr)
		}
		checkpointReceipt, err = status.VerifyCheckpoint(context.Background(), status.CheckpointRequest{
			Policy: checkpointPolicy, HighWater: highWater, AsOf: asOf, Checkpoint: documentReceipt,
			Declarations: declarationInputs,
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	return checkpointReceipt
}

func verifiedPromotionPolicy(t *testing.T, compatibilityPolicyDigest, conformancePolicyDigest string,
	rules []PromotionRule,
) PromotionPolicyReceipt {
	t.Helper()
	policy := PromotionPolicy{
		SchemaVersion:             PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: compatibilityPolicyDigest,
		ConformancePolicyDigest:   conformancePolicyDigest,
		Rules:                     rules,
		EffectiveEpoch:            1,
		ExpiresEpoch:              20,
	}
	raw, err := policy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "promotion-policy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	const signer = "promotion-policy-signer"
	const issuer = "https://promotion-policy-issuer.example.test"
	authenticator, err := trust.NewDocumentAuthenticator(
		statusDocumentVerifier{signer: signer, issuer: issuer},
		trust.JSONSchema[PromotionPolicy](PromotionPolicySchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RolePromotionPolicy,
		trust.Store{OperatorIdentities: []string{signer}}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RolePromotionPolicy, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := VerifyPromotionPolicy(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func vulnerabilityPromotionRule(kind ArtifactKind) PromotionRule {
	return PromotionRule{ArtifactKind: kind, Evidence: []EvidenceRequirement{{Type: EvidenceVulnerability}}}
}

func verifiedReleaseSet(t *testing.T, set *ReleaseSet, signer string) ReleaseSetReceipt {
	t.Helper()
	raw, err := set.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-set.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	const issuer = "https://release-set-issuer.example.test"
	authenticator, err := trust.NewDocumentAuthenticator(
		statusDocumentVerifier{signer: signer, issuer: issuer},
		trust.JSONSchema[ReleaseSet](ReleaseSetSchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleReleaseSet,
		trust.Store{OperatorIdentities: []string{signer}}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RoleReleaseSet, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := VerifyAuthenticatedReleaseSet(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func inventory() *Inventory {
	return &Inventory{
		SchemaVersion: InventorySchemaVersion,
		Version:       "1.0",
		Components: []InventoryComponent{
			{Name: "kubelet", Role: "worker", VersionConstraint: "1.32.x", RequiredArtifacts: []string{digest('a')},
				ArtifactKinds: []ArtifactKindBinding{{ArtifactDigest: digest('a'), Kind: ArtifactHostPackage}}},
			{Name: "apiserver", Role: "control-plane", VersionConstraint: ">=1.32 <1.33", RequiredArtifacts: []string{digest('b')},
				ArtifactKinds: []ArtifactKindBinding{{ArtifactDigest: digest('b'), Kind: ArtifactHostPackage}}},
		},
		Compatibility: Compatibility{Kubernetes: "1.32.x", NKE: ">=2.4 <3.0", Platform: "bare-metal|eks"},
	}
}

func releaseSet(t *testing.T, inv *Inventory) *ReleaseSet {
	t.Helper()
	invDigest, err := inv.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &ReleaseSet{
		SchemaVersion: ReleaseSetSchemaVersion,
		Version:       "1.0",
		InventoryRef:  invDigest,
		Members: []Member{
			{
				Component: "kubelet", Role: "worker", Arch: "amd64", Version: "1.32.7",
				RecipeDigest: digest('c'), VectorDigest: digest('d'), ResolvedInputDigest: digest('3'),
				ArtifactDigests: []string{digest('a')},
			},
			{
				Component: "apiserver", Role: "control-plane", Arch: "amd64", Version: "1.32.7",
				RecipeDigest: digest('f'), VectorDigest: digest('1'), ResolvedInputDigest: digest('5'),
				ArtifactDigests: []string{digest('b')},
			},
		},
	}
}

func TestReleaseDocumentsRejectNonCanonicalAndEmptyIdentityFields(t *testing.T) {
	for name, mutate := range map[string]func(*Inventory){
		"version whitespace": func(value *Inventory) { value.Version = " 1.0" },
		"member whitespace":  func(value *Inventory) { value.Components[0].Name = "kubelet " },
		"role whitespace":    func(value *Inventory) { value.Components[0].Role = " worker" },
		"no artifacts": func(value *Inventory) {
			value.Components[0].RequiredArtifacts = nil
			value.Components[0].ArtifactKinds = nil
		},
	} {
		t.Run("inventory "+name, func(t *testing.T) {
			value := inventory()
			mutate(value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid inventory identity must fail closed")
			}
		})
	}
	baseInventory := inventory()
	for name, mutate := range map[string]func(*ReleaseSet){
		"version whitespace": func(value *ReleaseSet) { value.Version = "1.0 " },
		"component whitespace": func(value *ReleaseSet) {
			value.Members[0].Component = " kubelet"
		},
		"unsupported arch": func(value *ReleaseSet) { value.Members[0].Arch = "s390x" },
		"no artifacts":     func(value *ReleaseSet) { value.Members[0].ArtifactDigests = nil },
	} {
		t.Run("release set "+name, func(t *testing.T) {
			value := releaseSet(t, baseInventory)
			mutate(value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid release-set identity must fail closed")
			}
		})
	}
}

func target() CompatibilityTarget {
	return CompatibilityTarget{Kubernetes: "1.32.7", NKE: "2.5.1", Platform: "bare-metal"}
}

func releaseFixtureArtifactBytes(component, role string) []byte {
	return []byte("release-fixture-artifact/" + component + "/" + role)
}

func rawFixtureDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeReleasePipelineResult(t *testing.T, dir, nodeID string, result pipeline.ArtifactResult) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nodeID+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func qualifiedPublisherFixture(t *testing.T, component, role, version string, artifactBytes []byte) (
	*recipe.Resolved, *pipeline.Report, *attest.PublisherEvidence,
) {
	t.Helper()
	if version == "" {
		version = "1.0.0"
	}
	artifactDigest := rawFixtureDigest(artifactBytes)
	resolved := &recipe.Resolved{
		RecipeID: "release-" + component,
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: component, Component: catalog.Component{
			Tag: version, SourceDate: 1, OutputType: "host-package",
			Prebuilt: &catalog.Prebuilt{URL: "https://artifacts.example.test/" + component,
				Digests:  map[string]string{"amd64": artifactDigest},
				Evidence: &catalog.ExternalEvidence{None: true}},
		}}},
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	options := pipeline.RunOptions{Ctx: context.Background(), SourceRoot: filepath.Join(work, "src")}
	var pull ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindPull {
			pull = node
			break
		}
	}
	artifactPath := filepath.Join(filepath.Dir(options.SourceRoot), "fetch", component, pull.ArtifactPath)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, artifactBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	writeReleasePipelineResult(t, pipeline.ResultDirectory(options.SourceRoot), pull.ID, pipeline.ArtifactResult{
		Component: component, OutputType: pull.OutputType,
		Qualification: pipeline.ArtifactProfileFreeAcquisition,
		EngineClass:   ir.EnginePrebuiltAcquire, Path: pull.ArtifactPath,
		SHA256: artifactDigest, Size: int64(len(artifactBytes)),
	})
	report, err := (pipeline.Local{}).VerifyResults(graph, recipeDigest,
		pipeline.ResultDirectory(options.SourceRoot), options)
	if err != nil {
		t.Fatal(err)
	}
	verifiedSBOM, err := sbom.BuildVerifiedFromReport(context.Background(), resolved, report, nil)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedProvenance, err := provenance.BuildQualifiedFromReport(context.Background(), report, graph)
	if err != nil {
		t.Fatal(err)
	}
	publisherEvidence, err := attest.QualifyPublisherEvidence(context.Background(), verifiedSBOM, qualifiedProvenance)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, report, publisherEvidence
}

func verifiedValidationVector(t *testing.T, report *pipeline.Report,
	evidence *attest.PublisherEvidence,
) (attest.PublishedVectorReceipt, string) {
	t.Helper()
	manifest, err := attest.FromQualifiedReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.AttachPublisherEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.MarshalQualified(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manifest.Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "validation-vector.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	const signer = "validation-vector-signer"
	const issuer = "https://validation-vector-issuer.example.test"
	authenticator, err := trust.NewDocumentAuthenticator(
		statusDocumentVerifier{signer: signer, issuer: issuer},
		trust.JSONSchema[attest.Manifest](attest.SchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewRolePolicy(trust.RoleValidationVector,
		trust.Store{OperatorIdentities: []string{signer}}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RoleValidationVector, Policy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := attest.VerifyPublishedVectorReceipt(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, identity
}

func authorizationRequest(t *testing.T, inv *Inventory, set *ReleaseSet) AuthorizationRequest {
	t.Helper()
	var validationVectors []attest.PublishedVectorReceipt
	var localSBOMs []sbom.LocalEvidenceReceipt
	var localProvenance []provenance.LocalEvidenceReceipt
	for memberIndex := range set.Members {
		member := &set.Members[memberIndex]
		artifactBytes := releaseFixtureArtifactBytes(member.Component, member.Role)
		artifactDigest := rawFixtureDigest(artifactBytes)
		for componentIndex := range inv.Components {
			component := &inv.Components[componentIndex]
			if component.Name != member.Component || (component.Role != "" && component.Role != member.Role) {
				continue
			}
			kind := ArtifactHostPackage
			if len(component.ArtifactKinds) == 1 {
				kind = component.ArtifactKinds[0].Kind
			}
			component.RequiredArtifacts = []string{artifactDigest}
			component.ArtifactKinds = []ArtifactKindBinding{{ArtifactDigest: artifactDigest, Kind: kind}}
			for gapIndex := range component.EvidenceGaps {
				component.EvidenceGaps[gapIndex].ArtifactDigest = artifactDigest
			}
		}
		resolved, report, publisherEvidence := qualifiedPublisherFixture(t,
			member.Component, member.Role, member.Version, artifactBytes)
		graph, err := recipe.Compile(resolved)
		if err != nil {
			t.Fatal(err)
		}
		recipeDigest, err := graph.Digest()
		if err != nil {
			t.Fatal(err)
		}
		vector, vectorDigest := verifiedValidationVector(t, report, publisherEvidence)
		sboms, provenances, err := publisherEvidence.LocalReceipts()
		if err != nil {
			t.Fatal(err)
		}
		member.RecipeDigest = recipeDigest
		member.VectorDigest = vectorDigest
		member.ProfileDigest = ""
		member.ArtifactDigests = []string{artifactDigest}
		validationVectors = append(validationVectors, vector)
		localSBOMs = append(localSBOMs, sboms...)
		localProvenance = append(localProvenance, provenances...)
	}
	invDigest, err := inv.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set.InventoryRef = invDigest
	releaseReceipt := verifiedReleaseSet(t, set, "release-signer")
	releaseDigest, err := set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publicationIdentity, err := set.PublicationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	authenticator := receiptAuthenticator{signers: map[ReceiptRole]string{
		RoleReproduction: "reproduction-signer", RoleCompatibility: "compatibility-signer",
		RoleConformance: "conformance-signer",
	}}
	req := AuthorizationRequest{
		Release: releaseReceipt, Inventory: inv, Target: target(),
		ValidationVectors: validationVectors,
		LocalSBOMEvidence: localSBOMs, LocalProvenanceEvidence: localProvenance,
		PromotionPolicy: verifiedPromotionPolicy(t, digest('8'), digest('9'),
			[]PromotionRule{vulnerabilityPromotionRule(ArtifactHostPackage)}),
		AsOfEpoch: 10,
		Status:    verifiedStatus(t, publicationIdentity, "status-authority", 10),
	}
	for _, member := range set.Members {
		result := ReproductionResult{
			SchemaVersion: ReproductionSchemaVersion, ReleaseSetDigest: releaseDigest,
			Component: member.Component, Role: member.Role, Arch: member.Arch,
			RecipeDigest: member.RecipeDigest, VectorDigest: member.VectorDigest,
			ProfileDigest: member.ProfileDigest, ProfileIndependent: member.ProfileDigest == "",
			ResolvedInputDigest: member.ResolvedInputDigest,
			ArtifactDigests:     append([]string(nil), member.ArtifactDigests...), Verdict: Reproduced,
			EffectiveEpoch: 1, ExpiresEpoch: 20,
		}
		receipt, err := VerifyReproduction(context.Background(), result,
			[]byte("reproduction-signature"), releaseReceipt, authenticator)
		if err != nil {
			t.Fatal(err)
		}
		req.Reproduction = append(req.Reproduction, receipt)
		for _, artifactDigest := range member.ArtifactDigests {
			var localSBOM sbom.LocalEvidenceReceipt
			for _, candidate := range req.LocalSBOMEvidence {
				binding, bindingErr := candidate.Binding()
				if bindingErr != nil {
					t.Fatal(bindingErr)
				}
				if binding.Component == member.Component && binding.RecipeDigest == member.RecipeDigest &&
					binding.ArtifactDigest == artifactDigest {
					localSBOM = candidate
					break
				}
			}
			req.VulnerabilityReports = append(req.VulnerabilityReports,
				qualifiedReport(t, member.RecipeDigest, artifactDigest, localSBOM))
		}
	}
	recipes, vectors, artifacts := releaseIdentitySets(set.Members)
	compatibility, err := VerifyCompatibility(context.Background(), CompatibilityResult{
		SchemaVersion: CompatibilitySchemaVersion, ReleaseSetDigest: releaseDigest, InventoryDigest: invDigest,
		CompatibilityPolicyDigest: req.PromotionPolicy.policy.CompatibilityPolicyDigest, Target: req.Target,
		RecipeDigests: recipes, VectorDigests: vectors, ArtifactDigests: artifacts,
		Verdict: CompatibilityAccepted, EffectiveEpoch: 1, ExpiresEpoch: 20,
	}, []byte("compatibility-signature"), releaseReceipt, inv, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	req.Compatibility = compatibility
	conformance, err := AuthenticateConformance(context.Background(), ConformanceResult{
		SchemaVersion: ConformanceSchemaVersion, ReleaseSetDigest: releaseDigest, InventoryDigest: invDigest,
		VectorDigests: vectors, Suite: "k8s", SuitePolicyDigest: req.PromotionPolicy.policy.ConformancePolicyDigest,
		Verdict: ConformancePassed, ResultsDigest: digest('0'), EffectiveEpoch: 1, ExpiresEpoch: 20,
	}, []byte("conformance-signature"), releaseReceipt, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	req.Conformance = []ConformanceReceipt{conformance}
	return req
}

func TestAuthorizeIssuesPrivateCapabilityOnlyAfterAllChecks(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	authorization, err := Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("complete compatible release must authorize: %v", err)
	}
	if _, err := authorization.Digest(); err != nil {
		t.Fatalf("authorization must be digest-addressable: %v", err)
	}
	artifacts, err := authorization.ArtifactDigests()
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("authorization must bind exact artifacts: %v %v", artifacts, err)
	}
	if _, err := (&Authorization{}).ArtifactDigests(); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("zero-value capability must fail closed: %v", err)
	}
}

func TestPackageOnlyReleaseRequiresExactAuthenticatedVectorChain(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	authorization, err := Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("complete package-only vector/evidence chain was rejected: %v", err)
	}
	publication, err := authorization.PublicationManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(publication.Referrers) != 0 {
		t.Fatalf("package-only authorization invented OCI referrers: %+v", publication.Referrers)
	}

	omitted := req
	omitted.ValidationVectors = append([]attest.PublishedVectorReceipt(nil), req.ValidationVectors[1:]...)
	if _, err := Authorize(context.Background(), omitted); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("package-only release omitted a member's authenticated vector: %v", err)
	}

	substituted := req
	substituted.ValidationVectors = append([]attest.PublishedVectorReceipt(nil), req.ValidationVectors...)
	substituted.ValidationVectors[0] = substituted.ValidationVectors[1]
	if _, err := Authorize(context.Background(), substituted); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("package-only release substituted another member's vector: %v", err)
	}

	wrongEvidence := req
	wrongEvidence.LocalSBOMEvidence = append([]sbom.LocalEvidenceReceipt(nil), req.LocalSBOMEvidence...)
	wrongEvidence.LocalSBOMEvidence[0] = wrongEvidence.LocalSBOMEvidence[1]
	if _, err := Authorize(context.Background(), wrongEvidence); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("package-only vector accepted substituted local SBOM authority: %v", err)
	}
}

func TestPublicationManifestRejectsDuplicateSubjectMediaType(t *testing.T) {
	manifest := PublicationManifest{
		SchemaVersion:       PublicationManifestSchemaVersion,
		PublicationIdentity: digest('1'),
		ArtifactDigests:     []string{digest('a')},
		Referrers: []PublicationReferrer{
			{SubjectDigest: digest('a'), ArtifactType: ReferrerSBOM,
				IdentityDigest: digest('2'), BlobDigest: digest('3')},
			{SubjectDigest: digest('a'), ArtifactType: ReferrerSBOM,
				IdentityDigest: digest('4'), BlobDigest: digest('5')},
		},
	}
	if code(manifest.Validate()) != nkderr.CodeUnverifiedRef {
		t.Fatal("publication manifest collapsed duplicate subject/media-type rows")
	}
}

func TestCompletenessNamesMissingMemberAndArtifactWithoutTrustingRawEvidence(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	set.Members = set.Members[:1]
	set.Members[0].ArtifactDigests = []string{digest('9')}
	err := set.CompletenessGate(inv)
	if code(err) != nkderr.CodeReleaseIncomplete {
		t.Fatalf("incomplete release must fail closed: %v", err)
	}
	for _, want := range []string{"apiserver@control-plane", "artifact " + digest('a')} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("completeness error must name %q: %v", want, err)
		}
	}
}

func TestAuthorizeRejectsCompatibilityAndReceiptSubstitution(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	req.Target.NKE = "3.1.0"
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeCompatMismatch {
		t.Fatalf("out-of-range target must fail compatibility: %v", err)
	}

	req = authorizationRequest(t, inv, set)
	req.Reproduction[0].result.ArtifactDigests[0] = digest('9')
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("receipt substitution must fail exact binding: %v", err)
	}

	req = authorizationRequest(t, inv, set)
	req.Conformance[0].result.ResultsDigest = digest('9')
	if _, err := Authorize(context.Background(), req); err == nil {
		t.Fatal("mutating a conformance receipt must fail")
	}

	set = releaseSet(t, inv)
	set.Members[0].Version = "1.31.9"
	if err := checkMemberCompatibility(inv, set); code(err) != nkderr.CodeCompatMismatch {
		t.Fatalf("member version outside inventory constraint must fail before receipt issuance: %v", err)
	}
}

func TestConformanceRequiresIndependentVerifiedSigner(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	releaseReceipt := verifiedReleaseSet(t, set, "same-signer")
	releaseDigest, _ := set.Digest()
	invDigest, _ := inv.Digest()
	_, vectors, _ := releaseIdentitySets(set.Members)
	result := ConformanceResult{
		SchemaVersion: ConformanceSchemaVersion, ReleaseSetDigest: releaseDigest, InventoryDigest: invDigest,
		VectorDigests: vectors, Suite: "k8s", SuitePolicyDigest: digest('7'), Verdict: ConformancePassed,
		ResultsDigest: digest('8'), EffectiveEpoch: 1, ExpiresEpoch: 2,
	}
	sameSigner := receiptAuthenticator{signers: map[ReceiptRole]string{RoleConformance: "same-signer"}}
	if _, err := AuthenticateConformance(context.Background(), result, []byte("sig"), releaseReceipt, sameSigner); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("release signer cannot also issue conformance receipt: %v", err)
	}
	result.ResultsDigest = "not-a-digest"
	otherSigner := receiptAuthenticator{signers: map[ReceiptRole]string{RoleConformance: "other"}}
	if _, err := AuthenticateConformance(context.Background(), result, []byte("sig"), releaseReceipt, otherSigner); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("conformance must bind exact immutable results: %v", err)
	}
}

func TestIncrementalCandidateDiffRequiresExactMemberDeclaration(t *testing.T) {
	inv := inventory()
	previous := releaseSet(t, inv)
	mutations := []struct {
		name   string
		mutate func(*Member)
	}{
		{name: "role", mutate: func(member *Member) { member.Role = "gpu-worker" }},
		{name: "artifact", mutate: func(member *Member) { member.ArtifactDigests = []string{digest('9')} }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			next := releaseSet(t, inv)
			test.mutate(&next.Members[0])
			revalidate, reused := IncrementalPlan(previous, next)
			if len(revalidate) != 1 || len(reused) != 1 || revalidate[0].Component != "kubelet" {
				t.Fatalf("changed identity must revalidate exactly one member: revalidate=%+v reused=%+v", revalidate, reused)
			}
		})
	}
}

func TestRollbackRecomputesIdentityAndRerunsClearanceAndStatus(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	targetDigest, err := req.Release.set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publicationIdentity, err := set.PublicationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := verifiedStatusCheckpoint(t, publicationIdentity, "status-authority", 10, nil)
	rollback, err := AuthorizeRollback(context.Background(), RollbackRequest{
		TargetDigest: targetDigest, Candidates: []AuthorizationRequest{req}, Status: checkpoint,
	})
	if err != nil || rollback.ReleaseSet() == nil || rollback.Clearance() == nil {
		t.Fatalf("current checks should re-authorize rollback: %v", err)
	}
	decision := rollback.Status()
	if !isDigest(decision.CheckpointDigest) || !isDigest(decision.PolicyBinding) || decision.AsOfEpoch != 10 {
		t.Fatalf("rollback must expose the exact accepted status checkpoint and policy: %+v", decision)
	}

	wrongTarget := digest('9')
	wrongCheckpoint := verifiedStatusCheckpoint(t, wrongTarget, "status-authority", 10, nil)
	if _, err := AuthorizeRollback(context.Background(), RollbackRequest{
		TargetDigest: wrongTarget, Candidates: []AuthorizationRequest{req}, Status: wrongCheckpoint,
	}); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("target absent from recomputed candidate identities must fail: %v", err)
	}
	if _, err := AuthorizeRollback(context.Background(), RollbackRequest{
		TargetDigest: targetDigest, Candidates: []AuthorizationRequest{req}, Status: status.CheckpointReceipt{},
	}); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero status checkpoint capability must fail closed: %v", err)
	}
	withdrawn := status.StatusDeclaration{SchemaVersion: status.SchemaVersion, Target: publicationIdentity, Action: status.ActionWithdrawn}
	withdrawnCheckpoint := verifiedStatusCheckpoint(t, publicationIdentity, "status-authority", 10, &withdrawn)
	if _, err := AuthorizeRollback(context.Background(), RollbackRequest{
		TargetDigest: targetDigest, Candidates: []AuthorizationRequest{req}, Status: withdrawnCheckpoint,
	}); code(err) != nkderr.CodeWithdrawn {
		t.Fatalf("withdrawal outside the authenticated checkpoint path must never be bypassed: %v", err)
	}

	lateCheckpoint := verifiedStatusCheckpoint(t, publicationIdentity, "status-authority", 21, nil)
	if _, err := AuthorizeRollback(context.Background(), RollbackRequest{
		TargetDigest: targetDigest, Candidates: []AuthorizationRequest{req}, Status: lateCheckpoint,
	}); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("rollback must re-evaluate receipt and policy expiry at checkpoint as-of: %v", err)
	}

}
