package attest

import (
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
)

// EvidenceSchemaVersion identifies the evidence-bundle shape.
const EvidenceSchemaVersion = "nkd.dev/evidence-bundle/v2"

// Verdict is the top-level outcome recorded in an evidence bundle.
const (
	VerdictReproduced = "reproduced" // byte-identical to the reference under the same profile
	VerdictRejected   = "rejected"   // fail closed
)

// EvidenceBundle is an UNQUALIFIED, diagnosable reproduction observation: it makes
// a verifier decision explainable rather than merely detectable (§8.4.2), and is
// deterministically serializable for audit. Serialization does not preserve the
// verifier's same-process authority and does not make this object a signed receipt.
// Before any downstream authority or release use, its exact bytes and subject MUST
// be authenticated and promoted into the applicable typed reproduction receipt.
// The bundle carries the profile the build ran under, the conformance claim and
// validation-vector reference, the preflight divergences, and per-component verdicts
// with stable mismatch cause codes.
type EvidenceBundle struct {
	SchemaVersion       string              `json:"schemaVersion"`
	BuildDigest         string              `json:"buildDigest"`
	ProfileDigest       string              `json:"profileDigest,omitempty"`
	ConformanceClaim    string              `json:"conformanceClaim,omitempty"`
	ValidationVectorRef string              `json:"validationVectorRef,omitempty"`
	Verdict             string              `json:"verdict"`
	Preflight           []PreflightRow      `json:"preflight,omitempty"`
	Components          []ComponentEvidence `json:"components"`
	DiskManifests       []DiskManifestRef   `json:"diskManifests,omitempty"`
}

// PreflightRow is one host-vs-profile divergence recorded at preflight.
type PreflightRow struct {
	Field    string `json:"field"`
	Want     string `json:"want"`
	Got      string `json:"got"`
	HardFail bool   `json:"hardFail"`
}

// MismatchCause is a stable diagnostic classification for an artifact mismatch.
type MismatchCause string

// CauseContentMismatch reports that the built bytes differ from the reference.
const CauseContentMismatch MismatchCause = "CONTENT_MISMATCH"

// ComponentEvidence is one component's verify verdict with its mismatch cause.
type ComponentEvidence struct {
	Name       string        `json:"name"`
	OutputType string        `json:"outputType,omitempty"`
	Expected   string        `json:"expected"`
	Built      string        `json:"built"`
	Match      bool          `json:"match"`
	Cause      MismatchCause `json:"cause,omitempty"`
}

// DiskManifestRef ties a disk artifact to its per-block canonical (RAW logical-disk)
// manifest so a disk-image mismatch is locatable without the reference bytes
// (NKD v1 §8.4.2): the aggregate logical digest plus a block-size and the count of
// block digests published alongside.
type DiskManifestRef struct {
	Artifact       string `json:"artifact"`
	LogicalDigest  string `json:"logicalDigest"` // sha256 over the decoded RAW LBA stream (the canonical boundary)
	BlockSizeBytes int    `json:"blockSizeBytes"`
	BlockCount     int    `json:"blockCount"`
}

// NewEvidence assembles a deterministic evidence bundle from a manifest, the verify
// decision, and the preflight result.
func NewEvidence(m *Manifest, verdict string, preflight []profile.Divergence, comps []ComponentEvidence) *EvidenceBundle {
	eb := &EvidenceBundle{
		SchemaVersion:       EvidenceSchemaVersion,
		BuildDigest:         m.BuildDigest,
		ProfileDigest:       m.ProfileDigest,
		ConformanceClaim:    m.ConformanceClaim,
		ValidationVectorRef: m.ValidationVectorRef,
		Verdict:             verdict,
	}
	for _, d := range preflight {
		eb.Preflight = append(eb.Preflight, PreflightRow{Field: d.Field, Want: d.Want, Got: d.Got, HardFail: d.HardFail})
	}
	sort.Slice(eb.Preflight, func(i, j int) bool { return eb.Preflight[i].Field < eb.Preflight[j].Field })
	eb.Components = append(eb.Components, comps...)
	sort.Slice(eb.Components, func(i, j int) bool { return eb.Components[i].Name < eb.Components[j].Name })
	return eb
}

// Evidence builds a deterministic evidence bundle for a verify decision: the
// reference manifest (receiver) vs the freshly-built manifest, the verdict, the
// preflight divergences. Per-component rows carry the match verdict and, on a
// mismatch, a stable cause code. Reference metadata (build/profile/conformance) comes from the
// receiver; buildDigest and profile are already asserted equal by Check.
func (want *Manifest) BuildEvidence(got *Manifest, verdict string, preflight []profile.Divergence) *EvidenceBundle {
	wantByName := make(map[string]ComponentDigest, len(want.Components))
	for _, w := range want.Components {
		wantByName[w.Name] = w
	}
	built := append([]ComponentDigest(nil), got.Components...)
	sort.Slice(built, func(i, j int) bool { return built[i].Name < built[j].Name })
	comps := make([]ComponentEvidence, 0, len(built))
	for _, g := range built {
		ce := ComponentEvidence{Name: g.Name, OutputType: g.OutputType, Built: g.SHA256}
		if w, ok := wantByName[g.Name]; ok {
			ce.Expected = w.SHA256
			ce.Match = w.SHA256 == g.SHA256
		}
		if !ce.Match {
			ce.Cause = CauseContentMismatch
		}
		comps = append(comps, ce)
	}
	// Metadata is the reference's (build/profile/conformance identity).
	meta := &Manifest{
		BuildDigest:   want.BuildDigest,
		ProfileDigest: want.ProfileDigest, ConformanceClaim: want.ConformanceClaim,
		ValidationVectorRef: want.ValidationVectorRef,
	}
	return NewEvidence(meta, verdict, preflight, comps)
}

// AddDiskManifest records a disk artifact's canonical logical-disk (RAW) manifest.
func (eb *EvidenceBundle) AddDiskManifest(ref DiskManifestRef) {
	eb.DiskManifests = append(eb.DiskManifests, ref)
	sort.Slice(eb.DiskManifests, func(i, j int) bool { return eb.DiskManifests[i].Artifact < eb.DiskManifests[j].Artifact })
}

// Marshal renders the unqualified diagnostic bundle deterministically. The returned
// bytes are evidence payload, not a portable authority capability or signed receipt.
func (eb *EvidenceBundle) Marshal() ([]byte, error) {
	return canonical.Marshal(eb) // canonical on-disk form (ADR-01 §4)
}

// Digest is the stable identity of this reproduction observation (ADR-05 §4 /
// ADR-01 codec). A digest makes substitution detectable only when a separately
// authenticated authority object binds it; the digest itself grants no authority.
func (eb *EvidenceBundle) Digest() (string, error) {
	return canonical.Digest(canonical.KindReproductionObservation, eb)
}
