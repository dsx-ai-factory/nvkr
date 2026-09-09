package release

import (
	"context"
	"sort"

	"github.com/nhensley/nkdctl/pkg/nkd/attest"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/provenance"
	"github.com/nhensley/nkdctl/pkg/nkd/sbom"
)

// validationVectorProof is the clearance's transport-independent record of the
// authenticated ReleaseSet -> ValidationVector -> publisher-evidence chain.
type validationVectorProof struct {
	IdentityDigest          string                          `json:"identityDigest"`
	BlobDigest              string                          `json:"blobDigest"`
	Signer                  string                          `json:"signer"`
	RecipeDigest            string                          `json:"recipeDigest"`
	ProfileDigest           string                          `json:"profileDigest,omitempty"`
	SBOMIndexDigest         string                          `json:"sbomIndexDigest"`
	ProvenanceIndexDigest   string                          `json:"provenanceIndexDigest"`
	PublisherEvidenceDigest string                          `json:"publisherEvidenceDigest"`
	Components              []attest.ComponentDigest        `json:"components"`
	PublisherEvidence       attest.PublisherEvidenceBinding `json:"publisherEvidence"`
}

func verifyAuthenticatedVectorChain(ctx context.Context, request AuthorizationRequest) ([]validationVectorProof, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	type sbomEntry struct {
		receipt sbom.LocalEvidenceReceipt
		binding sbom.LocalEvidenceBinding
	}
	type provenanceEntry struct {
		receipt provenance.LocalEvidenceReceipt
		binding provenance.LocalEvidenceBinding
	}
	sboms := make(map[string]sbomEntry, len(request.LocalSBOMEvidence))
	for _, receipt := range request.LocalSBOMEvidence {
		binding, err := receipt.Binding()
		if err != nil {
			return nil, err
		}
		key := vectorEvidenceKey(binding.Component, binding.RecipeDigest, binding.ArtifactDigest)
		if _, duplicate := sboms[key]; duplicate {
			return nil, e.New(e.CodeGateUnmet,
				"release repeats local SBOM evidence for %s/%s", binding.Component, binding.ArtifactDigest)
		}
		sboms[key] = sbomEntry{receipt: receipt, binding: binding}
	}
	provenances := make(map[string]provenanceEntry, len(request.LocalProvenanceEvidence))
	for _, receipt := range request.LocalProvenanceEvidence {
		binding, err := receipt.Binding()
		if err != nil {
			return nil, err
		}
		key := vectorEvidenceKey(binding.Component, binding.RecipeDigest, binding.ArtifactDigest)
		if _, duplicate := provenances[key]; duplicate {
			return nil, e.New(e.CodeGateUnmet,
				"release repeats local provenance evidence for %s/%s", binding.Component, binding.ArtifactDigest)
		}
		provenances[key] = provenanceEntry{receipt: receipt, binding: binding}
	}

	vectors := make(map[string]attest.PublishedVectorBinding, len(request.ValidationVectors))
	for _, receipt := range request.ValidationVectors {
		binding, err := receipt.ReleaseBinding()
		if err != nil {
			return nil, err
		}
		if _, duplicate := vectors[binding.IdentityDigest]; duplicate {
			return nil, e.New(e.CodeGateUnmet,
				"release repeats authenticated validation vector %s", binding.IdentityDigest)
		}
		vectors[binding.IdentityDigest] = binding
	}

	usedVectors := map[string]bool{}
	usedSBOMs := map[string]bool{}
	usedProvenance := map[string]bool{}
	proofByVector := map[string]validationVectorProof{}
	for _, member := range request.Release.set.sorted().Members {
		vector, ok := vectors[member.VectorDigest]
		if !ok {
			return nil, e.New(e.CodeGateUnmet,
				"release member %s/%s lacks authenticated validation vector %s",
				member.Component, orNone(member.Role), member.VectorDigest)
		}
		if vector.RecipeDigest != member.RecipeDigest || vector.ProfileDigest != member.ProfileDigest {
			return nil, e.New(e.CodeGateUnmet,
				"validation vector %s does not bind member %s/%s exact recipe/profile",
				member.VectorDigest, member.Component, orNone(member.Role))
		}
		componentArtifacts := make([]string, 0, len(vector.Components))
		var sbomReceipts []sbom.LocalEvidenceReceipt
		var provenanceReceipts []provenance.LocalEvidenceReceipt
		for _, component := range vector.Components {
			componentArtifacts = append(componentArtifacts, component.SHA256)
			key := vectorEvidenceKey(component.Name, vector.RecipeDigest, component.SHA256)
			sbomReceipt, sbomOK := sboms[key]
			provenanceReceipt, provenanceOK := provenances[key]
			if !sbomOK || !provenanceOK {
				return nil, e.New(e.CodeProvenanceMissing,
					"validation vector %s lacks exact local SBOM/provenance receipts for %s/%s",
					member.VectorDigest, component.Name, component.SHA256)
			}
			sbomReceipts = append(sbomReceipts, sbomReceipt.receipt)
			provenanceReceipts = append(provenanceReceipts, provenanceReceipt.receipt)
			usedSBOMs[key], usedProvenance[key] = true, true
		}
		sort.Strings(componentArtifacts)
		if !equalStrings(componentArtifacts, sortedStrings(member.ArtifactDigests)) {
			return nil, e.New(e.CodeGateUnmet,
				"validation vector %s artifact set does not exactly equal member %s/%s",
				member.VectorDigest, member.Component, orNone(member.Role))
		}
		publisherEvidence, err := attest.QualifyPublisherEvidenceReceipts(ctx, sbomReceipts, provenanceReceipts)
		if err != nil {
			return nil, err
		}
		publisherBinding, err := publisherEvidence.Binding()
		if err != nil {
			return nil, err
		}
		if publisherBinding.RecipeDigest != vector.RecipeDigest ||
			publisherBinding.SBOMIndexDigest != vector.SBOMIndexDigest ||
			publisherBinding.ProvenanceIndexDigest != vector.ProvenanceIndexDigest ||
			publisherBinding.EvidenceDigest != vector.PublisherEvidenceDigest {
			return nil, e.New(e.CodeUnverifiedRef,
				"validation vector %s publisher evidence aggregates do not match exact local receipts",
				member.VectorDigest)
		}
		proof := validationVectorProof{
			IdentityDigest: vector.IdentityDigest, BlobDigest: vector.BlobDigest, Signer: vector.Signer,
			RecipeDigest: vector.RecipeDigest, ProfileDigest: vector.ProfileDigest,
			SBOMIndexDigest: vector.SBOMIndexDigest, ProvenanceIndexDigest: vector.ProvenanceIndexDigest,
			PublisherEvidenceDigest: vector.PublisherEvidenceDigest,
			Components:              append([]attest.ComponentDigest(nil), vector.Components...),
			PublisherEvidence:       publisherBinding,
		}
		if previous, duplicate := proofByVector[member.VectorDigest]; duplicate {
			if !equalValidationVectorProof(previous, proof) {
				return nil, e.New(e.CodeGateUnmet,
					"shared validation vector %s has inconsistent member bindings", member.VectorDigest)
			}
		} else {
			proofByVector[member.VectorDigest] = proof
		}
		usedVectors[member.VectorDigest] = true
	}
	if len(usedVectors) != len(vectors) || len(usedSBOMs) != len(sboms) ||
		len(usedProvenance) != len(provenances) {
		return nil, e.New(e.CodeGateUnmet,
			"authenticated vector/local evidence inputs contain entries outside the exact release closure")
	}
	proofs := make([]validationVectorProof, 0, len(proofByVector))
	for _, proof := range proofByVector {
		proofs = append(proofs, proof)
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].IdentityDigest < proofs[j].IdentityDigest })
	return proofs, nil
}

func vectorEvidenceKey(component, recipeDigest, artifactDigest string) string {
	return component + "\x00" + recipeDigest + "\x00" + artifactDigest
}

func equalValidationVectorProof(left, right validationVectorProof) bool {
	return left.IdentityDigest == right.IdentityDigest && left.BlobDigest == right.BlobDigest &&
		left.Signer == right.Signer && left.RecipeDigest == right.RecipeDigest &&
		left.ProfileDigest == right.ProfileDigest &&
		left.PublisherEvidenceDigest == right.PublisherEvidenceDigest
}
