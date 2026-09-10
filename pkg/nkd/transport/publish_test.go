package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/recipe"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/release"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/vuln"
)

type receiptAuthenticator struct{}

func (receiptAuthenticator) AuthenticateReceipt(_ context.Context, _, _ []byte, role release.ReceiptRole) (string, error) {
	switch role {
	case release.RoleReproduction:
		return "reproduction-signer", nil
	case release.RoleCompatibility:
		return "compatibility-signer", nil
	case release.RoleConformance:
		return "conformance-signer", nil
	default:
		return "", errors.New("unexpected release receipt role")
	}
}

type qualifiedScanner struct{ observation vuln.ScanObservation }

func (scanner qualifiedScanner) Scan(context.Context, vuln.QualifiedScanRequest) (vuln.ScanObservation, error) {
	return scanner.observation, nil
}

type snapshotProbeOCI struct {
	originals []string
	expected  map[string][]byte
	mutated   bool
}

func (probe *snapshotProbeOCI) Push(_ context.Context, _ Target, artifactPath, digest string) (string, error) {
	for _, path := range probe.originals {
		if err := os.WriteFile(path, []byte("swapped-after-preflight"), 0o600); err != nil {
			return "", err
		}
	}
	probe.mutated = true
	if err := probe.checkSnapshot(artifactPath, digest); err != nil {
		return "", err
	}
	return "snapshot-probe@" + digest, nil
}

func (probe *snapshotProbeOCI) AttachReferrer(_ context.Context, _ Target, _ string, _ string,
	blobPath, blobDigest string,
) error {
	return probe.checkSnapshot(blobPath, blobDigest)
}

func (probe *snapshotProbeOCI) checkSnapshot(path, digest string) error {
	for _, original := range probe.originals {
		if path == original {
			return errors.New("adapter received caller-controlled path")
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	want, ok := probe.expected[digest]
	if !ok || string(got) != string(want) {
		return errors.New("adapter did not receive the authorized snapshot bytes")
	}
	return nil
}

type statusVerifier struct{}

func (statusVerifier) VerifyDocument(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{"status-signer"}, OIDCIssuer: "https://status-issuer.example.test",
		CertificateSHA256: fixedDigest('e'),
	}, nil
}

type promotionPolicyVerifier struct{}

func (promotionPolicyVerifier) VerifyDocument(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{"promotion-policy-signer"},
		OIDCIssuer:            "https://promotion-policy-issuer.example.test",
		CertificateSHA256:     fixedDigest('9'),
	}, nil
}

type releaseSetVerifier struct{}

func (releaseSetVerifier) VerifyDocument(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{"release-set-signer"},
		OIDCIssuer:            "https://release-set-issuer.example.test",
		CertificateSHA256:     fixedDigest('8'),
	}, nil
}

type validationVectorVerifier struct{}

func (validationVectorVerifier) VerifyDocument(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{"validation-vector-signer"},
		OIDCIssuer:            "https://validation-vector-issuer.example.test",
		CertificateSHA256:     fixedDigest('7'),
	}, nil
}

func code(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func digestBytes(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var transportArtifactPayloads sync.Map

func fixedDigest(char byte) string { return "sha256:" + strings.Repeat(string(char), 64) }

func writeArtifact(t *testing.T, dir, name string, bytes []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	transportArtifactPayloads.Store(digestBytes(bytes), append([]byte(nil), bytes...))
	return path
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
	canonicalSBOM, err := localSBOM.ExactBytes()
	if err != nil {
		t.Fatal(err)
	}
	request := vuln.QualifiedScanRequest{
		Scanner: vuln.ScannerGrype, ScannerDigest: fixedDigest('d'), DatabaseDigest: fixedDigest('e'),
		RecipeDigest: recipeDigest, ArtifactDigest: artifactDigest,
		SBOMDigest: binding.EvidenceDigest, CanonicalSBOM: canonicalSBOM,
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

func verifiedStatusWithBytes(t *testing.T, target string) (release.StatusBinding, []byte) {
	t.Helper()
	checkpoint := status.StatusCheckpoint{
		SchemaVersion: status.CheckpointSchemaVersion, Target: target, Authority: "status-authority",
		Sequence: 0, DeclarationDigests: []string{}, IssuedEpoch: 1, ValidUntilEpoch: 20,
	}
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeArtifact(t, t.TempDir(), "checkpoint.json", raw)
	authenticator, err := trust.NewDocumentAuthenticator(
		statusVerifier{}, trust.JSONSchema[status.StatusCheckpoint](status.CheckpointSchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleStatus,
		trust.Store{OperatorIdentities: []string{"status-signer"}}, "https://status-issuer.example.test")
	if err != nil {
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
	const timeAuthorityID = "transport-test-clock"
	checkpointPolicy, err := status.NewReleaseCheckpointPolicy(target, "status-authority",
		"https://status-issuer.example.test", []string{"status-signer"}, timeAuthorityID)
	if err != nil {
		t.Fatal(err)
	}
	highWater, err := status.NewHighWaterMark(target, "status-authority", 0, checkpointDigest)
	if err != nil {
		t.Fatal(err)
	}
	timeAuthority, err := status.NewTimeAuthority(timeAuthorityID,
		status.EpochSourceFunc(func(context.Context) (int64, error) { return 10, nil }))
	if err != nil {
		t.Fatal(err)
	}
	trustedTime, err := timeAuthority.Now(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpointReceipt, err := status.VerifyCheckpointForRelease(context.Background(), status.ReleaseCheckpointRequest{
		Policy: checkpointPolicy, HighWater: highWater, Time: trustedTime, Checkpoint: documentReceipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := release.BindVerifiedStatusCheckpoint(checkpointReceipt)
	if err != nil {
		t.Fatal(err)
	}
	return binding, raw
}

func verifiedPromotionPolicy(t *testing.T, compatibilityPolicy, conformancePolicy string,
	artifactKind release.ArtifactKind,
) release.PromotionPolicyReceipt {
	t.Helper()
	policy := release.PromotionPolicy{
		SchemaVersion:             release.PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: compatibilityPolicy,
		ConformancePolicyDigest:   conformancePolicy,
		Rules: []release.PromotionRule{{
			ArtifactKind: artifactKind,
			Evidence:     []release.EvidenceRequirement{{Type: release.EvidenceVulnerability}},
		}},
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}
	raw, err := policy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeArtifact(t, t.TempDir(), "promotion-policy.json", raw)
	authenticator, err := trust.NewDocumentAuthenticator(
		promotionPolicyVerifier{}, trust.JSONSchema[release.PromotionPolicy](release.PromotionPolicySchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RolePromotionPolicy,
		trust.Store{OperatorIdentities: []string{"promotion-policy-signer"}},
		"https://promotion-policy-issuer.example.test")
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RolePromotionPolicy, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := release.VerifyPromotionPolicy(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

type vectorEvidenceFixture struct {
	recipeDigest string
	vectorDigest string
	receipt      attest.PublishedVectorReceipt
	raw          []byte
	sboms        []sbom.LocalEvidenceReceipt
	provenances  []provenance.LocalEvidenceReceipt
}

func qualifiedValidationVector(t *testing.T, component string, artifactBytes []byte) vectorEvidenceFixture {
	t.Helper()
	artifactDigest := digestBytes(artifactBytes)
	resolved := &recipe.Resolved{RecipeID: "transport-" + component,
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: component, Component: catalog.Component{
			Tag: "v1.0.0", SourceDate: 1, OutputType: "host-package",
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
	result := pipeline.ArtifactResult{Component: component, OutputType: pull.OutputType,
		Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire,
		Path: pull.ArtifactPath, SHA256: artifactDigest, Size: int64(len(artifactBytes))}
	resultBytes, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	resultsDir := pipeline.ResultDirectory(options.SourceRoot)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, pull.ID+".json"), resultBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := (pipeline.Local{}).VerifyResults(graph, recipeDigest, resultsDir, options)
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
	manifest, err := attest.FromQualifiedReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.AttachPublisherEvidence(publisherEvidence); err != nil {
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
	path := writeArtifact(t, t.TempDir(), "validation-vector.json", raw)
	authenticator, err := trust.NewDocumentAuthenticator(
		validationVectorVerifier{}, trust.JSONSchema[attest.Manifest](attest.SchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleValidationVector,
		trust.Store{OperatorIdentities: []string{"validation-vector-signer"}},
		"https://validation-vector-issuer.example.test")
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RoleValidationVector, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := attest.VerifyPublishedVectorReceipt(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	sbomReceipts, provenanceReceipts, err := publisherEvidence.LocalReceipts()
	if err != nil {
		t.Fatal(err)
	}
	return vectorEvidenceFixture{recipeDigest: recipeDigest, vectorDigest: identity,
		receipt: receipt, raw: raw, sboms: sbomReceipts, provenances: provenanceReceipts}
}

func verifiedReleaseSet(t *testing.T, set *release.ReleaseSet) release.ReleaseSetReceipt {
	t.Helper()
	raw, err := set.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeArtifact(t, t.TempDir(), "release-set.json", raw)
	authenticator, err := trust.NewDocumentAuthenticator(
		releaseSetVerifier{}, trust.JSONSchema[release.ReleaseSet](release.ReleaseSetSchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleReleaseSet,
		trust.Store{OperatorIdentities: []string{"release-set-signer"}},
		"https://release-set-issuer.example.test")
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RoleReleaseSet, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := release.VerifyAuthenticatedReleaseSet(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

type publicationAuthorizationFixture struct {
	authorization *release.Authorization
	blobs         map[string]map[string][]byte
}

func authorizeArtifacts(t *testing.T, digests []string) *release.Authorization {
	t.Helper()
	return authorizeArtifactsForKind(t, digests, release.ArtifactRaw).authorization
}

func authorizeOCIArtifacts(t *testing.T, digests []string) publicationAuthorizationFixture {
	t.Helper()
	return authorizeArtifactsForKind(t, digests, release.ArtifactOCIImage)
}

func authorizeArtifactsForKind(t *testing.T, digests []string,
	artifactKind release.ArtifactKind,
) publicationAuthorizationFixture {
	t.Helper()
	inv := &release.Inventory{SchemaVersion: release.InventorySchemaVersion, Version: "1"}
	set := &release.ReleaseSet{SchemaVersion: release.ReleaseSetSchemaVersion, Version: "1"}
	var vectorReceipts []attest.PublishedVectorReceipt
	var localSBOMs []sbom.LocalEvidenceReceipt
	var localProvenances []provenance.LocalEvidenceReceipt
	vectorBytes := map[string][]byte{}
	evidenceBytes := map[string]map[string][]byte{}
	for index, digest := range digests {
		payloadValue, ok := transportArtifactPayloads.Load(digest)
		if !ok {
			t.Fatalf("no artifact bytes registered for release digest %s", digest)
		}
		payload := append([]byte(nil), payloadValue.([]byte)...)
		component := "artifact-" + string(rune('a'+index))
		fixture := qualifiedValidationVector(t, component, payload)
		inv.Components = append(inv.Components, release.InventoryComponent{
			Name: component, Role: "worker", VersionConstraint: "1.x",
			RequiredArtifacts: []string{digest},
			ArtifactKinds:     []release.ArtifactKindBinding{{ArtifactDigest: digest, Kind: artifactKind}},
		})
		set.Members = append(set.Members, release.Member{
			Component: component, Role: "worker", Arch: "amd64", Version: "1.0.0",
			RecipeDigest: fixture.recipeDigest, VectorDigest: fixture.vectorDigest,
			ResolvedInputDigest: fixedDigest(byte('a' + index)), ArtifactDigests: []string{digest},
		})
		vectorReceipts = append(vectorReceipts, fixture.receipt)
		vectorBytes[fixture.vectorDigest] = append([]byte(nil), fixture.raw...)
		localSBOMs = append(localSBOMs, fixture.sboms...)
		localProvenances = append(localProvenances, fixture.provenances...)
		if evidenceBytes[digest] == nil {
			evidenceBytes[digest] = map[string][]byte{}
		}
		for _, receipt := range fixture.sboms {
			raw, err := receipt.ExactBytes()
			if err != nil {
				t.Fatal(err)
			}
			evidenceBytes[digest][release.ReferrerSBOM] = raw
		}
		for _, receipt := range fixture.provenances {
			raw, err := receipt.ExactBytes()
			if err != nil {
				t.Fatal(err)
			}
			evidenceBytes[digest][release.ReferrerProvenance] = raw
		}
	}
	invDigest, err := inv.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set.InventoryRef = invDigest
	releaseReceipt := verifiedReleaseSet(t, set)
	releaseDigest, err := set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publicationIdentity, err := set.PublicationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	authenticator := receiptAuthenticator{}
	var reproductions []release.ReproductionReceipt
	var recipes, vectors []string
	for _, member := range set.Members {
		reproduction, err := release.VerifyReproduction(context.Background(), release.ReproductionResult{
			SchemaVersion: release.ReproductionSchemaVersion, ReleaseSetDigest: releaseDigest,
			Component: member.Component, Role: member.Role, Arch: member.Arch,
			RecipeDigest: member.RecipeDigest, VectorDigest: member.VectorDigest,
			ProfileIndependent: true, ResolvedInputDigest: member.ResolvedInputDigest,
			ArtifactDigests: member.ArtifactDigests, Verdict: release.Reproduced,
			EffectiveEpoch: 1, ExpiresEpoch: 20,
		}, []byte("sig"), releaseReceipt, authenticator)
		if err != nil {
			t.Fatal(err)
		}
		reproductions = append(reproductions, reproduction)
		recipes = append(recipes, member.RecipeDigest)
		vectors = append(vectors, member.VectorDigest)
	}
	sort.Strings(recipes)
	sort.Strings(vectors)
	compatibility, err := release.VerifyCompatibility(context.Background(), release.CompatibilityResult{
		SchemaVersion: release.CompatibilitySchemaVersion, ReleaseSetDigest: releaseDigest,
		InventoryDigest: invDigest, CompatibilityPolicyDigest: fixedDigest('1'),
		RecipeDigests: recipes, VectorDigests: vectors,
		ArtifactDigests: append([]string(nil), digests...), Verdict: release.CompatibilityAccepted,
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}, []byte("sig"), releaseReceipt, inv, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	conformance, err := release.AuthenticateConformance(context.Background(), release.ConformanceResult{
		SchemaVersion: release.ConformanceSchemaVersion, ReleaseSetDigest: releaseDigest,
		InventoryDigest: invDigest, VectorDigests: vectors,
		Suite: "k8s", SuitePolicyDigest: fixedDigest('2'), Verdict: release.ConformancePassed,
		ResultsDigest: fixedDigest('d'), EffectiveEpoch: 1, ExpiresEpoch: 20,
	}, []byte("sig"), releaseReceipt, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	reports := make([]vuln.QualifiedReport, 0, len(digests))
	reportBytes := make(map[string][]byte, len(digests))
	for _, member := range set.Members {
		artifactDigest := member.ArtifactDigests[0]
		var localSBOM sbom.LocalEvidenceReceipt
		for _, candidate := range localSBOMs {
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
		report := qualifiedReport(t, member.RecipeDigest, artifactDigest, localSBOM)
		raw, err := report.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
		reportBytes[artifactDigest] = raw
	}
	statusBinding, statusBytes := verifiedStatusWithBytes(t, publicationIdentity)
	authorization, err := release.Authorize(context.Background(), release.AuthorizationRequest{
		Release: releaseReceipt, Inventory: inv, Reproduction: reproductions,
		Compatibility: compatibility, Conformance: []release.ConformanceReceipt{conformance},
		ValidationVectors: vectorReceipts, LocalSBOMEvidence: localSBOMs,
		LocalProvenanceEvidence: localProvenances,
		VulnerabilityReports:    reports, Status: statusBinding,
		PromotionPolicy: verifiedPromotionPolicy(t, fixedDigest('1'), fixedDigest('2'), artifactKind),
		AsOfEpoch:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs := make(map[string]map[string][]byte, len(digests))
	publication, err := authorization.PublicationManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, referrer := range publication.Referrers {
		if blobs[referrer.SubjectDigest] == nil {
			blobs[referrer.SubjectDigest] = map[string][]byte{}
		}
		var payload []byte
		switch referrer.ArtifactType {
		case release.ReferrerValidationVector:
			payload = vectorBytes[referrer.IdentityDigest]
		case release.ReferrerStatusCheckpoint:
			payload = statusBytes
		case release.ReferrerSBOM, release.ReferrerProvenance:
			payload = evidenceBytes[referrer.SubjectDigest][referrer.ArtifactType]
		case release.ReferrerVulnerability:
			payload = reportBytes[referrer.SubjectDigest]
		default:
			t.Fatalf("publication fixture does not know payload for %s", referrer.ArtifactType)
		}
		if digestBytes(payload) != referrer.BlobDigest {
			t.Fatalf("publication payload for %s does not match authorized blob %s", referrer.ArtifactType, referrer.BlobDigest)
		}
		blobs[referrer.SubjectDigest][referrer.ArtifactType] = append([]byte(nil), payload...)
	}
	return publicationAuthorizationFixture{authorization: authorization, blobs: blobs}
}

func publicationReferrers(t *testing.T, fixture publicationAuthorizationFixture,
	directory, subject string,
) []Referrer {
	t.Helper()
	manifest, err := fixture.authorization.PublicationManifest()
	if err != nil {
		t.Fatal(err)
	}
	var referrers []Referrer
	for index, required := range manifest.Referrers {
		if required.SubjectDigest != subject {
			continue
		}
		payload, ok := fixture.blobs[subject][required.ArtifactType]
		if !ok {
			t.Fatalf("missing fixture bytes for authorized referrer %s", required.ArtifactType)
		}
		path := writeArtifact(t, directory, "authorized-referrer-"+string(rune('a'+index))+".json", payload)
		referrers = append(referrers, Referrer{ArtifactType: required.ArtifactType, Path: path,
			IdentityDigest: required.IdentityDigest, Digest: required.BlobDigest})
	}
	return referrers
}

func TestPublishClearedExactSetWithAtomicFilesystemAdapters(t *testing.T) {
	work := t.TempDir()
	packageBytes := []byte("package-bytes")
	imageBytes := []byte("image-bytes")
	objectBytes := []byte("object-bytes")
	packagePath := writeArtifact(t, work, "kubelet.deb", packageBytes)
	imagePath := writeArtifact(t, work, "pause.oci", imageBytes)
	objectPath := writeArtifact(t, work, "disk.raw", objectBytes)
	digests := []string{digestBytes(packageBytes), digestBytes(imageBytes), digestBytes(objectBytes)}
	authorization := authorizeArtifacts(t, digests)
	set, err := bindClearedArtifactSetDiagnostic(authorization, []Artifact{
		{Name: "kubelet", Kind: "package", Path: packagePath, Digest: digests[0]},
		{Name: "pause", Kind: "oci", Path: imagePath, Digest: digests[1]},
		{Name: "disk", Kind: "object", Path: objectPath, Digest: digests[2]},
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := Targets{
		OCI: Target{Ref: filepath.Join(work, "registry")}, Package: Target{Ref: filepath.Join(work, "apt")},
		Object: Target{Ref: filepath.Join(work, "objects")},
	}
	if err := PublishCleared(context.Background(), Seams{}, targets, set); err != nil {
		t.Fatalf("cleared set must publish: %v", err)
	}
	for _, path := range []string{
		filepath.Join(targets.Package.Ref, "kubelet.deb"),
		filepath.Join(targets.OCI.Ref, safeName(digests[1])),
		filepath.Join(targets.Object.Ref, safeName(digests[2])),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing published path %s: %v", path, err)
		}
	}
}

func TestPublishClearedPublishesExactAuthorizedReferrers(t *testing.T) {
	work := t.TempDir()
	artifactBytes := []byte("authorized-image")
	artifactPath := writeArtifact(t, work, "image.oci", artifactBytes)
	artifactDigest := digestBytes(artifactBytes)
	fixture := authorizeOCIArtifacts(t, []string{artifactDigest})
	referrers := publicationReferrers(t, fixture, work, artifactDigest)
	set, err := bindClearedArtifactSetDiagnostic(fixture.authorization, []Artifact{{
		Name: "image", Kind: "oci", Path: artifactPath, Digest: artifactDigest, Referrers: referrers,
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Ref: filepath.Join(work, "registry")}
	if err := PublishCleared(context.Background(), Seams{}, Targets{OCI: target}, set); err != nil {
		t.Fatal(err)
	}
	for _, referrer := range referrers {
		path := filepath.Join(target.Ref, "referrers", safeName(artifactDigest), safeName(referrer.ArtifactType))
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading published %s: %v", referrer.ArtifactType, err)
		}
		if digestBytes(got) != referrer.Digest {
			t.Fatalf("published %s bytes do not match authorization", referrer.ArtifactType)
		}
	}
}

func TestPublishClearedAdaptersCannotReopenSwappedCallerPaths(t *testing.T) {
	work := t.TempDir()
	artifactBytes := []byte("immutable-image")
	artifactPath := writeArtifact(t, work, "image.oci", artifactBytes)
	artifactDigest := digestBytes(artifactBytes)
	fixture := authorizeOCIArtifacts(t, []string{artifactDigest})
	referrers := publicationReferrers(t, fixture, work, artifactDigest)
	set, err := bindClearedArtifactSetDiagnostic(fixture.authorization, []Artifact{{
		Name: "image", Kind: "oci", Path: artifactPath, Digest: artifactDigest, Referrers: referrers,
	}})
	if err != nil {
		t.Fatal(err)
	}
	probe := &snapshotProbeOCI{expected: map[string][]byte{artifactDigest: artifactBytes}}
	probe.originals = append(probe.originals, artifactPath)
	for _, referrer := range referrers {
		payload, err := os.ReadFile(referrer.Path)
		if err != nil {
			t.Fatal(err)
		}
		probe.originals = append(probe.originals, referrer.Path)
		probe.expected[referrer.Digest] = payload
	}
	if err := PublishCleared(context.Background(), Seams{OCI: probe},
		Targets{OCI: Target{Ref: "snapshot-probe"}}, set); err != nil {
		t.Fatalf("publication must use its frozen authorized snapshots: %v", err)
	}
	if !probe.mutated {
		t.Fatal("adversarial adapter did not mutate caller paths")
	}
}

func TestBindingRejectsMissingExtraAndDuplicateArtifacts(t *testing.T) {
	work := t.TempDir()
	firstBytes, secondBytes := []byte("one"), []byte("two")
	first := writeArtifact(t, work, "one.bin", firstBytes)
	second := writeArtifact(t, work, "two.bin", secondBytes)
	firstDigest, secondDigest := digestBytes(firstBytes), digestBytes(secondBytes)
	authorization := authorizeArtifacts(t, []string{firstDigest, secondDigest})
	if _, err := bindClearedArtifactSetDiagnostic(authorization, []Artifact{{Name: "one", Kind: "object", Path: first, Digest: firstDigest}}); code(err) != nkderr.CodeReleaseIncomplete {
		t.Fatalf("missing artifact must fail: %v", err)
	}
	if _, err := bindClearedArtifactSetDiagnostic(authorization, []Artifact{
		{Name: "one", Kind: "object", Path: first, Digest: firstDigest},
		{Name: "two", Kind: "object", Path: second, Digest: secondDigest},
		{Name: "extra", Kind: "object", Path: second, Digest: fixedDigest('e')},
	}); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("extra artifact must fail: %v", err)
	}
	if _, err := bindClearedArtifactSetDiagnostic(authorizeArtifacts(t, []string{firstDigest}), []Artifact{
		{Name: "one", Kind: "object", Path: first, Digest: firstDigest},
		{Name: "duplicate", Kind: "object", Path: first, Digest: firstDigest},
	}); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("duplicate digest must fail: %v", err)
	}
}

func TestPublishRehashesBeforeAnyWrite(t *testing.T) {
	work := t.TempDir()
	bytes := []byte("authorized")
	path := writeArtifact(t, work, "artifact.bin", bytes)
	digest := digestBytes(bytes)
	set, err := bindClearedArtifactSetDiagnostic(authorizeArtifacts(t, []string{digest}), []Artifact{{Name: "artifact", Kind: "object", Path: path, Digest: digest}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("substituted"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(work, "objects")
	if err := PublishCleared(context.Background(), Seams{}, Targets{Object: Target{Ref: target}}, set); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("post-clearance mutation must fail: %v", err)
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) != 0 {
		t.Fatalf("preflight failure wrote external state: %v", entries)
	}
}

func TestPublishRejectsReferrerMismatchBeforeArtifactWrite(t *testing.T) {
	work := t.TempDir()
	artifactBytes := []byte("image")
	path := writeArtifact(t, work, "image.oci", artifactBytes)
	digest := digestBytes(artifactBytes)
	fixture := authorizeOCIArtifacts(t, []string{digest})
	referrers := publicationReferrers(t, fixture, work, digest)
	if err := os.WriteFile(referrers[0].Path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := bindClearedArtifactSetDiagnostic(fixture.authorization, []Artifact{{
		Name: "image", Kind: "oci", Path: path, Digest: digest,
		Referrers: referrers,
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(work, "registry")
	if err := PublishCleared(context.Background(), Seams{}, Targets{OCI: Target{Ref: target}}, set); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("referrer mismatch must fail: %v", err)
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) != 0 {
		t.Fatalf("referrer preflight failure wrote artifact: %v", entries)
	}
}

func TestReferrerDestinationCollisionsFailBeforePublication(t *testing.T) {
	work := t.TempDir()
	artifactBytes := []byte("image")
	artifactPath := writeArtifact(t, work, "image.oci", artifactBytes)
	artifactDigest := digestBytes(artifactBytes)
	fixture := authorizeOCIArtifacts(t, []string{artifactDigest})
	authorized := publicationReferrers(t, fixture, work, artifactDigest)
	for name, testCase := range map[string]struct {
		referrers []Referrer
		want      nkderr.Code
	}{
		"duplicate type": {
			referrers: append(append([]Referrer(nil), authorized...), authorized[0]),
			want:      nkderr.CodeImmutableViolation,
		},
		"unmanifested encoded collision": {
			referrers: []Referrer{
				{ArtifactType: "application/vnd.nkd+a", Path: authorized[0].Path, IdentityDigest: fixedDigest('1'), Digest: authorized[0].Digest},
				{ArtifactType: "application/vnd.nkd_a", Path: authorized[0].Path, IdentityDigest: fixedDigest('2'), Digest: authorized[0].Digest},
			},
			want: nkderr.CodeGateUnmet,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := bindClearedArtifactSetDiagnostic(fixture.authorization, []Artifact{{
				Name: "image", Kind: "oci", Path: artifactPath, Digest: artifactDigest,
				Referrers: testCase.referrers,
			}})
			if code(err) != testCase.want {
				t.Fatalf("referrer set must fail closed with %s: %v", testCase.want, err)
			}
		})
	}
}

func TestPublishRejectsSymlinkedReferrerBeforeArtifactWrite(t *testing.T) {
	work := t.TempDir()
	artifactBytes := []byte("image")
	artifactPath := writeArtifact(t, work, "image.oci", artifactBytes)
	artifactDigest := digestBytes(artifactBytes)
	fixture := authorizeOCIArtifacts(t, []string{artifactDigest})
	referrers := publicationReferrers(t, fixture, work, artifactDigest)
	linkedReferrer := filepath.Join(work, "evidence-link.json")
	if err := os.Symlink(referrers[0].Path, linkedReferrer); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	referrers[0].Path = linkedReferrer
	set, err := bindClearedArtifactSetDiagnostic(fixture.authorization, []Artifact{{
		Name: "image", Kind: "oci", Path: artifactPath, Digest: artifactDigest,
		Referrers: referrers,
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(work, "registry")
	if err := PublishCleared(context.Background(), Seams{}, Targets{OCI: Target{Ref: target}}, set); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("symlinked referrer must fail before publication: %v", err)
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) != 0 {
		t.Fatalf("symlink preflight failure wrote artifact: %v", entries)
	}
}

func TestBindingRejectsNonCanonicalDigestAndNames(t *testing.T) {
	work := t.TempDir()
	bytes := []byte("artifact")
	path := writeArtifact(t, work, "artifact.bin", bytes)
	digest := digestBytes(bytes)
	authorization := authorizeArtifacts(t, []string{digest})
	upper := "sha256:" + strings.ToUpper(strings.TrimPrefix(digest, "sha256:"))
	if _, err := bindClearedArtifactSetDiagnostic(authorization, []Artifact{{
		Name: "artifact", Kind: "object", Path: path, Digest: upper,
	}}); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("uppercase digest must fail closed: %v", err)
	}
	if _, err := bindClearedArtifactSetDiagnostic(authorization, []Artifact{{
		Name: " artifact", Kind: "object", Path: path, Digest: digest,
	}}); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("non-canonical artifact name must fail closed: %v", err)
	}
	if _, err := bindClearedArtifactSetDiagnostic(authorization, []Artifact{{
		Name: "artifact", Kind: "oci", Path: path, Digest: digest,
		Referrers: []Referrer{{ArtifactType: "Application/Vnd.NKD", Path: path, Digest: digest}},
	}}); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("non-canonical referrer media type must fail closed: %v", err)
	}
}

func TestPackageDestinationCollisionFailsBeforeWrite(t *testing.T) {
	work := t.TempDir()
	firstBytes, secondBytes := []byte("first"), []byte("second")
	firstPath := writeArtifact(t, work, "a+b.deb", firstBytes)
	secondPath := writeArtifact(t, work, "a_b.deb", secondBytes)
	firstDigest, secondDigest := digestBytes(firstBytes), digestBytes(secondBytes)
	set, err := bindClearedArtifactSetDiagnostic(authorizeArtifacts(t, []string{firstDigest, secondDigest}), []Artifact{
		{Name: "first", Kind: "package", Path: firstPath, Digest: firstDigest},
		{Name: "second", Kind: "package", Path: secondPath, Digest: secondDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(work, "packages")
	if err := PublishCleared(context.Background(), Seams{}, Targets{Package: Target{Ref: target}}, set); code(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("encoded package-name collision must fail before publication: %v", err)
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) != 0 {
		t.Fatalf("package collision preflight wrote external state: %v", entries)
	}
}

func TestFilesystemAdaptersRejectOverwriteAndCancellation(t *testing.T) {
	work := t.TempDir()
	bytes := []byte("object")
	path := writeArtifact(t, work, "object.bin", bytes)
	digest := digestBytes(bytes)
	set, err := bindClearedArtifactSetDiagnostic(authorizeArtifacts(t, []string{digest}), []Artifact{{Name: "object", Kind: "object", Path: path, Digest: digest}})
	if err != nil {
		t.Fatal(err)
	}
	targets := Targets{Object: Target{Ref: filepath.Join(work, "objects")}}
	if err := PublishCleared(context.Background(), Seams{}, targets, set); err != nil {
		t.Fatal(err)
	}
	if err := PublishCleared(context.Background(), Seams{}, targets, set); code(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("overwrite must fail immutably: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	otherTargets := Targets{Object: Target{Ref: filepath.Join(work, "canceled")}}
	if err := PublishCleared(canceled, Seams{}, otherTargets, set); code(err) != nkderr.CodeTimeout {
		t.Fatalf("cancellation must be timeout-coded: %v", err)
	}
}

func TestEmptyCapabilityCannotAuthorize(t *testing.T) {
	err := PublishCleared(context.Background(), Seams{}, Targets{}, &ClearedArtifactSet{})
	if code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("empty capability must not authorize: %v", err)
	}
}

type interruptedReader struct{ delivered bool }

func (reader *interruptedReader) Read(buffer []byte) (int, error) {
	if reader.delivered {
		return 0, errors.New("interrupted")
	}
	reader.delivered = true
	return copy(buffer, []byte("partial")), nil
}

func TestAtomicAdapterLeavesNoDestinationOnInterruptedWrite(t *testing.T) {
	target := Target{Ref: t.TempDir()}
	digest := fixedDigest('f')
	err := (fsObject{}).Put(context.Background(), target, digest, digest, &interruptedReader{})
	if err == nil {
		t.Fatal("interrupted write must fail")
	}
	if _, statErr := os.Stat(filepath.Join(target.Ref, safeName(digest))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("interrupted write exposed a destination: %v", statErr)
	}
	entries, readErr := os.ReadDir(target.Ref)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("interrupted write left staging files: %v", entries)
	}
}
