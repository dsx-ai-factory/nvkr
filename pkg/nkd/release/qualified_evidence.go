package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/vuln"
)

func rawEvidenceDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type evidenceProof struct {
	ReceiptType    EvidenceType `json:"receiptType"`
	Component      string       `json:"component,omitempty"`
	Role           string       `json:"role,omitempty"`
	RecipeDigest   string       `json:"recipeDigest,omitempty"`
	ArtifactDigest string       `json:"artifactDigest"`
	EvidenceDigest string       `json:"evidenceDigest"`
	BlobDigest     string       `json:"blobDigest"`
	EvidenceSchema string       `json:"evidenceSchema"`
	Reference      string       `json:"reference,omitempty"`
	SBOMDigest     string       `json:"sbomDigest,omitempty"`
	ResultDigest   string       `json:"resultDigest,omitempty"`
	ScannerDigest  string       `json:"scannerDigest,omitempty"`
	DatabaseDigest string       `json:"databaseDigest,omitempty"`
}

type waiverProof struct {
	ReceiptDigest          string         `json:"receiptDigest"`
	Component              string         `json:"component"`
	Role                   string         `json:"role,omitempty"`
	RecipeDigest           string         `json:"recipeDigest"`
	ArtifactDigest         string         `json:"artifactDigest"`
	ProfileScope           string         `json:"profileScope"`
	ProfileDigest          string         `json:"profileDigest,omitempty"`
	StatusCheckpointDigest string         `json:"statusCheckpointDigest"`
	MissingTypes           []EvidenceType `json:"missingTypes"`
	Reason                 string         `json:"reason"`
	PromotionPolicyDigest  string         `json:"promotionPolicyDigest"`
	ApproverIdentity       string         `json:"approverIdentity"`
	IssuedEpoch            int64          `json:"issuedEpoch"`
	ExpiresEpoch           int64          `json:"expiresEpoch"`
}

type qualifiedEvidenceResult struct {
	proofs                 []evidenceProof
	waivers                []waiverProof
	digests                []string
	promotionPolicyDigest  string
	statusCheckpointDigest string
	asOfEpoch              int64
}

type evidenceSubject struct {
	component      string
	role           string
	recipeDigest   string
	artifactDigest string
	artifactKind   ArtifactKind
	profileDigest  string
}

type waiverRequirement struct {
	subject evidenceSubject
	types   []EvidenceType
	reason  EvidenceGapReason
}

type externalRequirement struct {
	ref    string
	digest string
}

func evaluateQualifiedEvidence(ctx context.Context, request AuthorizationRequest,
	policy PromotionPolicy, promotionPolicyDigest string,
) (qualifiedEvidenceResult, error) {
	if err := contextErr(ctx); err != nil {
		return qualifiedEvidenceResult{}, err
	}
	if !isDigest(promotionPolicyDigest) {
		return qualifiedEvidenceResult{}, e.New(e.CodeUnverifiedRef,
			"authenticated promotion policy %q is not an exact digest", promotionPolicyDigest)
	}
	if request.AsOfEpoch < 0 {
		return qualifiedEvidenceResult{}, e.New(e.CodeGateUnmet, "authorization as-of epoch cannot be negative")
	}

	rules := newPromotionRuleSet(policy)
	subjects, err := releaseEvidenceSubjects(request.Release.set.sorted().Members, request.Inventory, rules)
	if err != nil {
		return qualifiedEvidenceResult{}, err
	}
	subjectsByReportKey := make(map[string][]evidenceSubject, len(subjects))
	for _, subject := range subjects {
		key := subjectKey(subject)
		subjectsByReportKey[key] = append(subjectsByReportKey[key], subject)
	}

	waiverRequirements, waivedTypes, err := inventoryWaiverRequirements(request.Inventory, subjects, rules)
	if err != nil {
		return qualifiedEvidenceResult{}, err
	}
	proofs := make([]evidenceProof, 0, len(request.LocalSBOMEvidence)+len(request.LocalProvenanceEvidence)+
		len(request.ExternalEvidence)+len(request.VulnerabilityReports))
	digests := map[string]bool{}
	satisfied := map[string]bool{}
	if err := collectLocalEvidence(ctx, request, subjects, rules, waivedTypes, satisfied, digests, &proofs); err != nil {
		return qualifiedEvidenceResult{}, err
	}
	externalRequirements, err := inventoryExternalRequirements(request.Inventory, subjects, rules, waivedTypes, satisfied)
	if err != nil {
		return qualifiedEvidenceResult{}, err
	}

	seenExternal := map[string]bool{}
	for _, receipt := range request.ExternalEvidence {
		if err := contextErr(ctx); err != nil {
			return qualifiedEvidenceResult{}, err
		}
		receiptType, ok := externalEvidenceType(receipt.ReceiptType())
		if !ok || strings.TrimSpace(receipt.Component()) == "" || !isDigest(receipt.ArtifactDigest()) ||
			!isDigest(receipt.EvidenceDigest()) || !isCanonicalEvidenceRef(receipt.Ref()) ||
			!validExternalEvidenceSchema(receiptType, receipt.EvidenceSchema()) {
			return qualifiedEvidenceResult{}, e.New(e.CodeUnverifiedRef,
				"authorization contains an invalid or zero-value external evidence receipt")
		}
		key := externalKey(receipt.Component(), receipt.ArtifactDigest(), receiptType)
		if seenExternal[key] {
			return qualifiedEvidenceResult{}, e.New(e.CodeGateUnmet,
				"authorization repeats external %s receipt for %s artifact %s",
				receiptType.String(), receipt.Component(), receipt.ArtifactDigest())
		}
		expected, required := externalRequirements[key]
		if !required {
			return qualifiedEvidenceResult{}, e.New(e.CodeGateUnmet,
				"external %s receipt for %s artifact %s is not required by the bound inventory",
				receiptType.String(), receipt.Component(), receipt.ArtifactDigest())
		}
		if receipt.Ref() != expected.ref || receipt.EvidenceDigest() != expected.digest {
			return qualifiedEvidenceResult{}, e.New(e.CodeUnverifiedRef,
				"external %s receipt reference/digest %q/%s does not match inventory binding %q/%s",
				receiptType.String(), receipt.Ref(), receipt.EvidenceDigest(), expected.ref, expected.digest)
		}
		seenExternal[key] = true
		digests[receipt.EvidenceDigest()] = true
		proofs = append(proofs, evidenceProof{
			ReceiptType: receiptType, Component: receipt.Component(), ArtifactDigest: receipt.ArtifactDigest(),
			EvidenceDigest: receipt.EvidenceDigest(), BlobDigest: receipt.EvidenceDigest(),
			EvidenceSchema: receipt.EvidenceSchema(), Reference: receipt.Ref(),
		})
	}
	for key, expected := range externalRequirements {
		if !seenExternal[key] {
			return qualifiedEvidenceResult{}, e.New(e.CodeGateUnmet,
				"required external evidence receipt is missing for %s (reference %s, digest %s)",
				printableEvidenceKey(key), expected.ref, expected.digest)
		}
	}

	seenReports := map[string]bool{}
	for _, qualified := range request.VulnerabilityReports {
		if err := contextErr(ctx); err != nil {
			return qualifiedEvidenceResult{}, err
		}
		report, err := qualified.Report()
		if err != nil {
			return qualifiedEvidenceResult{}, err
		}
		if report.SchemaVersion != vuln.QualifiedReportSchemaVersion ||
			!isDigest(report.RecipeDigest) || !isDigest(report.Artifact) || !isDigest(report.SBOMDigest) ||
			!isDigest(report.ScannerDigest) || !isDigest(report.DatabaseDigest) || !isDigest(report.ResultDigest) {
			return qualifiedEvidenceResult{}, e.New(e.CodeUnverifiedRef,
				"qualified vulnerability report lost an exact identity binding")
		}
		key := report.RecipeDigest + "\x00" + report.Artifact
		if seenReports[key] {
			return qualifiedEvidenceResult{}, e.New(e.CodeGateUnmet,
				"authorization repeats qualified vulnerability report for recipe %s artifact %s",
				report.RecipeDigest, report.Artifact)
		}
		matchingSubjects := subjectsByReportKey[key]
		var subject evidenceSubject
		for _, candidate := range matchingSubjects {
			if rules.requires(candidate.artifactKind, EvidenceVulnerability) &&
				!waivedTypes[subjectTypeKey(candidate, EvidenceVulnerability)] {
				subject = candidate
				break
			}
		}
		if subject.recipeDigest == "" {
			return qualifiedEvidenceResult{}, e.New(e.CodeGateUnmet,
				"qualified vulnerability report for recipe %s artifact %s is not an unwaived release requirement",
				report.RecipeDigest, report.Artifact)
		}
		reportDigest, err := qualified.Digest()
		if err != nil {
			return qualifiedEvidenceResult{}, err
		}
		payload, err := qualified.Marshal()
		if err != nil {
			return qualifiedEvidenceResult{}, err
		}
		seenReports[key] = true
		for _, digest := range []string{reportDigest, report.SBOMDigest, report.ResultDigest} {
			digests[digest] = true
		}
		proofs = append(proofs, evidenceProof{
			ReceiptType: EvidenceVulnerability, Component: subject.component, Role: subject.role,
			RecipeDigest: report.RecipeDigest, ArtifactDigest: report.Artifact,
			EvidenceDigest: reportDigest, BlobDigest: rawEvidenceDigest(payload), EvidenceSchema: report.SchemaVersion,
			SBOMDigest: report.SBOMDigest, ResultDigest: report.ResultDigest,
			ScannerDigest: report.ScannerDigest, DatabaseDigest: report.DatabaseDigest,
		})
	}
	for _, subject := range subjects {
		key := subject.recipeDigest + "\x00" + subject.artifactDigest
		if rules.requires(subject.artifactKind, EvidenceVulnerability) &&
			!waivedTypes[subjectTypeKey(subject, EvidenceVulnerability)] && !seenReports[key] {
			return qualifiedEvidenceResult{}, e.New(e.CodeGateUnmet,
				"member %s/%s recipe %s artifact %s lacks a qualified vulnerability report",
				subject.component, orNone(subject.role), subject.recipeDigest, subject.artifactDigest)
		}
	}

	waiverProofs, err := verifyWaiverRequirements(ctx, request, waiverRequirements, subjects, digests,
		promotionPolicyDigest)
	if err != nil {
		return qualifiedEvidenceResult{}, err
	}
	sortEvidenceProofs(proofs)
	sort.Slice(waiverProofs, func(i, j int) bool {
		return waiverProofKey(waiverProofs[i]) < waiverProofKey(waiverProofs[j])
	})
	return qualifiedEvidenceResult{
		proofs: proofs, waivers: waiverProofs, digests: sortedKeys(digests),
		promotionPolicyDigest:  promotionPolicyDigest,
		statusCheckpointDigest: request.Status.checkpointDigest, asOfEpoch: request.AsOfEpoch,
	}, nil
}

func validExternalEvidenceSchema(evidenceType EvidenceType, schema string) bool {
	switch evidenceType {
	case EvidenceSBOM:
		return schema == "cyclonedx-json/1.4" || schema == "cyclonedx-json/1.5" || schema == "cyclonedx-json/1.6"
	case EvidenceProvenance:
		return schema == "in-toto-slsa-provenance/v1"
	default:
		return false
	}
}

func collectLocalEvidence(ctx context.Context, request AuthorizationRequest, subjects []evidenceSubject,
	rules promotionRuleSet, waivedTypes, satisfied, digests map[string]bool, proofs *[]evidenceProof,
) error {
	type localBinding struct {
		component, recipeDigest, artifactDigest, evidenceDigest, blobDigest, schema string
		evidenceType                                                                EvidenceType
	}
	bindings := make([]localBinding, 0, len(request.LocalSBOMEvidence)+len(request.LocalProvenanceEvidence))
	for _, receipt := range request.LocalSBOMEvidence {
		if err := contextErr(ctx); err != nil {
			return err
		}
		binding, err := receipt.Binding()
		if err != nil {
			return err
		}
		bindings = append(bindings, localBinding{component: binding.Component,
			recipeDigest: binding.RecipeDigest, artifactDigest: binding.ArtifactDigest,
			evidenceDigest: binding.EvidenceDigest, blobDigest: binding.BlobDigest,
			schema: binding.EvidenceSchema, evidenceType: EvidenceSBOM})
	}
	for _, receipt := range request.LocalProvenanceEvidence {
		if err := contextErr(ctx); err != nil {
			return err
		}
		binding, err := receipt.Binding()
		if err != nil {
			return err
		}
		bindings = append(bindings, localBinding{component: binding.Component,
			recipeDigest: binding.RecipeDigest, artifactDigest: binding.ArtifactDigest,
			evidenceDigest: binding.EvidenceDigest, blobDigest: binding.BlobDigest,
			schema: binding.EvidenceSchema, evidenceType: EvidenceProvenance})
	}
	seenLocal := map[string]bool{}
	for _, binding := range bindings {
		var matches []evidenceSubject
		for _, subject := range subjects {
			if subject.component == binding.component && subject.recipeDigest == binding.recipeDigest &&
				subject.artifactDigest == binding.artifactDigest {
				matches = append(matches, subject)
			}
		}
		if len(matches) != 1 {
			return e.New(e.CodeGateUnmet,
				"local %s evidence for %s recipe %s artifact %s matches %d exact release subjects",
				binding.evidenceType.String(), binding.component, binding.recipeDigest,
				binding.artifactDigest, len(matches))
		}
		subject := matches[0]
		key := subjectTypeKey(subject, binding.evidenceType)
		if seenLocal[key] {
			return e.New(e.CodeGateUnmet,
				"authorization repeats qualified %s evidence for %s/%s artifact %s",
				binding.evidenceType.String(), subject.component, orNone(subject.role), subject.artifactDigest)
		}
		seenLocal[key] = true
		if rules.requires(subject.artifactKind, binding.evidenceType) &&
			!waivedTypes[subjectTypeKey(subject, binding.evidenceType)] {
			satisfied[key] = true
		}
		digests[binding.evidenceDigest] = true
		*proofs = append(*proofs, evidenceProof{ReceiptType: binding.evidenceType,
			Component: subject.component, Role: subject.role, RecipeDigest: subject.recipeDigest,
			ArtifactDigest: subject.artifactDigest, EvidenceDigest: binding.evidenceDigest,
			BlobDigest: binding.blobDigest, EvidenceSchema: binding.schema})
	}
	return nil
}

func releaseEvidenceSubjects(members []Member, inventory *Inventory, rules promotionRuleSet) ([]evidenceSubject, error) {
	seen := map[string]bool{}
	subjects := make([]evidenceSubject, 0)
	for _, member := range members {
		for _, artifact := range member.ArtifactDigests {
			kind, err := inventoryArtifactKind(inventory, member.Component, member.Role, artifact)
			if err != nil {
				return nil, err
			}
			if _, covered := rules[kind]; !covered {
				return nil, e.New(e.CodeGateUnmet,
					"promotion policy has no rule for artifact kind %s (%s/%s artifact %s)",
					kind.String(), member.Component, orNone(member.Role), artifact)
			}
			subject := evidenceSubject{component: member.Component, role: member.Role,
				recipeDigest: member.RecipeDigest, artifactDigest: artifact, artifactKind: kind,
				profileDigest: member.ProfileDigest}
			key := waiverSubjectKey(subject)
			if !seen[key] {
				seen[key] = true
				subjects = append(subjects, subject)
			}
		}
	}
	sort.Slice(subjects, func(i, j int) bool { return waiverSubjectKey(subjects[i]) < waiverSubjectKey(subjects[j]) })
	return subjects, nil
}

func inventoryArtifactKind(inventory *Inventory, componentName, role, artifact string) (ArtifactKind, error) {
	var matched bool
	var kind ArtifactKind
	for _, component := range inventory.Components {
		if component.Name != componentName || (component.Role != "" && component.Role != role) {
			continue
		}
		for _, binding := range component.ArtifactKinds {
			if binding.ArtifactDigest != artifact {
				continue
			}
			if matched {
				return 0, e.New(e.CodeInvalidCatalog,
					"inventory classifies %s/%s artifact %s through more than one component rule",
					componentName, orNone(role), artifact)
			}
			matched, kind = true, binding.Kind
		}
	}
	if !matched {
		return 0, e.New(e.CodeReleaseIncomplete,
			"release artifact %s for %s/%s has no exact Inventory artifact-kind binding",
			artifact, componentName, orNone(role))
	}
	return kind, nil
}

func inventoryExternalRequirements(inventory *Inventory, subjects []evidenceSubject,
	rules promotionRuleSet, waivedTypes, satisfied map[string]bool,
) (map[string]externalRequirement, error) {
	requirements := map[string]externalRequirement{}
	for _, component := range inventory.Components {
		for _, subject := range subjects {
			if subject.component != component.Name || (component.Role != "" && subject.role != component.Role) {
				continue
			}
			for _, item := range []struct {
				evidenceType EvidenceType
				ref          string
				digest       string
			}{
				{EvidenceSBOM, component.ExternalSBOMRef, component.ExternalSBOMDigest},
				{EvidenceProvenance, component.ExternalProvenanceRef, component.ExternalProvenanceDigest},
			} {
				if !rules.requires(subject.artifactKind, item.evidenceType) ||
					waivedTypes[subjectTypeKey(subject, item.evidenceType)] {
					continue
				}
				if item.ref == "" || !isDigest(item.digest) {
					if satisfied[subjectTypeKey(subject, item.evidenceType)] {
						continue
					}
					return nil, e.New(e.CodeGateUnmet,
						"promotion policy requires %s evidence for %s/%s artifact %s, but no qualified receipt or authorized gap exists",
						item.evidenceType.String(), subject.component, orNone(subject.role), subject.artifactDigest)
				}
				key := externalKey(component.Name, subject.artifactDigest, item.evidenceType)
				if _, duplicate := requirements[key]; duplicate {
					return nil, e.New(e.CodeGateUnmet,
						"external %s evidence for %s artifact %s ambiguously covers more than one release subject",
						item.evidenceType.String(), component.Name, subject.artifactDigest)
				}
				requirements[key] =
					externalRequirement{ref: item.ref, digest: item.digest}
			}
		}
	}
	return requirements, nil
}

func inventoryWaiverRequirements(inventory *Inventory, subjects []evidenceSubject,
	rules promotionRuleSet,
) ([]waiverRequirement, map[string]bool, error) {
	requirements := make([]waiverRequirement, 0)
	waivedTypes := map[string]bool{}
	for _, component := range inventory.Components {
		for _, gap := range component.EvidenceGaps {
			missing, err := normalizeEvidenceTypes(gap.MissingTypes)
			if err != nil {
				return nil, nil, err
			}
			matched := false
			for _, subject := range subjects {
				if subject.component != component.Name || subject.artifactDigest != gap.ArtifactDigest ||
					(component.Role != "" && subject.role != component.Role) {
					continue
				}
				matched = true
				requiredMissing := make([]EvidenceType, 0, len(missing))
				for _, evidenceType := range missing {
					if !rules.requires(subject.artifactKind, evidenceType) {
						continue
					}
					if !rules.allowsWaiver(subject.artifactKind, evidenceType, gap.Reason) {
						return nil, nil, e.New(e.CodeGateUnmet,
							"promotion policy does not permit %s waiver reason %s for artifact kind %s",
							evidenceType.String(), gap.Reason.String(), subject.artifactKind.String())
					}
					requiredMissing = append(requiredMissing, evidenceType)
					waivedTypes[subjectTypeKey(subject, evidenceType)] = true
				}
				if len(requiredMissing) > 0 {
					requirements = append(requirements, waiverRequirement{
						subject: subject, types: requiredMissing, reason: gap.Reason,
					})
				}
			}
			if !matched {
				return nil, nil, e.New(e.CodeReleaseIncomplete,
					"inventory evidence gap for %s artifact %s has no exact release member",
					label(component.Name, component.Role), gap.ArtifactDigest)
			}
		}
	}
	return requirements, waivedTypes, nil
}

func verifyWaiverRequirements(ctx context.Context, request AuthorizationRequest,
	requirements []waiverRequirement, subjects []evidenceSubject, digests map[string]bool,
	promotionPolicyDigest string,
) ([]waiverProof, error) {
	if len(request.EvidenceWaivers) == 0 && len(requirements) == 0 {
		return nil, nil
	}
	if err := request.Status.validate(); err != nil {
		return nil, err
	}
	if !isDigest(request.Status.checkpointDigest) ||
		!isDigest(promotionPolicyDigest) || request.AsOfEpoch <= 0 {
		return nil, e.New(e.CodeGateUnmet,
			"evidence waivers require exact status-checkpoint and promotion-policy bindings plus a positive as-of epoch")
	}
	requirementByKey := make(map[string]waiverRequirement, len(requirements))
	for _, requirement := range requirements {
		requirementByKey[waiverRequirementKey(requirement)] = requirement
	}
	subjectByWaiverIdentity := make(map[string]evidenceSubject, len(subjects))
	for _, subject := range subjects {
		subjectByWaiverIdentity[waiverSubjectKey(subject)] = subject
	}
	seen := map[string]bool{}
	proofs := make([]waiverProof, 0, len(request.EvidenceWaivers))
	for _, receipt := range request.EvidenceWaivers {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		if !receipt.valid || receipt.document.SchemaVersion != EvidenceWaiverSchemaVersion ||
			receipt.verifiedApprover == "" || receipt.verifiedApprover != receipt.document.ApproverIdentity {
			return nil, e.New(e.CodeSignatureInvalid,
				"authorization contains an unauthenticated evidence waiver receipt")
		}
		payload, err := canonical.Marshal(receipt.document)
		if err != nil {
			return nil, err
		}
		if string(payload) != receipt.canonicalPayload {
			return nil, e.New(e.CodeSignatureInvalid,
				"evidence waiver payload changed after authentication")
		}
		digest, err := canonical.Digest(canonical.KindEvidenceWaiver, receipt.document)
		if err != nil {
			return nil, err
		}
		if digest != receipt.digest {
			return nil, e.New(e.CodeSignatureInvalid,
				"evidence waiver digest changed after authentication")
		}
		if receipt.document.StatusCheckpointDigest != request.Status.checkpointDigest ||
			receipt.document.PromotionPolicyDigest != promotionPolicyDigest {
			return nil, e.New(e.CodeGateUnmet,
				"evidence waiver is bound to a different status checkpoint or promotion policy")
		}
		if request.AsOfEpoch < receipt.document.IssuedEpoch || request.AsOfEpoch > receipt.document.ExpiresEpoch {
			return nil, e.New(e.CodeGateUnmet,
				"evidence waiver for artifact %s is not valid at as-of epoch %d",
				receipt.document.ArtifactDigest, request.AsOfEpoch)
		}
		subject, exists := subjectByWaiverIdentity[documentSubjectKey(receipt.document)]
		if !exists || !documentMatchesProfile(receipt.document, subject) {
			return nil, e.New(e.CodeGateUnmet,
				"evidence waiver does not bind an exact release recipe, artifact, role, and profile")
		}
		reason, ok := parseGapReason(receipt.document.Reason)
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "evidence waiver contains unknown gap reason %q", receipt.document.Reason)
		}
		requirement := waiverRequirement{subject: subject,
			types: append([]EvidenceType(nil), receipt.document.MissingTypes...), reason: reason}
		key := waiverRequirementKey(requirement)
		if _, required := requirementByKey[key]; !required {
			return nil, e.New(e.CodeGateUnmet,
				"evidence waiver does not match an exact gap declared by the bound inventory")
		}
		if seen[key] {
			return nil, e.New(e.CodeGateUnmet, "authorization repeats an evidence waiver for the same exact gap")
		}
		seen[key] = true
		digests[digest] = true
		proofs = append(proofs, waiverProof{
			ReceiptDigest: digest, Component: receipt.document.Component, Role: receipt.document.Role,
			RecipeDigest: receipt.document.RecipeDigest, ArtifactDigest: receipt.document.ArtifactDigest,
			ProfileScope: receipt.document.ProfileScope, ProfileDigest: receipt.document.ProfileDigest,
			StatusCheckpointDigest: receipt.document.StatusCheckpointDigest,
			MissingTypes:           append([]EvidenceType(nil), receipt.document.MissingTypes...),
			Reason:                 receipt.document.Reason, PromotionPolicyDigest: receipt.document.PromotionPolicyDigest,
			ApproverIdentity: receipt.document.ApproverIdentity,
			IssuedEpoch:      receipt.document.IssuedEpoch, ExpiresEpoch: receipt.document.ExpiresEpoch,
		})
	}
	for key, requirement := range requirementByKey {
		if !seen[key] {
			return nil, e.New(e.CodeGateUnmet,
				"artifact %s recipe %s lacks an authenticated waiver for exact %s gap (%s)",
				requirement.subject.artifactDigest, requirement.subject.recipeDigest,
				evidenceTypesKey(requirement.types), requirement.reason.String())
		}
	}
	return proofs, nil
}

func sortEvidenceProofs(proofs []evidenceProof) {
	sort.Slice(proofs, func(i, j int) bool {
		left := proofs[i].ReceiptType.String() + "\x00" + proofs[i].Component + "\x00" + proofs[i].RecipeDigest + "\x00" + proofs[i].ArtifactDigest
		right := proofs[j].ReceiptType.String() + "\x00" + proofs[j].Component + "\x00" + proofs[j].RecipeDigest + "\x00" + proofs[j].ArtifactDigest
		return left < right
	})
}

func externalEvidenceType(value string) (EvidenceType, bool) {
	switch value {
	case "sbom":
		return EvidenceSBOM, true
	case "provenance":
		return EvidenceProvenance, true
	default:
		return 0, false
	}
}

func subjectKey(subject evidenceSubject) string {
	return subject.recipeDigest + "\x00" + subject.artifactDigest
}

func waiverSubjectKey(subject evidenceSubject) string {
	return subject.component + "\x00" + subject.role + "\x00" + subject.recipeDigest + "\x00" + subject.artifactDigest
}

func documentSubjectKey(document evidenceWaiverDocument) string {
	return document.Component + "\x00" + document.Role + "\x00" + document.RecipeDigest + "\x00" + document.ArtifactDigest
}

func subjectTypeKey(subject evidenceSubject, evidenceType EvidenceType) string {
	return waiverSubjectKey(subject) + "\x00" + evidenceType.String()
}

func externalKey(component, artifact string, evidenceType EvidenceType) string {
	return component + "\x00" + artifact + "\x00" + evidenceType.String()
}

func waiverRequirementKey(requirement waiverRequirement) string {
	return waiverSubjectKey(requirement.subject) + "\x00" + evidenceTypesKey(requirement.types) + "\x00" + requirement.reason.String()
}

func waiverProofKey(proof waiverProof) string {
	return proof.Component + "\x00" + proof.Role + "\x00" + proof.RecipeDigest + "\x00" +
		proof.ArtifactDigest + "\x00" + evidenceTypesKey(proof.MissingTypes)
}

func documentMatchesProfile(document evidenceWaiverDocument, subject evidenceSubject) bool {
	if subject.profileDigest == "" {
		return document.ProfileScope == "profile-independent" && document.ProfileDigest == ""
	}
	return document.ProfileScope == "builder-profile" && document.ProfileDigest == subject.profileDigest
}

func parseGapReason(value string) (EvidenceGapReason, bool) {
	switch value {
	case EvidenceNotDeclared.String():
		return EvidenceNotDeclared, true
	case EvidenceNone.String():
		return EvidenceNone, true
	default:
		return 0, false
	}
}

func printableEvidenceKey(key string) string {
	return strings.ReplaceAll(key, "\x00", "/")
}
