package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
	"github.com/nhensley/nkdctl/pkg/nkd/sbom"
	"github.com/nhensley/nkdctl/pkg/nkd/vuln"
)

type waiverVerifier struct {
	approver string
	err      error
}

func (verifier waiverVerifier) VerifyEvidenceWaiver(context.Context, []byte, []byte) (string, error) {
	return verifier.approver, verifier.err
}

func evidenceNoneAuthorization(t *testing.T) (AuthorizationRequest, EvidenceWaiverReceipt) {
	t.Helper()
	inv := inventory()
	inv.Components[0].EvidenceGaps = []EvidenceGapDeclaration{{
		ArtifactDigest: digest('a'), MissingTypes: []EvidenceType{EvidenceProvenance, EvidenceSBOM},
		Reason: EvidenceNone,
	}}
	inv.Components[0].ArtifactKinds[0].Kind = ArtifactRaw
	set := releaseSet(t, inv)
	set.Members[0].ProfileDigest = digest('4')
	req := authorizationRequest(t, inv, set)
	req.PromotionPolicy = verifiedPromotionPolicy(t,
		req.PromotionPolicy.policy.CompatibilityPolicyDigest,
		req.PromotionPolicy.policy.ConformancePolicyDigest,
		[]PromotionRule{
			{ArtifactKind: ArtifactRaw, Evidence: []EvidenceRequirement{
				{Type: EvidenceSBOM, WaivableReasons: []EvidenceGapReason{EvidenceNone}},
				{Type: EvidenceProvenance, WaivableReasons: []EvidenceGapReason{EvidenceNone}},
				{Type: EvidenceVulnerability},
			}},
			vulnerabilityPromotionRule(ArtifactHostPackage),
		},
	)
	promotionPolicyDigest, err := req.PromotionPolicy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	statusBinding := req.Status
	profileBinding := BindProfileIndependent()
	if set.Members[0].ProfileDigest != "" {
		var err error
		profileBinding, err = BindBuilderProfile(set.Members[0].ProfileDigest)
		if err != nil {
			t.Fatal(err)
		}
	}
	waiver, err := VerifyEvidenceWaiver(context.Background(), EvidenceWaiverRequest{
		Component: "kubelet", Role: "worker", RecipeDigest: set.Members[0].RecipeDigest,
		ArtifactDigest: set.Members[0].ArtifactDigests[0], Profile: profileBinding, Status: statusBinding,
		MissingTypes: []EvidenceType{EvidenceSBOM, EvidenceProvenance}, Reason: EvidenceNone,
		PromotionPolicyDigest: promotionPolicyDigest, ApproverIdentity: "waiver-approver",
		IssuedEpoch: 10, ExpiresEpoch: 20,
	}, []byte("waiver-signature"), waiverVerifier{approver: "waiver-approver"})
	if err != nil {
		t.Fatal(err)
	}
	req.EvidenceWaivers = []EvidenceWaiverReceipt{waiver}
	return req, waiver
}

func TestAuthorizeRequiresQualifiedReportsAndIgnoresRawEvidenceDigest(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	req.VulnerabilityReports = nil
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("raw member evidence digests must not replace qualified vulnerability reports: %v", err)
	}
	req.VulnerabilityReports = []vuln.QualifiedReport{{}}
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("zero-value qualified report must fail closed: %v", err)
	}

	req = authorizationRequest(t, inv, set)
	authorization, err := Authorize(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := authorization.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), digest('e')) || strings.Contains(string(encoded), digest('2')) {
		t.Fatalf("authorization recorded unauthenticated member evidence digest: %s", encoded)
	}
	if !strings.Contains(string(encoded), vuln.QualifiedReportSchemaVersion) {
		t.Fatalf("authorization omitted qualified-report proof: %s", encoded)
	}

	member := req.Release.set.Members[0]
	otherSBOM := map[string]any{
		"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1,
		"components": []any{},
	}
	otherSBOMBytes, err := canonical.Marshal(otherSBOM)
	if err != nil {
		t.Fatal(err)
	}
	otherSBOMDigest, err := canonical.Digest(canonical.KindSBOM, otherSBOM)
	if err != nil {
		t.Fatal(err)
	}
	wrongReport, err := vuln.QualifyScan(context.Background(), vuln.QualifiedScanRequest{
		Scanner: vuln.ScannerGrype, ScannerDigest: digest('7'), DatabaseDigest: digest('8'),
		RecipeDigest: member.RecipeDigest, ArtifactDigest: member.ArtifactDigests[0],
		SBOMDigest: otherSBOMDigest, CanonicalSBOM: otherSBOMBytes,
	}, qualifiedScanner{observation: vuln.ScanObservation{
		ScannerDigest: digest('7'), DatabaseDigest: digest('8'), Result: []byte(`{"matches":[]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	req.VulnerabilityReports = []vuln.QualifiedReport{wrongReport}
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("qualified vulnerability report scanned a different SBOM than the signed vector: %v", err)
	}
}

func TestReleaseStatusBindingRejectsCallerSelectedDiagnosticEpoch(t *testing.T) {
	receipt := diagnosticStatusCheckpoint(t, digest('a'), "status-authority", 10)
	if _, err := BindVerifiedStatusCheckpoint(receipt); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("caller-selected diagnostic epoch crossed the release boundary: %v", err)
	}
}

func TestEvidenceNoneRequiresExactAuthenticatedWaiver(t *testing.T) {
	req, waiver := evidenceNoneAuthorization(t)
	authorization, err := Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("exact authenticated evidence:none waiver must authorize the declared gap: %v", err)
	}
	accepted, err := authorization.EvidenceDigests()
	if err != nil {
		t.Fatal(err)
	}
	if !contains(accepted, waiver.digest) {
		t.Fatalf("authorization did not record exact evidence-gap waiver: %v", accepted)
	}
	for _, receipt := range req.LocalSBOMEvidence {
		binding, err := receipt.Binding()
		if err != nil {
			t.Fatal(err)
		}
		if !contains(accepted, binding.EvidenceDigest) {
			t.Fatalf("waived external gap discarded qualified local publisher SBOM %s: %v",
				binding.EvidenceDigest, accepted)
		}
	}
	for _, receipt := range req.LocalProvenanceEvidence {
		binding, err := receipt.Binding()
		if err != nil {
			t.Fatal(err)
		}
		if !contains(accepted, binding.EvidenceDigest) {
			t.Fatalf("waived external gap discarded qualified local publisher provenance %s: %v",
				binding.EvidenceDigest, accepted)
		}
	}

	without := req
	without.EvidenceWaivers = nil
	if _, err := Authorize(context.Background(), without); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("evidence:none without waiver must fail closed: %v", err)
	}
	publicationIdentity, err := req.Release.PublicationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	wrongStatus := verifiedStatus(t, publicationIdentity, "other-status-authority", req.AsOfEpoch)
	wrong := req
	wrong.Status = wrongStatus
	if _, err := Authorize(context.Background(), wrong); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("waiver replay under another status checkpoint must fail: %v", err)
	}
	wrong = req
	wrong.PromotionPolicy = verifiedPromotionPolicy(t,
		req.PromotionPolicy.policy.CompatibilityPolicyDigest,
		req.PromotionPolicy.policy.ConformancePolicyDigest,
		[]PromotionRule{
			{ArtifactKind: ArtifactRaw, Evidence: []EvidenceRequirement{
				{Type: EvidenceSBOM, WaivableReasons: []EvidenceGapReason{EvidenceNone, EvidenceNotDeclared}},
				{Type: EvidenceProvenance, WaivableReasons: []EvidenceGapReason{EvidenceNone}},
				{Type: EvidenceVulnerability},
			}},
			vulnerabilityPromotionRule(ArtifactHostPackage),
		},
	)
	if _, err := Authorize(context.Background(), wrong); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("waiver replay under another promotion policy must fail: %v", err)
	}
	var member Member
	for _, candidate := range req.Release.set.Members {
		if candidate.Component == "kubelet" {
			member = candidate
			break
		}
	}
	expiredWaiver, err := VerifyEvidenceWaiver(context.Background(), EvidenceWaiverRequest{
		Component: member.Component, Role: member.Role, RecipeDigest: member.RecipeDigest,
		ArtifactDigest: member.ArtifactDigests[0], Profile: mustProfile(t, member.ProfileDigest), Status: req.Status,
		MissingTypes: []EvidenceType{EvidenceSBOM, EvidenceProvenance}, Reason: EvidenceNone,
		PromotionPolicyDigest: mustPromotionPolicyDigest(t, req.PromotionPolicy), ApproverIdentity: "waiver-approver",
		IssuedEpoch: 1, ExpiresEpoch: 5,
	}, []byte("sig"), waiverVerifier{approver: "waiver-approver"})
	if err != nil {
		t.Fatal(err)
	}
	wrong = req
	wrong.EvidenceWaivers = []EvidenceWaiverReceipt{expiredWaiver}
	if _, err := Authorize(context.Background(), wrong); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("expired waiver must fail: %v", err)
	}
}

func TestWaiverReplayAcrossRecipeProfileOrGapFails(t *testing.T) {
	req, _ := evidenceNoneAuthorization(t)
	var member Member
	for _, candidate := range req.Release.set.Members {
		if candidate.Component == "kubelet" {
			member = candidate
			break
		}
	}
	statusBinding := req.Status
	tests := []struct {
		name    string
		request EvidenceWaiverRequest
	}{
		{name: "recipe", request: EvidenceWaiverRequest{
			Component: member.Component, Role: member.Role, RecipeDigest: digest('9'),
			ArtifactDigest: member.ArtifactDigests[0], Profile: mustProfile(t, member.ProfileDigest), Status: statusBinding,
			MissingTypes: []EvidenceType{EvidenceSBOM, EvidenceProvenance}, Reason: EvidenceNone,
			PromotionPolicyDigest: mustPromotionPolicyDigest(t, req.PromotionPolicy), ApproverIdentity: "waiver-approver", IssuedEpoch: 10, ExpiresEpoch: 20,
		}},
		{name: "profile", request: EvidenceWaiverRequest{
			Component: member.Component, Role: member.Role, RecipeDigest: member.RecipeDigest,
			ArtifactDigest: member.ArtifactDigests[0], Profile: mustProfile(t, digest('9')), Status: statusBinding,
			MissingTypes: []EvidenceType{EvidenceSBOM, EvidenceProvenance}, Reason: EvidenceNone,
			PromotionPolicyDigest: mustPromotionPolicyDigest(t, req.PromotionPolicy), ApproverIdentity: "waiver-approver", IssuedEpoch: 10, ExpiresEpoch: 20,
		}},
		{name: "gap", request: EvidenceWaiverRequest{
			Component: member.Component, Role: member.Role, RecipeDigest: member.RecipeDigest,
			ArtifactDigest: member.ArtifactDigests[0], Profile: mustProfile(t, member.ProfileDigest), Status: statusBinding,
			MissingTypes: []EvidenceType{EvidenceSBOM}, Reason: EvidenceNone,
			PromotionPolicyDigest: mustPromotionPolicyDigest(t, req.PromotionPolicy), ApproverIdentity: "waiver-approver", IssuedEpoch: 10, ExpiresEpoch: 20,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			waiver, err := VerifyEvidenceWaiver(context.Background(), test.request, []byte("sig"),
				waiverVerifier{approver: "waiver-approver"})
			if err != nil {
				t.Fatal(err)
			}
			copy := req
			copy.EvidenceWaivers = []EvidenceWaiverReceipt{waiver}
			if _, err := Authorize(context.Background(), copy); code(err) != nkderr.CodeGateUnmet {
				t.Fatalf("inexact waiver must fail closed: %v", err)
			}
		})
	}
}

func TestWaiverAuthenticationMutationAndCancellationFailClosed(t *testing.T) {
	req, waiver := evidenceNoneAuthorization(t)
	mutated := waiver
	mutated.document.MissingTypes = append([]EvidenceType(nil), waiver.document.MissingTypes...)
	mutated.document.MissingTypes[0] = EvidenceVulnerability
	req.EvidenceWaivers = []EvidenceWaiverReceipt{mutated}
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("post-authentication waiver mutation must fail: %v", err)
	}
	req.EvidenceWaivers = []EvidenceWaiverReceipt{{}}
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("zero-value waiver receipt must fail: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := VerifyEvidenceWaiver(ctx, EvidenceWaiverRequest{}, []byte("sig"), waiverVerifier{approver: "a"})
	if code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled waiver authentication must return timeout: %v", err)
	}
	request := EvidenceWaiverRequest{
		Component: "c", RecipeDigest: digest('a'), ArtifactDigest: digest('b'),
		Profile: BindProfileIndependent(), Status: verifiedStatus(t, digest('f'), "waiver-test-authority", 1),
		MissingTypes: []EvidenceType{EvidenceSBOM}, Reason: EvidenceNone,
		PromotionPolicyDigest: digest('d'), ApproverIdentity: "expected", IssuedEpoch: 1, ExpiresEpoch: 2,
	}
	if _, err := VerifyEvidenceWaiver(context.Background(), request, []byte("sig"), waiverVerifier{approver: "wrong"}); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("wrong waiver authority identity must fail: %v", err)
	}
	request.Reason = EvidenceGapReason(99)
	if _, err := VerifyEvidenceWaiver(context.Background(), request, []byte("sig"), waiverVerifier{approver: "expected"}); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("arbitrary string-like waiver reason must fail: %v", err)
	}
}

func TestExternalDeclarationsRequireByteVerifiedReceipts(t *testing.T) {
	artifactDigest := rawTestDigest(releaseFixtureArtifactBytes("prebuilt", "worker"))
	sbomBytes := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","metadata":{"component":{"name":"prebuilt","hashes":[{"alg":"SHA-256","content":"` + strings.TrimPrefix(artifactDigest, "sha256:") + `"}]}}}`)
	provenanceBytes := []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"prebuilt","digest":{"sha256":"` + strings.TrimPrefix(artifactDigest, "sha256:") + `"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"buildType":"https://example.test/prebuilt/v1","externalParameters":{}},"runDetails":{"builder":{"id":"https://example.test/upstream-builder"}}}}`)
	sbomDigest := rawTestDigest(sbomBytes)
	provenanceDigest := rawTestDigest(provenanceBytes)
	// Transport location and content identity are deliberately separate. HTTPS is
	// mutable as a location; BuildVerified qualifies the bytes against the exact
	// digest before an opaque receipt can cross into release authorization.
	sbomRef := "https://evidence.example.test/prebuilt/sbom.json"
	provenanceRef := "https://evidence.example.test/prebuilt/provenance.json"
	resolved := &recipe.Resolved{
		RecipeID: "prebuilt", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "prebuilt", Component: catalog.Component{
			Tag: "v1", SourceDate: 1, OutputType: "host-package", Prebuilt: &catalog.Prebuilt{
				URL: "https://upstream.example/prebuilt", Digests: map[string]string{"amd64": artifactDigest},
				Evidence: &catalog.ExternalEvidence{SBOMRef: sbomRef, SBOMDigest: sbomDigest,
					ProvenanceRef: provenanceRef, ProvenanceDigest: provenanceDigest},
			},
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
	reader := sbom.EvidenceReaderFunc(func(_ context.Context, ref string) ([]byte, error) {
		switch ref {
		case sbomRef:
			return sbomBytes, nil
		case provenanceRef:
			return provenanceBytes, nil
		default:
			return nil, errors.New("unexpected ref")
		}
	})
	verifiedSet, err := sbom.BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, reader)
	if err != nil {
		t.Fatal(err)
	}
	badResolved := *resolved
	badResolved.Components = append([]catalog.NamedComponent(nil), resolved.Components...)
	badComponent := badResolved.Components[0]
	badPrebuilt := *badComponent.Prebuilt
	badEvidence := *badPrebuilt.Evidence
	badEvidence.SBOMDigest = digest('f')
	badPrebuilt.Evidence = &badEvidence
	badComponent.Prebuilt = &badPrebuilt
	badResolved.Components[0] = badComponent
	badGraph, err := recipe.Compile(&badResolved)
	if err != nil {
		t.Fatal(err)
	}
	badRecipeDigest, err := badGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbom.BuildVerified(context.Background(), &badResolved, badRecipeDigest,
		map[string]string{"prebuilt": artifactDigest}, reader); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("external HTTPS bytes that mismatch their exact declared digest must not produce a receipt: %v", err)
	}
	receipts, err := verifiedSet.ExternalReceipts()
	if err != nil {
		t.Fatal(err)
	}
	inv := &Inventory{SchemaVersion: InventorySchemaVersion, Version: "1", Components: []InventoryComponent{{
		Name: "prebuilt", Role: "worker", RequiredArtifacts: []string{artifactDigest},
		ArtifactKinds:            []ArtifactKindBinding{{ArtifactDigest: artifactDigest, Kind: ArtifactOCIImage}},
		ExternalSBOMRef:          sbomRef,
		ExternalSBOMDigest:       sbomDigest,
		ExternalProvenanceRef:    provenanceRef,
		ExternalProvenanceDigest: provenanceDigest,
	}}}
	invDigest, err := inv.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set := &ReleaseSet{SchemaVersion: ReleaseSetSchemaVersion, Version: "1", InventoryRef: invDigest, Members: []Member{{
		Component: "prebuilt", Role: "worker", Arch: "amd64", RecipeDigest: recipeDigest,
		VectorDigest: digest('1'), ResolvedInputDigest: digest('3'), ArtifactDigests: []string{artifactDigest},
	}}}
	req := authorizationRequest(t, inv, set)
	req.PromotionPolicy = verifiedPromotionPolicy(t,
		req.PromotionPolicy.policy.CompatibilityPolicyDigest,
		req.PromotionPolicy.policy.ConformancePolicyDigest,
		[]PromotionRule{{ArtifactKind: ArtifactOCIImage, Evidence: []EvidenceRequirement{
			{Type: EvidenceSBOM}, {Type: EvidenceProvenance}, {Type: EvidenceVulnerability},
		}}},
	)
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("raw immutable external declarations must not authorize without receipts: %v", err)
	}
	req.ExternalEvidence = receipts
	authorization, err := Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("byte-verified external receipts should satisfy the exact declarations: %v", err)
	}
	evidenceDigests, err := authorization.EvidenceDigests()
	if err != nil {
		t.Fatal(err)
	}
	if !contains(evidenceDigests, sbomDigest) || !contains(evidenceDigests, provenanceDigest) || contains(evidenceDigests, digest('2')) {
		t.Fatalf("authorization recorded wrong evidence identities: %v", evidenceDigests)
	}
	publication, err := authorization.PublicationManifest()
	if err != nil {
		t.Fatal(err)
	}
	typeCounts := map[string]int{}
	for _, referrer := range publication.Referrers {
		typeCounts[referrer.ArtifactType]++
	}
	if typeCounts[ReferrerSBOM] != 1 || typeCounts[ReferrerProvenance] != 1 {
		t.Fatalf("external dependency refs became duplicate publishable OCI evidence rows: %+v",
			publication.Referrers)
	}
	req.ExternalEvidence = append(req.ExternalEvidence, sbom.ExternalEvidenceReceipt{})
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("zero-value external receipt must fail closed: %v", err)
	}

	wrongInventory := *inv
	wrongInventory.Components = append([]InventoryComponent(nil), inv.Components...)
	wrongInventory.Components[0].ExternalSBOMRef = "https://evidence.example.test/prebuilt/other-sbom.json"
	wrongInventoryDigest, err := wrongInventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wrongSet := *set.sorted()
	wrongSet.InventoryRef = wrongInventoryDigest
	wrongRequest := authorizationRequest(t, &wrongInventory, &wrongSet)
	wrongRequest.PromotionPolicy = req.PromotionPolicy
	wrongRequest.ExternalEvidence = receipts
	if _, err := Authorize(context.Background(), wrongRequest); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("qualified evidence receipt replay under a different expected HTTPS reference must fail: %v", err)
	}

	wrongDigestInventory := *inv
	wrongDigestInventory.Components = append([]InventoryComponent(nil), inv.Components...)
	wrongDigestInventory.Components[0].ExternalSBOMDigest = digest('e')
	wrongDigestInventoryDigest, err := wrongDigestInventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wrongDigestSet := *set.sorted()
	wrongDigestSet.InventoryRef = wrongDigestInventoryDigest
	wrongDigestRequest := authorizationRequest(t, &wrongDigestInventory, &wrongDigestSet)
	wrongDigestRequest.PromotionPolicy = req.PromotionPolicy
	wrongDigestRequest.ExternalEvidence = receipts
	if _, err := Authorize(context.Background(), wrongDigestRequest); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("qualified evidence receipt replay under a different signed evidence digest must fail: %v", err)
	}
}

func TestConcurrentAuthorizationIsDeterministicAndRaceSafe(t *testing.T) {
	req, _ := evidenceNoneAuthorization(t)
	const workers = 32
	results := make(chan string, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			authorization, err := Authorize(context.Background(), req)
			if err != nil {
				errors <- err
				return
			}
			digest, err := authorization.Digest()
			if err != nil {
				errors <- err
				return
			}
			results <- digest
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent authorization failed: %v", err)
	}
	var first string
	for digest := range results {
		if first == "" {
			first = digest
		} else if digest != first {
			t.Fatalf("concurrent authorization was nondeterministic: %s != %s", digest, first)
		}
	}
}

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

func mustProfile(t *testing.T, digest string) ProfileBinding {
	t.Helper()
	if digest == "" {
		return BindProfileIndependent()
	}
	binding, err := BindBuilderProfile(digest)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func mustPromotionPolicyDigest(t *testing.T, receipt PromotionPolicyReceipt) string {
	t.Helper()
	digest, err := receipt.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func rawTestDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
