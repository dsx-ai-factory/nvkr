package release

import (
	"sort"

	"github.com/nhensley/nkdctl/pkg/nkd/attest"
	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

const (
	PublicationManifestSchemaVersion = "nkd.dev/publication-manifest/v1"
	ReferrerValidationVector         = "application/vnd.nkd.validation-vector.v1+json"
	ReferrerStatusCheckpoint         = "application/vnd.nkd.status-checkpoint.v1+json"
	ReferrerSBOM                     = "application/vnd.cyclonedx+json"
	ReferrerProvenance               = "application/vnd.in-toto+json"
	ReferrerVulnerability            = "application/vnd.nkd.vulnerability-report.v2+json"
)

// PublicationReferrer binds one exact OCI subject to both an NKD semantic identity
// and the ordinary sha256 of the bytes the registry descriptor must carry.
type PublicationReferrer struct {
	SubjectDigest  string `json:"subjectDigest"`
	ArtifactType   string `json:"artifactType"`
	IdentityDigest string `json:"identityDigest"`
	BlobDigest     string `json:"blobDigest"`
}

// PublicationManifest is the complete, authorization-bound publication closure.
// An empty Referrers slice is meaningful; transports may not add convenience blobs.
type PublicationManifest struct {
	SchemaVersion       string                `json:"schemaVersion"`
	PublicationIdentity string                `json:"publicationIdentity"`
	ArtifactDigests     []string              `json:"artifactDigests"`
	Referrers           []PublicationReferrer `json:"referrers"`
}

func (manifest PublicationManifest) Validate() error {
	if manifest.SchemaVersion != PublicationManifestSchemaVersion {
		return e.New(e.CodeGateUnmet, "publication manifest schemaVersion %q is unsupported", manifest.SchemaVersion)
	}
	if !isDigest(manifest.PublicationIdentity) {
		return e.New(e.CodeGateUnmet, "publication manifest has no exact publication identity")
	}
	if manifest.ArtifactDigests == nil || manifest.Referrers == nil {
		return e.New(e.CodeGateUnmet,
			"publication manifest must preserve explicit artifact and referrer sets (artifactsNil=%t referrersNil=%t)",
			manifest.ArtifactDigests == nil, manifest.Referrers == nil)
	}
	artifacts := map[string]bool{}
	for index, digest := range manifest.ArtifactDigests {
		if !isDigest(digest) || (index > 0 && manifest.ArtifactDigests[index-1] >= digest) {
			return e.New(e.CodeUnverifiedRef,
				"publication artifact digests must be exact, sorted, and duplicate-free")
		}
		artifacts[digest] = true
	}
	previous := ""
	seenSubjectTypes := map[string]bool{}
	for _, referrer := range manifest.Referrers {
		key := publicationReferrerKey(referrer)
		subjectType := referrer.SubjectDigest + "\x00" + referrer.ArtifactType
		if !artifacts[referrer.SubjectDigest] || !canonicalArtifactType(referrer.ArtifactType) ||
			!isDigest(referrer.IdentityDigest) || !isDigest(referrer.BlobDigest) ||
			(previous != "" && previous >= key) || seenSubjectTypes[subjectType] {
			return e.New(e.CodeUnverifiedRef,
				"publication referrers must bind one exact row per subject/media-type in canonical order")
		}
		seenSubjectTypes[subjectType] = true
		previous = key
	}
	return nil
}

func (manifest PublicationManifest) Digest() (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindPublicationManifest, manifest)
}

func buildPublicationManifest(request AuthorizationRequest, publicationIdentity string,
	artifactDigests []string, proofs []evidenceProof,
) (PublicationManifest, error) {
	manifest := PublicationManifest{SchemaVersion: PublicationManifestSchemaVersion,
		PublicationIdentity: publicationIdentity,
		ArtifactDigests:     append([]string(nil), artifactDigests...), Referrers: []PublicationReferrer{}}
	sort.Strings(manifest.ArtifactDigests)
	ociSubjects := map[string]bool{}
	for _, component := range request.Inventory.Components {
		for _, binding := range component.ArtifactKinds {
			if binding.Kind == ArtifactOCIImage {
				ociSubjects[binding.ArtifactDigest] = true
			}
		}
	}
	if len(ociSubjects) == 0 {
		if err := manifest.Validate(); err != nil {
			return PublicationManifest{}, err
		}
		return manifest, nil
	}

	vectors := make(map[string]struct {
		blob       string
		components []attest.ComponentDigest
	}, len(request.ValidationVectors))
	for _, receipt := range request.ValidationVectors {
		identity, blob, _, components, err := receipt.PublicationBinding()
		if err != nil {
			return PublicationManifest{}, err
		}
		if _, duplicate := vectors[identity]; duplicate {
			return PublicationManifest{}, e.New(e.CodeGateUnmet,
				"publication repeats authenticated validation vector %s", identity)
		}
		vectors[identity] = struct {
			blob       string
			components []attest.ComponentDigest
		}{blob: blob, components: components}
	}

	for _, member := range request.Release.set.sorted().Members {
		containsOCI := false
		for _, artifact := range member.ArtifactDigests {
			containsOCI = containsOCI || ociSubjects[artifact]
		}
		if !containsOCI {
			continue
		}
		vector, ok := vectors[member.VectorDigest]
		if !ok {
			return PublicationManifest{}, e.New(e.CodeGateUnmet,
				"OCI member %s/%s lacks an authenticated validation-vector publication receipt %s",
				member.Component, orNone(member.Role), member.VectorDigest)
		}
		componentArtifacts := map[string]bool{}
		for _, component := range vector.components {
			componentArtifacts[component.SHA256] = true
		}
		for _, artifact := range member.ArtifactDigests {
			if ociSubjects[artifact] && !componentArtifacts[artifact] {
				return PublicationManifest{}, e.New(e.CodeGateUnmet,
					"validation vector %s does not bind OCI artifact %s", member.VectorDigest, artifact)
			}
			if ociSubjects[artifact] {
				manifest.Referrers = append(manifest.Referrers, PublicationReferrer{
					SubjectDigest: artifact, ArtifactType: ReferrerValidationVector,
					IdentityDigest: member.VectorDigest, BlobDigest: vector.blob,
				})
			}
		}
	}
	// StatusCheckpoint.Target is PublicationIdentity, never a component artifact.
	// Attaching it to every OCI component would create false subject semantics.
	// The checkpoint is deliberately absent until the non-circular clearance-root
	// and PublicationIdentity discovery protocol can carry it; transport fails
	// closed through Authorization.RequirePublicationCommit in the meantime.
	for _, proof := range proofs {
		if !ociSubjects[proof.ArtifactDigest] {
			continue
		}
		// External upstream evidence remains at its separately authenticated
		// transport reference. The locally generated artifact-scoped SBOM and
		// provenance rows are the publishable payloads for this subject; emitting
		// the external dependency again would create a second same-media-type row
		// and falsely imply that NKD owns those upstream bytes.
		if proof.Reference != "" {
			continue
		}
		artifactType := evidenceArtifactType(proof.ReceiptType)
		if artifactType == "" || !isDigest(proof.BlobDigest) {
			return PublicationManifest{}, e.New(e.CodeGateUnmet,
				"qualified evidence for OCI artifact %s has no publishable typed blob", proof.ArtifactDigest)
		}
		manifest.Referrers = append(manifest.Referrers, PublicationReferrer{
			SubjectDigest: proof.ArtifactDigest, ArtifactType: artifactType,
			IdentityDigest: proof.EvidenceDigest, BlobDigest: proof.BlobDigest,
		})
	}
	sort.Slice(manifest.Referrers, func(i, j int) bool {
		return publicationReferrerKey(manifest.Referrers[i]) < publicationReferrerKey(manifest.Referrers[j])
	})
	if err := manifest.Validate(); err != nil {
		return PublicationManifest{}, err
	}
	return manifest, nil
}

func evidenceArtifactType(evidenceType EvidenceType) string {
	switch evidenceType {
	case EvidenceSBOM:
		return ReferrerSBOM
	case EvidenceProvenance:
		return ReferrerProvenance
	case EvidenceVulnerability:
		return ReferrerVulnerability
	default:
		return ""
	}
}

func publicationReferrerKey(referrer PublicationReferrer) string {
	return referrer.SubjectDigest + "\x00" + referrer.ArtifactType + "\x00" +
		referrer.IdentityDigest + "\x00" + referrer.BlobDigest
}

func canonicalArtifactType(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	slash := false
	for _, character := range value {
		switch {
		case character == '/':
			if slash {
				return false
			}
			slash = true
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
		case character == '!', character == '#', character == '$', character == '&', character == '^',
			character == '_', character == '.', character == '+', character == '-':
		default:
			return false
		}
	}
	return slash
}
