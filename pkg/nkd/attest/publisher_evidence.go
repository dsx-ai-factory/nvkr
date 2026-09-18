package attest

import (
	"context"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

const PublisherEvidenceSchemaVersion = "nkd.dev/publisher-evidence/v2"

// publisherEvidenceRow is the exact artifact scope of both locally generated
// evidence families. Semantic identities and transport blob digests are both bound
// so a later publisher cannot substitute different bytes behind an index digest.
type publisherEvidenceRow struct {
	Component            string `json:"component"`
	ArtifactDigest       string `json:"artifactDigest"`
	SBOMDigest           string `json:"sbomDigest"`
	SBOMBlobDigest       string `json:"sbomBlobDigest"`
	SBOMEvidenceSchema   string `json:"sbomEvidenceSchema"`
	ProvenanceDigest     string `json:"provenanceDigest"`
	ProvenanceBlobDigest string `json:"provenanceBlobDigest"`
	ProvenanceSchema     string `json:"provenanceEvidenceSchema"`
}

type publisherEvidenceCore struct {
	SchemaVersion         string                 `json:"schemaVersion"`
	BuildDigest           string                 `json:"buildDigest"`
	SBOMIndexDigest       string                 `json:"sbomIndexDigest"`
	ProvenanceIndexDigest string                 `json:"provenanceIndexDigest"`
	Artifacts             []publisherEvidenceRow `json:"artifacts"`
}

// PublisherEvidenceArtifactBinding is the non-authoritative audit projection of
// one row in a qualified publisher aggregate. Both evidence schemas are explicit
// so a future schema cannot be substituted under an old semantic family.
type PublisherEvidenceArtifactBinding struct {
	Component            string `json:"component"`
	ArtifactDigest       string `json:"artifactDigest"`
	SBOMDigest           string `json:"sbomDigest"`
	SBOMBlobDigest       string `json:"sbomBlobDigest"`
	SBOMEvidenceSchema   string `json:"sbomEvidenceSchema"`
	ProvenanceDigest     string `json:"provenanceDigest"`
	ProvenanceBlobDigest string `json:"provenanceBlobDigest"`
	ProvenanceSchema     string `json:"provenanceEvidenceSchema"`
}

// PublisherEvidenceBinding is a defensive data projection used to bind the
// authenticated vector to the exact local receipt aggregate during release. It is
// not itself an authority capability.
type PublisherEvidenceBinding struct {
	SchemaVersion         string                             `json:"schemaVersion"`
	BuildDigest           string                             `json:"buildDigest"`
	SBOMIndexDigest       string                             `json:"sbomIndexDigest"`
	ProvenanceIndexDigest string                             `json:"provenanceIndexDigest"`
	EvidenceDigest        string                             `json:"evidenceDigest"`
	Artifacts             []PublisherEvidenceArtifactBinding `json:"artifacts"`
}

// Validate checks the projection's complete domain-separated core. It does not
// confer publisher authority; that comes only from the opaque receipts or from an
// independently authenticated vector/clearance containing the binding.
func (binding PublisherEvidenceBinding) Validate() error {
	if binding.SchemaVersion != PublisherEvidenceSchemaVersion ||
		!canonical.IsDigest(binding.BuildDigest) ||
		!canonical.IsDigest(binding.SBOMIndexDigest) ||
		!canonical.IsDigest(binding.ProvenanceIndexDigest) ||
		!canonical.IsDigest(binding.EvidenceDigest) || len(binding.Artifacts) == 0 {
		return e.New(e.CodeUnverifiedRef, "publisher evidence binding is incomplete")
	}
	core := publisherEvidenceCore{SchemaVersion: binding.SchemaVersion,
		BuildDigest: binding.BuildDigest, SBOMIndexDigest: binding.SBOMIndexDigest,
		ProvenanceIndexDigest: binding.ProvenanceIndexDigest}
	for index, row := range binding.Artifacts {
		if strings.TrimSpace(row.Component) == "" || strings.TrimSpace(row.Component) != row.Component ||
			(index > 0 && binding.Artifacts[index-1].Component >= row.Component) ||
			!canonical.IsDigest(row.ArtifactDigest) || !canonical.IsDigest(row.SBOMDigest) ||
			!canonical.IsDigest(row.SBOMBlobDigest) || !canonical.IsDigest(row.ProvenanceDigest) ||
			!canonical.IsDigest(row.ProvenanceBlobDigest) || row.SBOMEvidenceSchema != "cyclonedx-json/1.5" ||
			row.ProvenanceSchema != "in-toto-slsa-provenance/v1" {
			return e.New(e.CodeUnverifiedRef,
				"publisher evidence binding contains an invalid artifact row")
		}
		core.Artifacts = append(core.Artifacts, publisherEvidenceRow(row))
	}
	digest, err := canonical.Digest(canonical.KindPublisherEvidence, core)
	if err != nil {
		return err
	}
	if digest != binding.EvidenceDigest {
		return e.New(e.CodeUnverifiedRef,
			"publisher evidence binding does not match its domain-separated digest")
	}
	return nil
}

// PublisherEvidence is an opaque publisher-side capability. It is distinct from
// operator reproduction/conformance observations: those later facts belong to
// release receipts and never rewrite the signed ValidationVector's evidence core.
type PublisherEvidence struct {
	valid              bool
	core               publisherEvidenceCore
	digest             string
	sbomReceipts       []sbom.LocalEvidenceReceipt
	provenanceReceipts []provenance.LocalEvidenceReceipt
}

// QualifyPublisherEvidence joins locally generated SBOM and provenance capabilities
// by exact build, component, and artifact digest. Caller strings never select the
// aggregate evidenceDigest.
func QualifyPublisherEvidence(ctx context.Context, sboms *sbom.VerifiedSet,
	provenanceSet *provenance.QualifiedSet,
) (*PublisherEvidence, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "publisher evidence qualification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualifying publisher evidence")
	}
	_, sbomIndex, err := sboms.PerArtifact()
	if err != nil {
		return nil, err
	}
	provenanceIndex, err := provenanceSet.Index()
	if err != nil {
		return nil, err
	}
	if sbomIndex.BuildDigest != provenanceIndex.BuildDigest {
		return nil, e.New(e.CodeUnverifiedRef,
			"publisher SBOM build %s does not match provenance build %s",
			sbomIndex.BuildDigest, provenanceIndex.BuildDigest)
	}
	sbomIndexDigest, err := sbomIndex.Digest()
	if err != nil {
		return nil, err
	}
	provenanceIndexDigest, err := provenanceIndex.Digest()
	if err != nil {
		return nil, err
	}
	sbomReceipts, err := sboms.LocalReceipts()
	if err != nil {
		return nil, err
	}
	provenanceReceipts, err := provenanceSet.LocalReceipts()
	if err != nil {
		return nil, err
	}
	qualified, err := QualifyPublisherEvidenceReceipts(ctx, sbomReceipts, provenanceReceipts)
	if err != nil {
		return nil, err
	}
	if qualified.core.SBOMIndexDigest != sbomIndexDigest ||
		qualified.core.ProvenanceIndexDigest != provenanceIndexDigest {
		return nil, e.New(e.CodeUnverifiedRef,
			"publisher receipt aggregate does not match its qualified SBOM/provenance indexes")
	}
	return qualified, nil
}

// QualifyPublisherEvidenceReceipts reconstructs the publisher aggregate from only
// opaque, exact-byte local evidence capabilities. It is used at the release seam to
// prove that a signed vector's aggregate fields name these exact artifact-scoped
// receipts, rather than caller strings.
func QualifyPublisherEvidenceReceipts(ctx context.Context, sbomReceipts []sbom.LocalEvidenceReceipt,
	provenanceReceipts []provenance.LocalEvidenceReceipt,
) (*PublisherEvidence, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "publisher evidence qualification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualifying publisher evidence receipts")
	}
	if len(sbomReceipts) == 0 || len(sbomReceipts) != len(provenanceReceipts) {
		return nil, e.New(e.CodeProvenanceMissing,
			"publisher evidence requires one SBOM and provenance statement per artifact")
	}
	type provenanceEntry struct {
		receipt provenance.LocalEvidenceReceipt
		binding provenance.LocalEvidenceBinding
	}
	provenanceByComponent := make(map[string]provenanceEntry, len(provenanceReceipts))
	for _, receipt := range provenanceReceipts {
		binding, err := receipt.Binding()
		if err != nil {
			return nil, err
		}
		if _, duplicate := provenanceByComponent[binding.Component]; duplicate {
			return nil, e.New(e.CodeProvenanceMissing,
				"publisher provenance repeats component %q", binding.Component)
		}
		provenanceByComponent[binding.Component] = provenanceEntry{receipt: receipt, binding: binding}
	}
	type pair struct {
		sbomReceipt       sbom.LocalEvidenceReceipt
		provenanceReceipt provenance.LocalEvidenceReceipt
		sbomBinding       sbom.LocalEvidenceBinding
		provenanceBinding provenance.LocalEvidenceBinding
	}
	pairs := make([]pair, 0, len(sbomReceipts))
	for _, receipt := range sbomReceipts {
		sbomBinding, err := receipt.Binding()
		if err != nil {
			return nil, err
		}
		entry, ok := provenanceByComponent[sbomBinding.Component]
		if !ok || entry.binding.BuildDigest != sbomBinding.BuildDigest ||
			entry.binding.ArtifactDigest != sbomBinding.ArtifactDigest {
			return nil, e.New(e.CodeUnverifiedRef,
				"publisher evidence does not bind the same exact subject for component %q",
				sbomBinding.Component)
		}
		pairs = append(pairs, pair{sbomReceipt: receipt, provenanceReceipt: entry.receipt,
			sbomBinding: sbomBinding, provenanceBinding: entry.binding})
		delete(provenanceByComponent, sbomBinding.Component)
	}
	if len(provenanceByComponent) != 0 {
		return nil, e.New(e.CodeProvenanceMissing,
			"publisher provenance contains subjects absent from the SBOM set")
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].sbomBinding.Component < pairs[j].sbomBinding.Component })
	buildDigest := pairs[0].sbomBinding.BuildDigest
	sbomIndex := &sbom.Index{SchemaVersion: sbom.IndexSchemaVersion, BuildDigest: buildDigest}
	provenanceIndex := &provenance.Index{SchemaVersion: provenance.IndexSchemaVersion, BuildDigest: buildDigest}
	core := publisherEvidenceCore{SchemaVersion: PublisherEvidenceSchemaVersion, BuildDigest: buildDigest}
	sortedSBOMReceipts := make([]sbom.LocalEvidenceReceipt, 0, len(pairs))
	sortedProvenanceReceipts := make([]provenance.LocalEvidenceReceipt, 0, len(pairs))
	for index, item := range pairs {
		if item.sbomBinding.BuildDigest != buildDigest || item.provenanceBinding.BuildDigest != buildDigest ||
			(index > 0 && pairs[index-1].sbomBinding.Component >= item.sbomBinding.Component) {
			return nil, e.New(e.CodeUnverifiedRef,
				"publisher evidence receipts do not form one canonical build-scoped set")
		}
		statementBytes, err := item.provenanceReceipt.ExactBytes()
		if err != nil {
			return nil, err
		}
		var statement provenance.Statement
		if err := canonical.UnmarshalExact(statementBytes, &statement); err != nil {
			return nil, err
		}
		sbomIndex.Entries = append(sbomIndex.Entries, sbom.IndexEntry{
			Artifact: item.sbomBinding.Component, SBOMDigest: item.sbomBinding.EvidenceDigest,
			ArtifactSHA: item.sbomBinding.ArtifactDigest,
		})
		provenanceIndex.Statements = append(provenanceIndex.Statements, statement)
		core.Artifacts = append(core.Artifacts, publisherEvidenceRow{
			Component: item.sbomBinding.Component, ArtifactDigest: item.sbomBinding.ArtifactDigest,
			SBOMDigest: item.sbomBinding.EvidenceDigest, SBOMBlobDigest: item.sbomBinding.BlobDigest,
			SBOMEvidenceSchema:   item.sbomBinding.EvidenceSchema,
			ProvenanceDigest:     item.provenanceBinding.EvidenceDigest,
			ProvenanceBlobDigest: item.provenanceBinding.BlobDigest,
			ProvenanceSchema:     item.provenanceBinding.EvidenceSchema,
		})
		sortedSBOMReceipts = append(sortedSBOMReceipts, item.sbomReceipt)
		sortedProvenanceReceipts = append(sortedProvenanceReceipts, item.provenanceReceipt)
	}
	var err error
	core.SBOMIndexDigest, err = sbomIndex.Digest()
	if err != nil {
		return nil, err
	}
	core.ProvenanceIndexDigest, err = provenanceIndex.Digest()
	if err != nil {
		return nil, err
	}
	digest, err := canonical.Digest(canonical.KindPublisherEvidence, core)
	if err != nil {
		return nil, err
	}
	return &PublisherEvidence{valid: true, core: core, digest: digest,
		sbomReceipts: sortedSBOMReceipts, provenanceReceipts: sortedProvenanceReceipts}, nil
}

func (evidence *PublisherEvidence) validate() error {
	if evidence == nil || !evidence.valid || evidence.core.SchemaVersion != PublisherEvidenceSchemaVersion ||
		!canonical.IsDigest(evidence.core.BuildDigest) || !canonical.IsDigest(evidence.digest) ||
		len(evidence.core.Artifacts) == 0 {
		return e.New(e.CodeProvenanceMissing,
			"publisher evidence capability is absent or invalid")
	}
	if len(evidence.sbomReceipts) != len(evidence.core.Artifacts) ||
		len(evidence.provenanceReceipts) != len(evidence.core.Artifacts) {
		return e.New(e.CodeProvenanceMissing,
			"publisher evidence capability lost its artifact-scoped receipts")
	}
	recomputed, err := canonical.Digest(canonical.KindPublisherEvidence, evidence.core)
	if err != nil {
		return err
	}
	if recomputed != evidence.digest {
		return e.New(e.CodeUnverifiedRef,
			"publisher evidence core changed after qualification")
	}
	for index, row := range evidence.core.Artifacts {
		if index > 0 && evidence.core.Artifacts[index-1].Component >= row.Component {
			return e.New(e.CodeProvenanceMissing,
				"publisher evidence artifacts are not canonical")
		}
		for _, digest := range []string{row.ArtifactDigest, row.SBOMDigest, row.SBOMBlobDigest,
			row.ProvenanceDigest, row.ProvenanceBlobDigest} {
			if !canonical.IsDigest(digest) {
				return e.New(e.CodeUnverifiedRef,
					"publisher evidence artifact %q lost an exact digest", row.Component)
			}
		}
		if row.SBOMEvidenceSchema != "cyclonedx-json/1.5" ||
			row.ProvenanceSchema != "in-toto-slsa-provenance/v1" {
			return e.New(e.CodeUnverifiedRef,
				"publisher evidence artifact %q has unsupported evidence schemas %q/%q",
				row.Component, row.SBOMEvidenceSchema, row.ProvenanceSchema)
		}
	}
	return nil
}

// EvidenceRef returns the only vector evidence block this capability can issue.
func (evidence *PublisherEvidence) EvidenceRef() (*EvidenceRef, string, error) {
	if err := evidence.validate(); err != nil {
		return nil, "", err
	}
	return &EvidenceRef{SBOMDigest: evidence.core.SBOMIndexDigest,
		ProvenanceDigest: evidence.core.ProvenanceIndexDigest}, evidence.digest, nil
}

// Binding returns a defensive, non-authoritative projection of the exact evidence
// aggregate. Release stores this inside its authenticated clearance so the signed
// vector/evidence relationship remains auditable across process boundaries.
func (evidence *PublisherEvidence) Binding() (PublisherEvidenceBinding, error) {
	if err := evidence.validate(); err != nil {
		return PublisherEvidenceBinding{}, err
	}
	binding := PublisherEvidenceBinding{
		SchemaVersion: evidence.core.SchemaVersion, BuildDigest: evidence.core.BuildDigest,
		SBOMIndexDigest:       evidence.core.SBOMIndexDigest,
		ProvenanceIndexDigest: evidence.core.ProvenanceIndexDigest,
		EvidenceDigest:        evidence.digest,
		Artifacts:             make([]PublisherEvidenceArtifactBinding, len(evidence.core.Artifacts)),
	}
	for index, row := range evidence.core.Artifacts {
		binding.Artifacts[index] = PublisherEvidenceArtifactBinding(row)
	}
	return binding, nil
}

// VerifyAgainstPublishedVector compares a freshly qualified local evidence
// aggregate with an authenticated, publishable validation vector. The receipt type
// is deliberate: a decoded Manifest or caller-provided digest strings cannot enter
// this authority boundary. Aggregate digests and the exact component/artifact set
// must all match.
func (evidence *PublisherEvidence) VerifyAgainstPublishedVector(ctx context.Context,
	receipt PublishedVectorReceipt,
) error {
	if ctx == nil {
		return e.New(e.CodeUsage,
			"publisher evidence comparison requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err,
			"comparing publisher evidence with authenticated validation vector")
	}
	local, err := evidence.Binding()
	if err != nil {
		return err
	}
	published, err := receipt.ReleaseBinding()
	if err != nil {
		return err
	}
	if local.BuildDigest != published.BuildDigest ||
		local.SBOMIndexDigest != published.SBOMIndexDigest ||
		local.ProvenanceIndexDigest != published.ProvenanceIndexDigest ||
		local.EvidenceDigest != published.PublisherEvidenceDigest {
		return e.New(e.CodeVerifyFailed,
			"qualified publisher evidence aggregates do not match the authenticated validation vector")
	}
	if len(local.Artifacts) != len(published.Components) {
		return e.New(e.CodeVerifyFailed,
			"qualified publisher evidence covers %d artifacts, authenticated vector covers %d; a selected subset cannot claim full publisher-evidence equivalence",
			len(local.Artifacts), len(published.Components))
	}
	for index, row := range local.Artifacts {
		component := published.Components[index]
		if row.Component != component.Name || row.ArtifactDigest != component.SHA256 {
			return e.New(e.CodeVerifyFailed,
				"qualified publisher evidence artifact %s/%s does not match authenticated vector artifact %s/%s",
				row.Component, row.ArtifactDigest, component.Name, component.SHA256)
		}
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err,
			"comparing publisher evidence with authenticated validation vector")
	}
	return nil
}

// LocalReceipts returns defensive copies for the release evidence gate. It keeps
// publisher generation and operator reproduction observations as separate inputs.
func (evidence *PublisherEvidence) LocalReceipts() ([]sbom.LocalEvidenceReceipt,
	[]provenance.LocalEvidenceReceipt, error,
) {
	if err := evidence.validate(); err != nil {
		return nil, nil, err
	}
	return append([]sbom.LocalEvidenceReceipt(nil), evidence.sbomReceipts...),
		append([]provenance.LocalEvidenceReceipt(nil), evidence.provenanceReceipts...), nil
}

func (evidence *PublisherEvidence) bindManifest(manifest *Manifest) error {
	if err := evidence.validate(); err != nil {
		return err
	}
	if manifest == nil || manifest.BuildDigest != evidence.core.BuildDigest ||
		len(manifest.Components) != len(evidence.core.Artifacts) {
		return e.New(e.CodeUnverifiedRef,
			"publisher evidence does not cover the validation vector's exact build/artifact set")
	}
	for index, component := range manifest.Components {
		row := evidence.core.Artifacts[index]
		if component.Name != row.Component || component.SHA256 != row.ArtifactDigest {
			return e.New(e.CodeUnverifiedRef,
				"publisher evidence subject %s/%s does not match vector component %s/%s",
				row.Component, row.ArtifactDigest, component.Name, component.SHA256)
		}
	}
	return nil
}
