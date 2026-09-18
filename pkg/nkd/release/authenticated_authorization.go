package release

import (
	"bytes"
	"context"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/vuln"
)

// AuthorizationDocumentSchema is the sole schema registration for signed
// cross-process release clearances. The decoded Go record remains package-private.
func AuthorizationDocumentSchema() trust.DocumentSchema {
	return trust.JSONSchema[authorizationRecord](AuthorizationSchemaVersion)
}

// VerifyAuthenticatedAuthorization rehydrates a clearance capability only from the
// exact canonical bytes authenticated under the distinct release-clearance role.
// Parsing raw JSON, using a generic signature callback, or deserializing Authorization
// cannot recreate this authority.
func VerifyAuthenticatedAuthorization(ctx context.Context, document trust.DocumentReceipt) (*Authorization, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if !document.Authenticated() || document.Role() != trust.RoleClearance ||
		document.SchemaVersion() != AuthorizationSchemaVersion {
		return nil, e.New(e.CodeSignatureInvalid,
			"release clearance requires an authenticated %s document", trust.RoleClearance)
	}
	switch decoded := document.Decoded().(type) {
	case authorizationRecord:
	case *authorizationRecord:
		if decoded == nil {
			return nil, e.New(e.CodeSignatureInvalid,
				"authenticated release clearance decoded to nil")
		}
	default:
		return nil, e.New(e.CodeSignatureInvalid,
			"authenticated release clearance has an unexpected decoded type")
	}
	raw := document.ExactBytes()
	var record authorizationRecord
	if err := canonical.UnmarshalExact(raw, &record); err != nil {
		return nil, err
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	payload, err := canonical.Marshal(record)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw, payload) {
		return nil, e.New(e.CodeSignatureInvalid,
			"authenticated release clearance is not the exact canonical record")
	}
	signer := strings.TrimSpace(document.SignerIdentity())
	if signer == "" || signer != document.SignerIdentity() {
		return nil, e.New(e.CodeSignatureInvalid,
			"authenticated release clearance has no canonical signer identity")
	}
	if signer == record.ReleaseSigner {
		return nil, e.New(e.CodeGateUnmet,
			"release-clearance signer %q must be distinct from release-set signer", signer)
	}
	authorization := &Authorization{valid: true, record: record,
		clearanceSigner: signer, canonicalPayload: string(payload)}
	if err := authorization.validateStored(); err != nil {
		return nil, err
	}
	return authorization, nil
}

func (authorization *Authorization) validateStored() error {
	if authorization == nil || !authorization.valid || authorization.canonicalPayload == "" {
		return e.New(e.CodeGateUnmet, "release authorization capability is absent or invalid")
	}
	if err := authorization.record.validate(); err != nil {
		return err
	}
	payload, err := canonical.Marshal(authorization.record)
	if err != nil {
		return err
	}
	if string(payload) != authorization.canonicalPayload {
		return e.New(e.CodeGateUnmet,
			"release authorization record changed after qualification")
	}
	return nil
}

func (record authorizationRecord) validate() error {
	if record.SchemaVersion != AuthorizationSchemaVersion || !isDigest(record.ReleaseSetDigest) ||
		strings.TrimSpace(record.ReleaseVersion) == "" ||
		strings.TrimSpace(record.ReleaseVersion) != record.ReleaseVersion ||
		!isDigest(record.PublicationIdentity) || !isDigest(record.InventoryDigest) ||
		!isDigest(record.PromotionPolicyDigest) || !isDigest(record.CompatibilityPolicyDigest) ||
		!isDigest(record.ConformancePolicyDigest) || !isDigest(record.StatusCheckpointDigest) ||
		!isDigest(record.StatusPolicyBinding) || !isDigest(record.StatusTimeBinding) ||
		strings.TrimSpace(record.StatusTimeAuthority) == "" ||
		strings.TrimSpace(record.StatusTimeAuthority) != record.StatusTimeAuthority ||
		strings.TrimSpace(record.ReleaseSigner) == "" ||
		strings.TrimSpace(record.ReleaseSigner) != record.ReleaseSigner ||
		strings.TrimSpace(record.PromotionPolicySigner) == "" ||
		strings.TrimSpace(record.PromotionPolicySigner) != record.PromotionPolicySigner ||
		!canonicalCompatibilityTarget(record.Target) || record.AsOfEpoch <= 0 {
		return e.New(e.CodeGateUnmet,
			"release authorization record lost a required identity or signer binding")
	}
	if err := status.VerifyTrustedTimeBinding(record.StatusTimeAuthority,
		record.AsOfEpoch, record.StatusTimeBinding); err != nil {
		return err
	}
	if record.PromotionPolicyEffective <= 0 ||
		record.PromotionPolicyExpires <= record.PromotionPolicyEffective ||
		record.PromotionPolicyExpires < record.AsOfEpoch ||
		record.AsOfEpoch < record.PromotionPolicyEffective {
		return e.New(e.CodeGateUnmet,
			"release authorization record is outside its authenticated policy interval")
	}
	if err := record.Publication.Validate(); err != nil {
		return err
	}
	if record.Publication.PublicationIdentity != record.PublicationIdentity ||
		!equalStrings(record.Publication.ArtifactDigests, record.ArtifactDigests) {
		return e.New(e.CodeGateUnmet,
			"release authorization publication manifest does not bind its exact identity/artifact set")
	}
	if err := validateRecordDigestSet("authorization artifact", record.ArtifactDigests, false); err != nil {
		return err
	}
	if err := validateRecordDigestSet("authorization evidence", record.EvidenceDigests, true); err != nil {
		return err
	}
	if len(record.Members) == 0 {
		return e.New(e.CodeReleaseIncomplete, "release authorization record has no member proofs")
	}
	if len(record.ValidationVectors) == 0 {
		return e.New(e.CodeGateUnmet,
			"release authorization record has no authenticated validation-vector proofs")
	}
	vectors := make(map[string]validationVectorProof, len(record.ValidationVectors))
	publisherRows := make(map[string]attest.PublisherEvidenceArtifactBinding)
	previousVector := ""
	for _, vector := range record.ValidationVectors {
		if !isDigest(vector.IdentityDigest) || !isDigest(vector.BlobDigest) ||
			!isDigest(vector.BuildDigest) || !isDigest(vector.SBOMIndexDigest) ||
			!isDigest(vector.ProvenanceIndexDigest) || !isDigest(vector.PublisherEvidenceDigest) ||
			strings.TrimSpace(vector.Signer) == "" ||
			strings.TrimSpace(vector.Signer) != vector.Signer ||
			(previousVector != "" && previousVector >= vector.IdentityDigest) || len(vector.Components) == 0 {
			return e.New(e.CodeGateUnmet,
				"release authorization contains an invalid validation-vector proof")
		}
		if vector.ProfileDigest != "" && !isDigest(vector.ProfileDigest) {
			return e.New(e.CodeGateUnmet,
				"release authorization validation vector has an invalid profile scope")
		}
		if err := vector.PublisherEvidence.Validate(); err != nil ||
			vector.PublisherEvidence.BuildDigest != vector.BuildDigest ||
			vector.PublisherEvidence.SBOMIndexDigest != vector.SBOMIndexDigest ||
			vector.PublisherEvidence.ProvenanceIndexDigest != vector.ProvenanceIndexDigest ||
			vector.PublisherEvidence.EvidenceDigest != vector.PublisherEvidenceDigest ||
			len(vector.PublisherEvidence.Artifacts) != len(vector.Components) {
			return e.New(e.CodeGateUnmet,
				"release authorization validation vector lost its exact publisher-evidence binding")
		}
		previousComponent := ""
		for index, component := range vector.Components {
			row := vector.PublisherEvidence.Artifacts[index]
			if strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Name) != component.Name ||
				strings.TrimSpace(component.OutputType) == "" ||
				strings.TrimSpace(component.OutputType) != component.OutputType ||
				(previousComponent != "" && previousComponent >= component.Name) ||
				!isDigest(component.SHA256) || row.Component != component.Name ||
				row.ArtifactDigest != component.SHA256 {
				return e.New(e.CodeGateUnmet,
					"release authorization validation vector has an invalid component/evidence projection")
			}
			rowKey := vectorEvidenceKey(row.Component, vector.BuildDigest, row.ArtifactDigest)
			if previousRow, duplicate := publisherRows[rowKey]; duplicate && previousRow != row {
				return e.New(e.CodeGateUnmet,
					"release authorization validation vectors disagree on publisher evidence for %s/%s",
					row.Component, row.ArtifactDigest)
			}
			publisherRows[rowKey] = row
			previousComponent = component.Name
		}
		vectors[vector.IdentityDigest] = vector
		previousVector = vector.IdentityDigest
	}
	previousMember := ""
	memberArtifacts := map[string]bool{}
	memberBuilds := map[string]bool{}
	memberVectorDigests := map[string]bool{}
	memberSubjects := map[string]memberProof{}
	externalSubjects := map[string]int{}
	releaseMembers := make([]Member, 0, len(record.Members))
	usedVectors := map[string]bool{}
	for _, member := range record.Members {
		key := member.Component + "\x00" + member.Arch
		if strings.TrimSpace(member.Component) == "" || strings.TrimSpace(member.Arch) == "" ||
			strings.TrimSpace(member.Component) != member.Component ||
			strings.TrimSpace(member.Arch) != member.Arch ||
			(member.Version != "" && strings.TrimSpace(member.Version) != member.Version) ||
			(member.Arch != "amd64" && member.Arch != "arm64") ||
			(previousMember != "" && previousMember >= key) || !isDigest(member.BuildDigest) ||
			!isDigest(member.VectorDigest) || !isDigest(member.ResolvedInputDigest) ||
			!isDigest(member.ReceiptDigest) || strings.TrimSpace(member.VerifiedBy) == "" ||
			strings.TrimSpace(member.VerifiedBy) != member.VerifiedBy ||
			member.Reproduction != Reproduced {
			return e.New(e.CodeGateUnmet, "release authorization contains an invalid member proof")
		}
		if member.ProfileIndependent == (member.ProfileDigest != "") ||
			(!member.ProfileIndependent && !isDigest(member.ProfileDigest)) {
			return e.New(e.CodeGateUnmet,
				"release authorization member has an invalid builder-profile scope")
		}
		if err := validateRecordDigestSet("member artifact", member.ArtifactDigests, false); err != nil {
			return err
		}
		reproduction := ReproductionResult{
			SchemaVersion: ReproductionSchemaVersion, ReleaseSetDigest: record.ReleaseSetDigest,
			Component: member.Component, Arch: member.Arch,
			BuildDigest: member.BuildDigest, VectorDigest: member.VectorDigest,
			ProfileDigest: member.ProfileDigest, ProfileIndependent: member.ProfileIndependent,
			ResolvedInputDigest: member.ResolvedInputDigest,
			ArtifactDigests:     append([]string(nil), member.ArtifactDigests...), Verdict: member.Reproduction,
			EffectiveEpoch: member.EffectiveEpoch, ExpiresEpoch: member.ExpiresEpoch,
		}
		if err := reproduction.Validate(); err != nil {
			return err
		}
		if err := receiptCurrent(member.EffectiveEpoch, member.ExpiresEpoch,
			record.AsOfEpoch, "reproduction"); err != nil {
			return err
		}
		reproductionDigest, err := canonical.Digest(canonical.KindReproductionResult, reproduction)
		if err != nil {
			return err
		}
		if reproductionDigest != member.ReceiptDigest {
			return e.New(e.CodeGateUnmet,
				"release authorization member proof does not rehydrate its exact reproduction receipt")
		}
		vector, ok := vectors[member.VectorDigest]
		if !ok || vector.BuildDigest != member.BuildDigest || vector.ProfileDigest != member.ProfileDigest {
			return e.New(e.CodeGateUnmet,
				"release authorization member does not bind its exact authenticated validation vector")
		}
		vectorArtifacts := make([]string, 0, len(vector.Components))
		for _, component := range vector.Components {
			vectorArtifacts = append(vectorArtifacts, component.SHA256)
		}
		sort.Strings(vectorArtifacts)
		if !equalStrings(vectorArtifacts, member.ArtifactDigests) {
			return e.New(e.CodeGateUnmet,
				"release authorization member/vector artifact sets differ")
		}
		usedVectors[member.VectorDigest] = true
		memberBuilds[member.BuildDigest] = true
		memberVectorDigests[member.VectorDigest] = true
		releaseMembers = append(releaseMembers, Member{
			Component: member.Component, Arch: member.Arch, Version: member.Version,
			BuildDigest: member.BuildDigest, VectorDigest: member.VectorDigest,
			ResolvedInputDigest: member.ResolvedInputDigest, ProfileDigest: member.ProfileDigest,
			ArtifactDigests: append([]string(nil), member.ArtifactDigests...),
		})
		for _, digest := range member.ArtifactDigests {
			memberArtifacts[digest] = true
			subjectKey := clearanceSubjectKey(member.Component, member.BuildDigest, digest)
			if _, duplicate := memberSubjects[subjectKey]; duplicate {
				return e.New(e.CodeGateUnmet,
					"release authorization repeats exact evidence subject %s/%s",
					member.Component, digest)
			}
			memberSubjects[subjectKey] = member
			externalSubjects[externalClearanceSubjectKey(member.Component, digest)]++
		}
		previousMember = key
	}
	if len(usedVectors) != len(vectors) {
		return e.New(e.CodeGateUnmet,
			"release authorization contains validation vectors outside its member closure")
	}
	if !equalStrings(sortedKeys(memberArtifacts), record.ArtifactDigests) {
		return e.New(e.CodeReleaseIncomplete,
			"release authorization member proofs do not exhaust its artifact set")
	}
	releaseSet := &ReleaseSet{SchemaVersion: ReleaseSetSchemaVersion, Version: record.ReleaseVersion,
		InventoryRef: record.InventoryDigest, Members: releaseMembers}
	releaseSetDigest, err := releaseSet.Digest()
	if err != nil {
		return err
	}
	publicationIdentity, err := releaseSet.PublicationIdentity()
	if err != nil {
		return err
	}
	if releaseSetDigest != record.ReleaseSetDigest || publicationIdentity != record.PublicationIdentity {
		return e.New(e.CodeGateUnmet,
			"release authorization does not rehydrate its exact ReleaseSet/publication identities")
	}
	compatibility := CompatibilityResult{
		SchemaVersion:    CompatibilitySchemaVersion,
		ReleaseSetDigest: record.ReleaseSetDigest, InventoryDigest: record.InventoryDigest,
		CompatibilityPolicyDigest: record.Compatibility.CompatibilityPolicyDigest,
		Target:                    record.Compatibility.Target,
		BuildDigests:              append([]string(nil), record.Compatibility.BuildDigests...),
		VectorDigests:             append([]string(nil), record.Compatibility.VectorDigests...),
		ArtifactDigests:           append([]string(nil), record.Compatibility.ArtifactDigests...),
		Verdict:                   record.Compatibility.Verdict,
		EffectiveEpoch:            record.Compatibility.EffectiveEpoch,
		ExpiresEpoch:              record.Compatibility.ExpiresEpoch,
	}
	if err := compatibility.Validate(); err != nil ||
		record.Compatibility.CompatibilityPolicyDigest != record.CompatibilityPolicyDigest ||
		record.Compatibility.Target != record.Target ||
		record.Compatibility.Verdict != CompatibilityAccepted ||
		strings.TrimSpace(record.Compatibility.VerifiedSigner) == "" ||
		strings.TrimSpace(record.Compatibility.VerifiedSigner) != record.Compatibility.VerifiedSigner ||
		!equalStrings(record.Compatibility.BuildDigests, sortedKeys(memberBuilds)) ||
		!equalStrings(record.Compatibility.VectorDigests, sortedKeys(memberVectorDigests)) ||
		!equalStrings(record.Compatibility.ArtifactDigests, record.ArtifactDigests) {
		return e.New(e.CodeGateUnmet,
			"release authorization lost its exact passing compatibility proof")
	}
	if err := receiptCurrent(record.Compatibility.EffectiveEpoch,
		record.Compatibility.ExpiresEpoch, record.AsOfEpoch, "compatibility"); err != nil {
		return err
	}
	compatibilityDigest, err := canonical.Digest(canonical.KindCompatibilityResult, compatibility)
	if err != nil {
		return err
	}
	if compatibilityDigest != record.Compatibility.ReceiptDigest {
		return e.New(e.CodeGateUnmet,
			"release authorization compatibility proof does not rehydrate its exact receipt")
	}

	conformance := ConformanceResult{
		SchemaVersion:     ConformanceSchemaVersion,
		ReleaseSetDigest:  record.Conformance.ReleaseSetDigest,
		InventoryDigest:   record.Conformance.InventoryDigest,
		VectorDigests:     append([]string(nil), record.Conformance.VectorDigests...),
		Suite:             record.Conformance.Suite,
		SuitePolicyDigest: record.Conformance.SuitePolicyDigest,
		Verdict:           record.Conformance.Verdict,
		ResultsDigest:     record.Conformance.ResultsDigest,
		EffectiveEpoch:    record.Conformance.EffectiveEpoch,
		ExpiresEpoch:      record.Conformance.ExpiresEpoch,
	}
	if err := conformance.Validate(); err != nil ||
		record.Conformance.ReleaseSetDigest != record.ReleaseSetDigest ||
		record.Conformance.InventoryDigest != record.InventoryDigest ||
		record.Conformance.SuitePolicyDigest != record.ConformancePolicyDigest ||
		record.Conformance.Verdict != ConformancePassed ||
		strings.TrimSpace(record.Conformance.Suite) == "" ||
		strings.TrimSpace(record.Conformance.Suite) != record.Conformance.Suite ||
		strings.TrimSpace(record.Conformance.VerifiedSigner) == "" ||
		strings.TrimSpace(record.Conformance.VerifiedSigner) != record.Conformance.VerifiedSigner ||
		record.Conformance.VerifiedSigner == record.ReleaseSigner ||
		!equalStrings(record.Conformance.VectorDigests, sortedKeys(memberVectorDigests)) {
		return e.New(e.CodeGateUnmet,
			"release authorization lost its exact passing conformance proof")
	}
	if err := receiptCurrent(record.Conformance.EffectiveEpoch,
		record.Conformance.ExpiresEpoch, record.AsOfEpoch, "conformance"); err != nil {
		return err
	}
	conformanceDigest, err := canonical.Digest(canonical.KindConformanceResult, conformance)
	if err != nil {
		return err
	}
	if conformanceDigest != record.Conformance.ReceiptDigest {
		return e.New(e.CodeGateUnmet,
			"release authorization conformance proof does not rehydrate its exact receipt")
	}
	previousEvidence := ""
	evidenceDigests := map[string]bool{}
	localEvidence := map[string]bool{}
	publicationCandidates := map[string]PublicationReferrer{}
	for _, vector := range record.ValidationVectors {
		for _, component := range vector.Components {
			candidate := PublicationReferrer{SubjectDigest: component.SHA256,
				ArtifactType: ReferrerValidationVector, IdentityDigest: vector.IdentityDigest,
				BlobDigest: vector.BlobDigest}
			if err := addPublicationCandidate(publicationCandidates, candidate); err != nil {
				return err
			}
		}
	}
	for _, proof := range record.Evidence {
		key := proof.ReceiptType.String() + "\x00" + proof.Component + "\x00" +
			proof.BuildDigest + "\x00" + proof.ArtifactDigest
		if !proof.ReceiptType.valid() || strings.TrimSpace(proof.Component) == "" ||
			strings.TrimSpace(proof.Component) != proof.Component ||
			!isDigest(proof.ArtifactDigest) || !isDigest(proof.EvidenceDigest) ||
			!isDigest(proof.BlobDigest) || strings.TrimSpace(proof.EvidenceSchema) == "" ||
			strings.TrimSpace(proof.EvidenceSchema) != proof.EvidenceSchema ||
			(previousEvidence != "" && previousEvidence >= key) {
			return e.New(e.CodeGateUnmet, "release authorization contains an invalid evidence proof")
		}
		rowKey := vectorEvidenceKey(proof.Component, proof.BuildDigest, proof.ArtifactDigest)
		subjectKey := clearanceSubjectKey(proof.Component,
			proof.BuildDigest, proof.ArtifactDigest)
		switch proof.ReceiptType {
		case EvidenceSBOM, EvidenceProvenance:
			if proof.SBOMDigest != "" || proof.ResultDigest != "" || proof.ScannerDigest != "" ||
				proof.DatabaseDigest != "" {
				return e.New(e.CodeGateUnmet,
					"SBOM/provenance proof carries vulnerability-only fields")
			}
			if proof.Reference != "" {
				if proof.BuildDigest != "" ||
					!isCanonicalEvidenceRef(proof.Reference) || proof.BlobDigest != proof.EvidenceDigest ||
					!validExternalEvidenceSchema(proof.ReceiptType, proof.EvidenceSchema) ||
					externalSubjects[externalClearanceSubjectKey(proof.Component, proof.ArtifactDigest)] != 1 {
					return e.New(e.CodeGateUnmet,
						"external evidence proof is not bound to one exact release subject and verified payload")
				}
			} else {
				if !isDigest(proof.BuildDigest) {
					return e.New(e.CodeGateUnmet,
						"local evidence proof has no exact build binding")
				}
				if _, exists := memberSubjects[subjectKey]; !exists {
					return e.New(e.CodeGateUnmet,
						"local evidence proof is outside the exact release member closure")
				}
				row, exists := publisherRows[rowKey]
				if !exists || !localProofMatchesPublisherRow(proof, row) {
					return e.New(e.CodeGateUnmet,
						"local evidence proof does not match the signed vector publisher-evidence row")
				}
				localKey := rowKey + "\x00" + proof.ReceiptType.String()
				if localEvidence[localKey] {
					return e.New(e.CodeGateUnmet,
						"release authorization repeats one local publisher-evidence proof")
				}
				localEvidence[localKey] = true
				candidate := PublicationReferrer{SubjectDigest: proof.ArtifactDigest,
					ArtifactType:   evidenceArtifactType(proof.ReceiptType),
					IdentityDigest: proof.EvidenceDigest, BlobDigest: proof.BlobDigest}
				if err := addPublicationCandidate(publicationCandidates, candidate); err != nil {
					return err
				}
			}
		case EvidenceVulnerability:
			if proof.Reference != "" || !isDigest(proof.BuildDigest) ||
				proof.EvidenceSchema != vuln.QualifiedReportSchemaVersion ||
				!isDigest(proof.SBOMDigest) || !isDigest(proof.ResultDigest) ||
				!isDigest(proof.ScannerDigest) || !isDigest(proof.DatabaseDigest) {
				return e.New(e.CodeGateUnmet,
					"vulnerability proof lost its exact report, scan, or database binding")
			}
			if _, exists := memberSubjects[subjectKey]; !exists {
				return e.New(e.CodeGateUnmet,
					"vulnerability proof is outside the exact release member closure")
			}
			row, exists := publisherRows[rowKey]
			if !exists || proof.SBOMDigest != row.SBOMDigest {
				return e.New(e.CodeGateUnmet,
					"vulnerability proof does not scan the signed vector's exact publisher SBOM")
			}
			candidate := PublicationReferrer{SubjectDigest: proof.ArtifactDigest,
				ArtifactType: ReferrerVulnerability, IdentityDigest: proof.EvidenceDigest,
				BlobDigest: proof.BlobDigest}
			if err := addPublicationCandidate(publicationCandidates, candidate); err != nil {
				return err
			}
		}
		evidenceDigests[proof.EvidenceDigest] = true
		if proof.SBOMDigest != "" {
			if !isDigest(proof.SBOMDigest) {
				return e.New(e.CodeGateUnmet, "release authorization evidence proof has an invalid SBOM digest")
			}
			evidenceDigests[proof.SBOMDigest] = true
		}
		if proof.ResultDigest != "" {
			if !isDigest(proof.ResultDigest) {
				return e.New(e.CodeGateUnmet, "release authorization evidence proof has an invalid result digest")
			}
			evidenceDigests[proof.ResultDigest] = true
		}
		previousEvidence = key
	}
	for rowKey := range publisherRows {
		for _, evidenceType := range []EvidenceType{EvidenceSBOM, EvidenceProvenance} {
			if !localEvidence[rowKey+"\x00"+evidenceType.String()] {
				return e.New(e.CodeGateUnmet,
					"release authorization lost an exact local publisher-evidence proof")
			}
		}
	}
	if err := validatePublicationProofClosure(record.Publication, publicationCandidates); err != nil {
		return err
	}
	previousWaiver := ""
	for _, waiver := range record.Waivers {
		if !isDigest(waiver.ReceiptDigest) || !isDigest(waiver.ArtifactDigest) ||
			!isDigest(waiver.BuildDigest) || !isDigest(waiver.StatusCheckpointDigest) ||
			!isDigest(waiver.PromotionPolicyDigest) || waiver.StatusCheckpointDigest != record.StatusCheckpointDigest ||
			waiver.PromotionPolicyDigest != record.PromotionPolicyDigest {
			return e.New(e.CodeGateUnmet, "release authorization contains an invalid waiver proof")
		}
		key := waiverProofKey(waiver)
		if strings.TrimSpace(waiver.Component) == "" ||
			strings.TrimSpace(waiver.Component) != waiver.Component ||
			strings.TrimSpace(waiver.ApproverIdentity) == "" ||
			strings.TrimSpace(waiver.ApproverIdentity) != waiver.ApproverIdentity ||
			(previousWaiver != "" && previousWaiver >= key) {
			return e.New(e.CodeGateUnmet,
				"release authorization contains a non-canonical waiver proof")
		}
		member, exists := memberSubjects[clearanceSubjectKey(waiver.Component,
			waiver.BuildDigest, waiver.ArtifactDigest)]
		if !exists || (member.ProfileIndependent &&
			(waiver.ProfileScope != "profile-independent" || waiver.ProfileDigest != "")) ||
			(!member.ProfileIndependent &&
				(waiver.ProfileScope != "builder-profile" || waiver.ProfileDigest != member.ProfileDigest)) {
			return e.New(e.CodeGateUnmet,
				"release authorization waiver does not bind an exact member/profile subject")
		}
		missingTypes, err := normalizeEvidenceTypes(waiver.MissingTypes)
		if err != nil || !equalEvidenceTypes(missingTypes, waiver.MissingTypes) {
			return e.New(e.CodeGateUnmet,
				"release authorization waiver missing-types set is not canonical")
		}
		if _, ok := parseGapReason(waiver.Reason); !ok {
			return e.New(e.CodeGateUnmet,
				"release authorization waiver has an unsupported gap reason")
		}
		if err := receiptCurrent(waiver.IssuedEpoch, waiver.ExpiresEpoch,
			record.AsOfEpoch, "evidence waiver"); err != nil {
			return err
		}
		document := evidenceWaiverDocument{
			SchemaVersion: EvidenceWaiverSchemaVersion,
			Component:     waiver.Component, BuildDigest: waiver.BuildDigest,
			ArtifactDigest: waiver.ArtifactDigest, ProfileScope: waiver.ProfileScope,
			ProfileDigest: waiver.ProfileDigest, StatusCheckpointDigest: waiver.StatusCheckpointDigest,
			MissingTypes: append([]EvidenceType(nil), waiver.MissingTypes...), Reason: waiver.Reason,
			PromotionPolicyDigest: waiver.PromotionPolicyDigest,
			ApproverIdentity:      waiver.ApproverIdentity,
			IssuedEpoch:           waiver.IssuedEpoch, ExpiresEpoch: waiver.ExpiresEpoch,
		}
		waiverDigest, err := canonical.Digest(canonical.KindEvidenceWaiver, document)
		if err != nil {
			return err
		}
		if waiverDigest != waiver.ReceiptDigest {
			return e.New(e.CodeGateUnmet,
				"release authorization waiver proof does not rehydrate its exact receipt")
		}
		evidenceDigests[waiver.ReceiptDigest] = true
		previousWaiver = key
	}
	if !equalStrings(sortedKeys(evidenceDigests), record.EvidenceDigests) {
		return e.New(e.CodeGateUnmet,
			"release authorization evidence digest set does not exactly equal its proof/waiver closure")
	}
	return nil
}

func validateRecordDigestSet(label string, values []string, allowEmpty bool) error {
	if !allowEmpty && len(values) == 0 {
		return e.New(e.CodeReleaseIncomplete, "%s set is empty", label)
	}
	for index, value := range values {
		if !isDigest(value) || (index > 0 && values[index-1] >= value) {
			return e.New(e.CodeUnverifiedRef,
				"%s set must contain exact, sorted, duplicate-free digests", label)
		}
	}
	return nil
}

func clearanceSubjectKey(component, buildDigest, artifactDigest string) string {
	return component + "\x00" + buildDigest + "\x00" + artifactDigest
}

func externalClearanceSubjectKey(component, artifactDigest string) string {
	return component + "\x00" + artifactDigest
}

func localProofMatchesPublisherRow(proof evidenceProof,
	row attest.PublisherEvidenceArtifactBinding,
) bool {
	if proof.Component != row.Component || proof.ArtifactDigest != row.ArtifactDigest {
		return false
	}
	switch proof.ReceiptType {
	case EvidenceSBOM:
		return proof.EvidenceDigest == row.SBOMDigest &&
			proof.BlobDigest == row.SBOMBlobDigest &&
			proof.EvidenceSchema == row.SBOMEvidenceSchema
	case EvidenceProvenance:
		return proof.EvidenceDigest == row.ProvenanceDigest &&
			proof.BlobDigest == row.ProvenanceBlobDigest &&
			proof.EvidenceSchema == row.ProvenanceSchema
	default:
		return false
	}
}

func publicationSubjectTypeKey(subject, artifactType string) string {
	return subject + "\x00" + artifactType
}

func addPublicationCandidate(candidates map[string]PublicationReferrer,
	candidate PublicationReferrer,
) error {
	key := publicationSubjectTypeKey(candidate.SubjectDigest, candidate.ArtifactType)
	if previous, exists := candidates[key]; exists && previous != candidate {
		return e.New(e.CodeGateUnmet,
			"release clearance has conflicting publication proofs for subject/media-type %s/%s",
			candidate.SubjectDigest, candidate.ArtifactType)
	}
	candidates[key] = candidate
	return nil
}

func validatePublicationProofClosure(manifest PublicationManifest,
	candidates map[string]PublicationReferrer,
) error {
	actual := make(map[string]bool, len(manifest.Referrers))
	publicationSubjects := map[string]bool{}
	for _, referrer := range manifest.Referrers {
		key := publicationSubjectTypeKey(referrer.SubjectDigest, referrer.ArtifactType)
		candidate, exists := candidates[key]
		if !exists || candidate != referrer {
			return e.New(e.CodeGateUnmet,
				"publication manifest referrer is not backed by an exact authenticated vector/evidence proof")
		}
		actual[key] = true
		publicationSubjects[referrer.SubjectDigest] = true
	}
	for key, candidate := range candidates {
		if publicationSubjects[candidate.SubjectDigest] && !actual[key] {
			return e.New(e.CodeGateUnmet,
				"publication manifest omits an authenticated referrer proof for subject %s",
				candidate.SubjectDigest)
		}
	}
	return nil
}

func equalEvidenceTypes(left, right []EvidenceType) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func canonicalCompatibilityTarget(target CompatibilityTarget) bool {
	for _, value := range []string{target.Kubernetes, target.NKE, target.Platform} {
		if strings.TrimSpace(value) != value {
			return false
		}
	}
	return true
}
