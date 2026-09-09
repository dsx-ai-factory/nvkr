package release

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
)

const clearanceTestIssuer = "https://clearance-issuer.example.test"

func authenticateClearanceRecord(t *testing.T, record authorizationRecord, role trust.SignerRole,
	signer string,
) trust.DocumentReceipt {
	t.Helper()
	raw, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "clearance.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	authenticator, err := trust.NewDocumentAuthenticator(
		statusDocumentVerifier{signer: signer, issuer: clearanceTestIssuer},
		AuthorizationDocumentSchema(),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewRolePolicy(role,
		trust.Store{OperatorIdentities: []string{signer}}, clearanceTestIssuer)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: role, Policy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestAuthorizationRoundTripsOnlyThroughAuthenticatedClearanceRole(t *testing.T) {
	req := authorizationRequest(t, inventory(), releaseSet(t, inventory()))
	authorization, err := Authorize(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := authorization.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wantPublication, err := authorization.PublicationManifest()
	if err != nil {
		t.Fatal(err)
	}

	document := authenticateClearanceRecord(t, authorization.record,
		trust.RoleClearance, "clearance-signer")
	rehydrated, err := VerifyAuthenticatedAuthorization(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := rehydrated.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, err := rehydrated.Digest()
	if err != nil {
		t.Fatal(err)
	}
	gotPublication, err := rehydrated.PublicationManifest()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) || gotDigest != wantDigest ||
		gotPublication.DigestMustForTest(t) != wantPublication.DigestMustForTest(t) {
		t.Fatalf("authenticated clearance changed across process boundary")
	}
	if err := authorization.RequirePublicationCommit(); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("in-process authorization claimed an authenticated publication commit: %v", err)
	}
	if err := rehydrated.RequirePublicationCommit(); code(err) != nkderr.CodeEngineCapability {
		t.Fatalf("authenticated clearance bypassed missing publication-root protocol: %v", err)
	}

	wrongRole := authenticateClearanceRecord(t, authorization.record,
		trust.RoleReleaseSet, "clearance-signer")
	if _, err := VerifyAuthenticatedAuthorization(context.Background(), wrongRole); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("cross-role clearance substitution was accepted: %v", err)
	}
	if _, err := VerifyAuthenticatedAuthorization(context.Background(), trust.DocumentReceipt{}); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("raw/zero receipt recreated clearance authority: %v", err)
	}
	sameSigner := authenticateClearanceRecord(t, authorization.record,
		trust.RoleClearance, authorization.record.ReleaseSigner)
	if _, err := VerifyAuthenticatedAuthorization(context.Background(), sameSigner); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("release-set signer also issued clearance: %v", err)
	}
}

func TestAuthenticatedClearanceRejectsInternallyInconsistentSignedSets(t *testing.T) {
	authorization, err := Authorize(context.Background(),
		authorizationRequest(t, inventory(), releaseSet(t, inventory())))
	if err != nil {
		t.Fatal(err)
	}

	extraEvidence := authorization.record
	extraEvidence.EvidenceDigests = append(append([]string(nil), extraEvidence.EvidenceDigests...), digest('f'))
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, extraEvidence, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("signed clearance overstated its accepted evidence closure: %v", err)
	}

	wrongTime := authorization.record
	wrongTime.StatusTimeBinding = digest('f')
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, wrongTime, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("signed clearance substituted its trusted-time observation: %v", err)
	}

	wrongReproduction := authorization.record
	wrongReproduction.Members = append([]memberProof(nil), wrongReproduction.Members...)
	wrongReproduction.Members[0].EffectiveEpoch++
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, wrongReproduction, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("signed clearance substituted its reproduction receipt projection: %v", err)
	}

	wrongReleaseIdentity := authorization.record
	wrongReleaseIdentity.ReleaseVersion += "-substituted"
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, wrongReleaseIdentity, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("signed clearance substituted its ReleaseSet/publication identity core: %v", err)
	}

	wrongCompatibility := authorization.record
	wrongCompatibility.Compatibility.Target.Platform = "substituted-platform"
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, wrongCompatibility, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("signed clearance substituted its compatibility receipt projection: %v", err)
	}

	wrongConformance := authorization.record
	wrongConformance.Conformance.ResultsDigest = digest('f')
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, wrongConformance, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("signed clearance substituted its conformance receipt projection: %v", err)
	}

	wrongReferrer := authorization.record
	wrongReferrer.Publication.Referrers = append([]PublicationReferrer(nil),
		wrongReferrer.Publication.Referrers...)
	if len(wrongReferrer.Publication.Referrers) == 0 {
		wrongReferrer.Publication.Referrers = append(wrongReferrer.Publication.Referrers,
			PublicationReferrer{SubjectDigest: wrongReferrer.ArtifactDigests[0],
				ArtifactType: ReferrerStatusCheckpoint, IdentityDigest: digest('f'),
				BlobDigest: digest('e')})
	} else {
		wrongReferrer.Publication.Referrers[0].IdentityDigest = digest('f')
	}
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, wrongReferrer, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("signed clearance introduced a referrer outside its authenticated proof closure: %v", err)
	}

	wrongPublisherEvidence := authorization.record
	wrongPublisherEvidence.Evidence = append([]evidenceProof(nil), wrongPublisherEvidence.Evidence...)
	for index := range wrongPublisherEvidence.Evidence {
		proof := &wrongPublisherEvidence.Evidence[index]
		if proof.ReceiptType == EvidenceSBOM && proof.Reference == "" {
			proof.BlobDigest = digest('f')
			break
		}
	}
	if _, err := VerifyAuthenticatedAuthorization(context.Background(),
		authenticateClearanceRecord(t, wrongPublisherEvidence, trust.RoleClearance, "clearance-signer")); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("signed clearance substituted local evidence outside the vector aggregate: %v", err)
	}
}

func (manifest PublicationManifest) DigestMustForTest(t *testing.T) string {
	t.Helper()
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
