// Package attest binds qualified validation vectors and publisher evidence to exact
// build, execution, and artifact identities. Diagnostic projections remain
// unauthenticated until the owning receipt checks succeed.
package attest

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// SchemaVersion identifies the manifest / ValidationVector shape.
const SchemaVersion = "nkd.dev/validation-vector/v3"

var (
	canonicalIdentity = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
)

// ValidationVector is the ADR-05 name for the one signed object binding a build +
// builder profile to expected artifacts and evidence. NKD publishes ONE such object,
// not a separate "manifest" and "vector" — Manifest is its Go type; this alias makes
// the ADR-05 vocabulary usable directly.
type ValidationVector = Manifest

// BuilderProfileRef is the ADR-05 §1 structured profile binding (from-source subset
// only): the label, the authoritative digest, and the host-control class. The digest
// is what verify compares; id/hostControl are descriptive.
type BuilderProfileRef struct {
	ID          string `json:"id,omitempty"`
	Digest      string `json:"digest,omitempty"`
	HostControl string `json:"hostControl,omitempty"`
}

// EvidenceRef is the ADR-05 publisher evidence block. Publishable vectors bind only
// deterministic SBOM and provenance aggregates. Vulnerability observations belong
// to later independently authenticated release receipts; ValidatePublished rejects
// the diagnostic vulnerability field. Deployment qualification has no vector slot.
type EvidenceRef struct {
	SBOMDigest       string `json:"sbomDigest,omitempty"`
	ProvenanceDigest string `json:"provenanceDigest,omitempty"`
	// VulnReportDigest is the deterministic vuln-report digest (ADR-13 §3), bound to
	// the same build + pinned scanner DB — scan-time omitted so it is reproducible.
	VulnReportDigest string `json:"vulnReportDigest,omitempty"`
}

// ComponentDigest is one built artifact's expected content digest, bound to the
// digest-pinned toolchain image that produced it (provenance, NKD-FR-REP-009). The
// toolchain is also part of the IR and thus buildDigest, so a toolchain change is
// already caught by the buildDigest check; recording it per-component makes the
// manifest self-describing for mirroring/audit.
type ComponentDigest struct {
	Name       string `json:"name"`
	OutputType string `json:"outputType"`
	// Toolchain is the transport-free OCI manifest identity (a bare exact sha256),
	// never a registry/name reference. Online and mirrored executions therefore
	// produce byte-identical T2 validation-vector components.
	Toolchain string `json:"toolchain,omitempty"`
	// InputDigest is the runner-observed identity of the exact post-vendor,
	// post-patch source closure. It prevents a matching declaration from masking a
	// dirty checkout, an unpinned vendor closure, or patches that were not applied.
	InputDigest string `json:"inputDigest,omitempty"`
	// BuilderType is the DECLARED build substrate; EngineClass is what the runner
	// OBSERVED. Verify fails closed if they diverge (ADR-07 §1).
	BuilderType string `json:"builderType,omitempty"`
	EngineClass string `json:"engineClass,omitempty"`
	SHA256      string `json:"sha256"`
}

// Manifest is the expected-digests document — reconciled toward NKD v1's validation
// vector (§8.4). It contains no host/clock/random values, so a publisher and an
// operator building the same build under the same profile emit byte-equal manifests.
//
// Byte-reproducibility is bounded to a builder profile (NKD v1 §10.1): ProfileDigest
// binds this manifest to the profile it was produced under; a verify under a
// different profile cannot claim the Reproduced state. ConformanceClaim +
// ValidationVectorRef carry the Kubernetes-conformance assertion and its evidence
// reference (§8.1).
type Manifest struct {
	SchemaVersion string `json:"schemaVersion"`
	BuildDigest   string `json:"buildDigest"`
	// ProfileDigest is the authoritative from-source profile binding compared by
	// verify. BuilderProfile carries the same digest plus the id/hostControl the
	// ADR-05 schema calls for; both are empty for an all-prebuilt vector (ADR-07 §6).
	ProfileDigest       string                 `json:"profileDigest,omitempty"`
	BuilderProfile      *BuilderProfileRef     `json:"builderProfile,omitempty"`
	ConformanceClaim    string                 `json:"conformanceClaim,omitempty"`
	ValidationVectorRef string                 `json:"validationVectorRef,omitempty"`
	Evidence            *EvidenceRef           `json:"evidence,omitempty"`
	EvidenceDigest      string                 `json:"evidenceDigest,omitempty"` // aggregate evidence-bundle digest (ADR-05 §4)
	AICR                *AICRRef               `json:"aicr,omitempty"`
	Components          []ComponentDigest      `json:"components"` // the artifacts[] of the vector (ADR-05 §1)
	qualification       *manifestQualification `json:"-"`
}

// Close revokes the retained same-process qualification capability. Callers that
// receive a manifest from FromQualifiedReport or Client.Attest should defer Close
// after their final qualified comparison or marshal. Parsed and diagnostic manifests
// own no such authority, so Close is a no-op for them. Public data remains available
// for diagnostic display.
func (m *Manifest) Close() error {
	if m == nil || m.qualification == nil {
		return nil
	}
	report := m.qualification.report
	m.qualification = nil
	if report == nil {
		return nil
	}
	return report.Close()
}

// manifestQualification is the opaque, same-process authority that distinguishes a
// validation observation constructed from terminally qualified artifact results from
// a caller-composed Manifest. It deliberately is not serialized: published vectors
// gain authority from their detached signature, while fresh observations gain it from
// the pipeline receipt. The binding detects mutation of either projection.
type manifestQualification struct {
	report             *pipeline.Report
	profileDigest      string
	executionBinding   string
	manifestBinding    string
	publisherEvidence  *PublisherEvidence
	publicationBinding string
}

// Identity is the ValidationVector's canonical content digest (ADR-01 codec), the
// basis for the immutable publicationIdentity (ADR-06 §1).
func (m *Manifest) Identity() (string, error) {
	if err := m.ValidatePublished(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindValidationVector, m)
}

// ValidatePublished applies the stricter contract for a signable ValidationVector.
// A report-derived draft is useful for comparisons, but it cannot acquire a vector
// identity or cross an authenticated consumer boundary until it binds both required
// supply-chain evidence families and the exact aggregate evidence bundle.
func (m *Manifest) ValidatePublished() error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Evidence == nil {
		return e.New(e.CodeProvenanceMissing,
			"published validation vector requires aggregate SBOM and provenance evidence")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"evidence.sbomDigest", m.Evidence.SBOMDigest},
		{"evidence.provenanceDigest", m.Evidence.ProvenanceDigest},
		{"evidenceDigest", m.EvidenceDigest},
	} {
		if !canonical.IsDigest(field.value) {
			return e.New(e.CodeProvenanceMissing,
				"published validation vector requires exact %s", field.name)
		}
	}
	if m.Evidence.VulnReportDigest != "" {
		return e.New(e.CodeProvenanceMissing,
			"published validation vector cannot embed later operator reproduction, conformance, or vulnerability observations")
	}
	if m.ConformanceClaim != "" || m.ValidationVectorRef != "" {
		return e.New(e.CodeProvenanceMissing,
			"published validation vector cannot embed legacy caller-authored conformance claims or validation references; conformance requires a separately authenticated receipt")
	}
	return nil
}

// AICRRef is the pinned AICR accelerated-runtime profile carried in the distribution
// manifest as provenance (NKD-FR-RC-013a). GeneratorDigest identifies the exact AICR
// executable selected by T1 and is required even when no bundle was rendered.
// BundleDigest is the authoritative identity of a rendered bundle tree; Bundle is
// only its descriptive transport/display location and never substitutes for the
// digest receipt.
type AICRRef struct {
	Identity        string `json:"identity"`
	Digest          string `json:"digest"`
	GeneratorDigest string `json:"generatorDigest"`
	Repo            string `json:"repo,omitempty"`
	Bundle          string `json:"bundle,omitempty"`
	BundleDigest    string `json:"bundleDigest,omitempty"`
}

// SetBuilderProfile records the full ADR-05 §1 profile binding (id + digest +
// host-control class), keeping ProfileDigest in sync as the comparison key.
func (m *Manifest) SetBuilderProfile(id, digest, hostControl string) {
	m.ProfileDigest = digest
	if digest == "" && id == "" && hostControl == "" {
		m.BuilderProfile = nil
		return
	}
	m.BuilderProfile = &BuilderProfileRef{ID: id, Digest: digest, HostControl: hostControl}
}

// AttachEvidence records the aggregate evidence digests (ADR-05 §1 evidence block) and
// the evidence-bundle aggregate digest (§4). Idempotent; nil clears.
func (m *Manifest) AttachEvidence(ev *EvidenceRef, evidenceDigest string) {
	m.Evidence = ev
	m.EvidenceDigest = evidenceDigest
	// Caller-authored strings are useful in diagnostic records but can never retain
	// publisher authority. A subsequent publication must attach a fresh opaque
	// PublisherEvidence capability.
	if m.qualification != nil {
		m.qualification.publisherEvidence = nil
		m.qualification.publicationBinding = ""
	}
}

// AttachPublisherEvidence derives the complete publishable evidence fields from an
// opaque capability and binds their exact projection to this qualified report. It is
// the only attachment path accepted by MarshalQualified.
func (m *Manifest) AttachPublisherEvidence(evidence *PublisherEvidence) error {
	if m == nil || m.qualification == nil {
		return e.New(e.CodePreflightFailed,
			"publisher evidence can attach only to a report-qualified validation vector")
	}
	if err := evidence.bindManifest(m); err != nil {
		return err
	}
	reference, digest, err := evidence.EvidenceRef()
	if err != nil {
		return err
	}
	m.Evidence = reference
	m.EvidenceDigest = digest
	binding, err := m.publicationAuthorityBinding()
	if err != nil {
		return err
	}
	m.qualification.publisherEvidence = evidence
	m.qualification.publicationBinding = binding
	return nil
}

// FromReport performs a pure, deterministic projection of a report. It does not
// inspect or preserve the report's opaque qualification receipt and therefore cannot
// authorize a signed vector or a Reproduced verdict.
//
// Authority consumers must use FromQualifiedReport and
// VerifyQualifiedAgainst. This projection is useful only for
// diagnostics and non-authoritative comparisons.
func FromReport(rep *pipeline.Report) *Manifest {
	if rep == nil {
		return nil
	}
	m := &Manifest{
		SchemaVersion: SchemaVersion,
		BuildDigest:   rep.BuildDigest,
	}
	for _, a := range rep.Artifacts {
		m.Components = append(m.Components, ComponentDigest{
			Name: a.Component, OutputType: a.OutputType, Toolchain: toolchainIdentity(a.Toolchain),
			InputDigest: a.InputDigest, BuilderType: a.BuilderType,
			EngineClass: a.EngineClass, SHA256: a.SHA256,
		})
	}
	sort.Slice(m.Components, func(i, j int) bool { return m.Components[i].Name < m.Components[j].Name })
	return m
}

// FromQualifiedReport is the sole pipeline-to-validation-vector authority seam. It
// accepts only a report whose terminal artifact verifier still proves its opaque
// stage-two execution (or profile-free byte-preserving acquisition) receipt. The
// returned manifest binds the authoritative profile and execution identities and
// detects later mutation of either the report or its manifest projection.
func FromQualifiedReport(ctx context.Context, rep *pipeline.Report) (*Manifest, error) {
	if rep == nil {
		return nil, e.New(e.CodePreflightFailed, "cannot construct a validation vector from a nil report")
	}
	if err := rep.RequireQualified(ctx); err != nil {
		return nil, err
	}
	m := FromReport(rep)
	profileDigest := rep.QualificationProfileDigest()
	executionBinding := rep.ExecutionBinding()
	if profileDigest != "" {
		m.SetBuilderProfile("", profileDigest, "")
	}
	if err := m.Validate(); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "qualified report produced an invalid validation vector")
	}
	binding, err := m.authorityBinding(profileDigest, executionBinding)
	if err != nil {
		return nil, err
	}
	m.qualification = &manifestQualification{
		report: rep, profileDigest: profileDigest,
		executionBinding: executionBinding, manifestBinding: binding,
	}
	return m, nil
}

// Validate enforces the semantic profile of the public ValidationVector schema. The
// canonical codec guarantees one byte representation; this method guarantees that
// those bytes have one unambiguous NKD meaning. In particular, a component cannot
// erase its declared builder to bypass source-closure or execution-identity checks.
func (m *Manifest) Validate() error {
	if m == nil {
		return e.New(e.CodeVerifyFailed, "validation vector is nil")
	}
	if m.SchemaVersion != SchemaVersion {
		return e.New(e.CodeVerifyFailed, "validation vector schemaVersion %q is unsupported; want %q", m.SchemaVersion, SchemaVersion)
	}
	if err := requireExactDigest("buildDigest", m.BuildDigest, true); err != nil {
		return err
	}
	if err := validateProfileBinding(m); err != nil {
		return err
	}
	if err := validateEvidence(m); err != nil {
		return err
	}
	if err := validateAICR(m.AICR); err != nil {
		return err
	}
	if len(m.Components) == 0 {
		return e.New(e.CodeVerifyFailed, "validation vector has no components")
	}

	componentNames := make(map[string]bool, len(m.Components))
	hasFromSource := false
	previous := ""
	for i, component := range m.Components {
		if !canonicalIdentity.MatchString(component.Name) {
			return e.New(e.CodeVerifyFailed, "component[%d] name %q is not a canonical nonempty identifier", i, component.Name)
		}
		if componentNames[component.Name] {
			return e.New(e.CodeVerifyFailed, "validation vector contains duplicate component %q", component.Name)
		}
		if i > 0 && previous >= component.Name {
			return e.New(e.CodeVerifyFailed, "validation vector components are not strictly sorted by name: %q precedes %q", previous, component.Name)
		}
		componentNames[component.Name] = true
		previous = component.Name
		if err := validateComponent(component); err != nil {
			return err
		}
		if component.BuilderType != "" {
			hasFromSource = true
		}
	}
	if hasFromSource && m.ProfileDigest == "" {
		return e.New(e.CodeVerifyFailed, "from-source validation vector has no builder-profile digest")
	}
	if !hasFromSource && (m.ProfileDigest != "" || m.BuilderProfile != nil) {
		return e.New(e.CodeVerifyFailed, "all-prebuilt validation vector must be profile-free")
	}
	return nil
}

func validateProfileBinding(m *Manifest) error {
	if m.ProfileDigest == "" {
		if m.BuilderProfile != nil {
			return e.New(e.CodeVerifyFailed, "builderProfile is present without profileDigest")
		}
		return nil
	}
	if err := requireExactDigest("profileDigest", m.ProfileDigest, true); err != nil {
		return err
	}
	if m.BuilderProfile == nil {
		return e.New(e.CodeVerifyFailed, "profileDigest is present without builderProfile")
	}
	if m.BuilderProfile.Digest != m.ProfileDigest {
		return e.New(e.CodeVerifyFailed, "builderProfile digest %q does not equal profileDigest %q", m.BuilderProfile.Digest, m.ProfileDigest)
	}
	if m.BuilderProfile.ID != "" && !canonicalIdentity.MatchString(m.BuilderProfile.ID) {
		return e.New(e.CodeVerifyFailed, "builderProfile id %q is not canonical", m.BuilderProfile.ID)
	}
	if strings.TrimSpace(m.BuilderProfile.HostControl) != m.BuilderProfile.HostControl {
		return e.New(e.CodeVerifyFailed, "builderProfile hostControl %q is not canonical", m.BuilderProfile.HostControl)
	}
	return nil
}

func validateEvidence(m *Manifest) error {
	if err := requireExactDigest("evidenceDigest", m.EvidenceDigest, false); err != nil {
		return err
	}
	if m.Evidence == nil {
		return nil
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"evidence.sbomDigest", m.Evidence.SBOMDigest},
		{"evidence.provenanceDigest", m.Evidence.ProvenanceDigest},
		{"evidence.vulnReportDigest", m.Evidence.VulnReportDigest},
	} {
		if err := requireExactDigest(field.name, field.value, false); err != nil {
			return err
		}
	}
	return nil
}

func validateAICR(ref *AICRRef) error {
	if ref == nil {
		return nil
	}
	if !canonicalIdentity.MatchString(ref.Identity) {
		return e.New(e.CodeVerifyFailed, "aicr identity %q is not canonical", ref.Identity)
	}
	if err := requireExactDigest("aicr.digest", ref.Digest, true); err != nil {
		return err
	}
	if err := requireExactDigest("aicr.generatorDigest", ref.GeneratorDigest, true); err != nil {
		return err
	}
	if err := requireExactDigest("aicr.bundleDigest", ref.BundleDigest, false); err != nil {
		return err
	}
	if ref.Bundle != "" {
		if strings.TrimSpace(ref.Bundle) != ref.Bundle {
			return e.New(e.CodeVerifyFailed, "aicr bundle display location %q is not canonical", ref.Bundle)
		}
		if ref.BundleDigest == "" {
			return e.New(e.CodeVerifyFailed,
				"aicr bundle display location %q has no authoritative bundleDigest receipt", ref.Bundle)
		}
	}
	return nil
}

func validateComponent(component ComponentDigest) error {
	if strings.TrimSpace(component.OutputType) == "" || strings.TrimSpace(component.OutputType) != component.OutputType {
		return e.New(e.CodeVerifyFailed, "component %q has a noncanonical or empty outputType", component.Name)
	}
	if err := requireExactDigest("component "+component.Name+" sha256", component.SHA256, true); err != nil {
		return err
	}
	switch component.BuilderType {
	case "":
		if component.EngineClass != "prebuilt-acquire" {
			return e.New(e.CodeVerifyFailed,
				"component %q erases or ambiguously represents its builder identity: empty builderType requires engineClass prebuilt-acquire", component.Name)
		}
		if component.InputDigest != "" || component.Toolchain != "" {
			return e.New(e.CodeVerifyFailed, "prebuilt component %q cannot carry from-source input/toolchain identity", component.Name)
		}
	case "oci-hermetic", "nix":
		if component.EngineClass != component.BuilderType {
			return e.New(e.CodeVerifyFailed,
				"component %q observed engineClass %q does not equal declared builderType %q", component.Name, component.EngineClass, component.BuilderType)
		}
		if err := requireExactDigest("component "+component.Name+" inputDigest", component.InputDigest, true); err != nil {
			return err
		}
		if !isPinnedToolchain(component.Toolchain) {
			return e.New(e.CodeVerifyFailed, "component %q toolchain %q is not pinned by one exact sha256 manifest digest", component.Name, component.Toolchain)
		}
	default:
		return e.New(e.CodeVerifyFailed, "component %q declares unknown builderType %q", component.Name, component.BuilderType)
	}
	return nil
}

func requireExactDigest(field, value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if !canonical.IsDigest(value) {
		return e.New(e.CodeVerifyFailed, "%s %q is not an exact lowercase sha256 digest", field, value)
	}
	return nil
}

func isPinnedToolchain(value string) bool {
	return canonical.IsDigest(value)
}

// toolchainIdentity projects an execution reference into its immutable manifest
// content address. Invalid values are preserved so Manifest.Validate still rejects
// them instead of accidentally turning malformed input into an empty identity.
func toolchainIdentity(value string) string {
	if canonical.IsDigest(value) {
		return value
	}
	if strings.Count(value, "@") == 1 {
		_, digest, ok := strings.Cut(value, "@")
		if ok && canonical.IsDigest(digest) {
			return digest
		}
	}
	return value
}

func (m *Manifest) authorityBinding(profileDigest, executionBinding string) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	type authorityView struct {
		SchemaVersion    string            `json:"schemaVersion"`
		BuildDigest      string            `json:"buildDigest"`
		ProfileDigest    string            `json:"profileDigest,omitempty"`
		ExecutionBinding string            `json:"executionBinding,omitempty"`
		Components       []ComponentDigest `json:"components"`
	}
	return canonical.Digest(canonical.KindQualifiedObservation, authorityView{
		SchemaVersion: m.SchemaVersion, BuildDigest: m.BuildDigest,
		ProfileDigest: profileDigest, ExecutionBinding: executionBinding,
		Components: m.Components,
	})
}

func (m *Manifest) requireQualified(ctx context.Context) error {
	if m == nil || m.qualification == nil || m.qualification.report == nil {
		return e.New(e.CodePreflightFailed,
			"built validation-vector observation is unqualified; use FromQualifiedReport")
	}
	q := m.qualification
	if err := q.report.RequireQualified(ctx); err != nil {
		return err
	}
	if q.report.QualificationProfileDigest() != q.profileDigest || q.report.ExecutionBinding() != q.executionBinding {
		return e.New(e.CodeVerifyFailed, "qualified report execution identity changed after vector construction")
	}
	binding, err := m.authorityBinding(q.profileDigest, q.executionBinding)
	if err != nil {
		return err
	}
	if binding != q.manifestBinding {
		return e.New(e.CodeVerifyFailed, "qualified validation-vector observation changed after construction")
	}
	return nil
}

func (m *Manifest) requirePublisherQualified(ctx context.Context) error {
	if err := m.requireQualified(ctx); err != nil {
		return err
	}
	qualification := m.qualification
	if qualification.publisherEvidence == nil || qualification.publicationBinding == "" {
		return e.New(e.CodeProvenanceMissing,
			"publishable validation vector requires opaque publisher evidence")
	}
	if err := qualification.publisherEvidence.bindManifest(m); err != nil {
		return err
	}
	reference, digest, err := qualification.publisherEvidence.EvidenceRef()
	if err != nil {
		return err
	}
	if m.Evidence == nil || *m.Evidence != *reference || m.EvidenceDigest != digest {
		return e.New(e.CodeUnverifiedRef,
			"validation vector evidence fields no longer match publisher qualification")
	}
	binding, err := m.publicationAuthorityBinding()
	if err != nil {
		return err
	}
	if binding != qualification.publicationBinding {
		return e.New(e.CodeVerifyFailed,
			"publishable validation vector changed after evidence qualification")
	}
	return nil
}

func (m *Manifest) publicationAuthorityBinding() (string, error) {
	if err := m.ValidatePublished(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindQualifiedPublication, m)
}

// Marshal renders the ValidationVector as its CANONICAL bytes (ADR-01 §4): the stored
// on-disk form equals the bytes Identity() hashes, so an auditor recomputes the digest
// straight from the file with no reserialization ambiguity.
func (m *Manifest) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(m)
}

// MarshalQualified returns canonical publication bytes only while the vector's
// report-derived core still matches its live opaque pipeline qualification. Callers
// may attach publisher-qualified evidence or receipt-bound AICR before this
// finalization; build/profile/component mutations still fail.
// Conformance remains a separately authenticated release-gate receipt and cannot be
// embedded as caller-authored metadata in these bytes.
// Use these bytes for any signed or publisher-qualified output derived from a build.
func (m *Manifest) MarshalQualified(ctx context.Context) ([]byte, error) {
	if err := m.requirePublisherQualified(ctx); err != nil {
		return nil, err
	}
	if err := m.ValidatePublished(); err != nil {
		return nil, err
	}
	return m.Marshal()
}

// MarshalQualifiedDiagnostic preserves the live report authority on an explicitly
// non-publishable observation record. It is intended for bake-off diagnostics only;
// authenticated ValidationVector consumers call Parse and reject these bytes until
// evidence is linked.
func (m *Manifest) MarshalQualifiedDiagnostic(ctx context.Context) ([]byte, error) {
	if err := m.requireQualified(ctx); err != nil {
		return nil, err
	}
	return m.Marshal()
}

// Parse loads a manifest from bytes.
func Parse(b []byte) (*Manifest, error) {
	m, err := parseDiagnostic(b)
	if err != nil {
		return nil, err
	}
	if err := m.ValidatePublished(); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "validating publishable expected-digests manifest")
	}
	return m, nil
}

// ParseDiagnostic parses a canonical observation record without granting it the
// published ValidationVector contract. It is intentionally separate so tooling
// cannot mistake an evidence-free bake-off record for a signable expectation.
func ParseDiagnostic(b []byte) (*Manifest, error) {
	return parseDiagnostic(b)
}

func parseDiagnostic(b []byte) (*Manifest, error) {
	var m Manifest
	if err := canonical.UnmarshalExact(b, &m); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "parsing expected-digests manifest")
	}
	if err := m.Validate(); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "validating expected-digests manifest")
	}
	return &m, nil
}

// VerifyAgainst is a pure comparison of a freshly-built manifest (got) against the
// expected one. It performs no signature or pipeline-receipt authentication and must
// not by itself authorize a Reproduced verdict; authority consumers
// use VerifyQualifiedAgainst. It nevertheless validates both documents and rejects
// all identity drift before comparing artifact bytes.
//
// A subset observation verifies only the components it contains. Expected components
// not present in got receive no verdict and cannot be treated as built by this result.
// Iterating got preserves `verify --only <c>` without authorizing an unbuilt member.
func (want *Manifest) VerifyAgainst(got *Manifest) error {
	wantByName, built, err := comparisonInputs(want, got)
	if err != nil {
		return err
	}
	for _, g := range built {
		w, ok := wantByName[g.Name]
		if !ok {
			return e.New(e.CodeVerifyFailed, "built component %q is not in the expected manifest", g.Name)
		}
		if err := compareComponentIdentity(w, g); err != nil {
			return err
		}
		if w.SHA256 != g.SHA256 {
			return e.New(e.CodeVerifyFailed,
				"component %q diverged: expected %s, built %s", g.Name, w.SHA256, g.SHA256)
		}
	}
	return nil
}

// VerifyQualifiedAgainst authenticates got's still-valid pipeline qualification and
// then performs the pure comparison. The signed/authenticated expected vector remains
// the caller's responsibility; this method establishes that the observed artifacts
// came through the terminal execution/acquisition authority seam.
func (want *Manifest) VerifyQualifiedAgainst(ctx context.Context, got *Manifest) error {
	if got == nil {
		return e.New(e.CodePreflightFailed, "built validation-vector observation is nil")
	}
	if err := got.requireQualified(ctx); err != nil {
		return err
	}
	return want.VerifyAgainst(got)
}

// Divergence is one component whose built digest differs from the expected digest.
type Divergence struct {
	Component  string
	OutputType string
	Expected   string
	Built      string
}

// Check collects every divergence between the expected manifest and a freshly-built
// one without failing. Build and profile mismatches are fatal. Component
// digest mismatches are collected into Divergences; built components absent from the
// expected manifest are collected into Missing.
func (want *Manifest) Check(got *Manifest) (divs []Divergence, missing []string, err error) {
	wantByName, built, err := comparisonInputs(want, got)
	if err != nil {
		return nil, nil, err
	}
	for _, g := range built {
		w, ok := wantByName[g.Name]
		if !ok {
			missing = append(missing, g.Name)
			continue
		}
		if err := compareComponentIdentity(w, g); err != nil {
			return nil, nil, err
		}
		if w.SHA256 != g.SHA256 {
			divs = append(divs, Divergence{
				Component: g.Name, OutputType: g.OutputType, Expected: w.SHA256, Built: g.SHA256,
			})
		}
	}
	return divs, missing, nil
}

func comparisonInputs(want, got *Manifest) (map[string]ComponentDigest, []ComponentDigest, error) {
	if err := want.Validate(); err != nil {
		return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "expected validation vector is invalid")
	}
	if err := got.Validate(); err != nil {
		return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "built validation-vector observation is invalid")
	}
	if want.BuildDigest != got.BuildDigest {
		return nil, nil, e.New(e.CodeVerifyFailed,
			"buildDigest mismatch: expected %s, built %s", want.BuildDigest, got.BuildDigest)
	}
	if want.ProfileDigest != got.ProfileDigest {
		return nil, nil, e.New(e.CodeVerifyFailed,
			"builder-profile mismatch: expected %s, built %s (byte reproduction is profile-bounded; a different profile cannot establish reproduction)", orNone(want.ProfileDigest), orNone(got.ProfileDigest))
	}
	wantByName := make(map[string]ComponentDigest, len(want.Components))
	for _, component := range want.Components {
		wantByName[component.Name] = component
	}
	return wantByName, got.Components, nil
}

func compareComponentIdentity(want, got ComponentDigest) error {
	for _, field := range []struct {
		name     string
		expected string
		built    string
	}{
		{"outputType", want.OutputType, got.OutputType},
		{"toolchain", want.Toolchain, got.Toolchain},
		{"builderType", want.BuilderType, got.BuilderType},
		{"engineClass", want.EngineClass, got.EngineClass},
		{"inputDigest", want.InputDigest, got.InputDigest},
	} {
		if field.expected != field.built {
			return e.New(e.CodeVerifyFailed,
				"component %q %s drifted: expected %q, built %q", got.Name, field.name, field.expected, field.built)
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

// Summary is a one-line human description.
func (m *Manifest) Summary() string {
	prof := ""
	if m.ProfileDigest != "" {
		prof = fmt.Sprintf(", profile %s", m.ProfileDigest)
	}
	return fmt.Sprintf("%s (%d component digests%s)", m.BuildDigest, len(m.Components), prof)
}
