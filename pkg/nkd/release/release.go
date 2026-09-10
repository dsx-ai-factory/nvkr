// Package release is the release-lifecycle layer above per-component verification
// (ADR-05 §2, ADR-16): the Inventory (single source of the required set +
// compatibility), the atomic ReleaseSet, the two independent gates (reproduction +
// handed-off conformance), digest-first selection, incremental validation, rollback,
// and the downstream clearance result. NKD PRODUCES these objects and re-selectable
// verified sets; it does not operate the fleet — it never runs the conformance suite
// and performs no infrastructure mutation (ADR-16 §4).
//
// Every object is digest-addressable through the one canonical codec (ADR-01).
package release

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
)

const (
	InventorySchemaVersion   = "nkd.dev/inventory/v2"
	ReleaseSetSchemaVersion  = "nkd.dev/release-set/v2"
	ConformanceSchemaVersion = "nkd.dev/conformance-result/v2"
)

// Reproduction verdicts a member may carry (from verify, ADR-04/07).
const (
	Reproduced         = "reproduced"
	ReproductionFailed = "failed"
)

// Conformance verdicts (handed off, ADR-16 §2).
const (
	ConformancePassed = "passed"
	ConformanceFailed = "failed"
)

// InventoryComponent is one required component in the Inventory: what MUST be present
// and any external evidence refs for a prebuilt/acquired one.
type InventoryComponent struct {
	Name                     string                   `json:"name"`
	Role                     string                   `json:"role,omitempty"` // control-plane | worker | gpu-worker | "" (any)
	VersionConstraint        string                   `json:"versionConstraint,omitempty"`
	RequiredArtifacts        []string                 `json:"requiredArtifacts,omitempty"`
	ArtifactKinds            []ArtifactKindBinding    `json:"artifactKinds,omitempty"`
	ExternalSBOMRef          string                   `json:"externalSbomRef,omitempty"`
	ExternalSBOMDigest       string                   `json:"externalSbomDigest,omitempty"`
	ExternalProvenanceRef    string                   `json:"externalProvenanceRef,omitempty"`
	ExternalProvenanceDigest string                   `json:"externalProvenanceDigest,omitempty"`
	EvidenceGaps             []EvidenceGapDeclaration `json:"evidenceGaps,omitempty"`
}

// Compatibility is the Inventory's compatibility envelope (ADR-05 §2).
type Compatibility struct {
	Kubernetes string `json:"kubernetes,omitempty"`
	NKE        string `json:"nke,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

// Inventory is the single source of the required set + compatibility (ADR-05 §2).
type Inventory struct {
	SchemaVersion string               `json:"schemaVersion"`
	Version       string               `json:"version"`
	Components    []InventoryComponent `json:"components"`
	Compatibility Compatibility        `json:"compatibility,omitempty"`
}

// Digest is the Inventory's canonical identity.
func (in *Inventory) Digest() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindInventory, in.sorted())
}

func (in *Inventory) sorted() *Inventory {
	c := *in
	c.Components = append([]InventoryComponent(nil), in.Components...)
	for i := range c.Components {
		c.Components[i].RequiredArtifacts = append([]string(nil), c.Components[i].RequiredArtifacts...)
		sort.Strings(c.Components[i].RequiredArtifacts)
		c.Components[i].ArtifactKinds = append([]ArtifactKindBinding(nil), c.Components[i].ArtifactKinds...)
		sort.Slice(c.Components[i].ArtifactKinds, func(a, b int) bool {
			if c.Components[i].ArtifactKinds[a].ArtifactDigest != c.Components[i].ArtifactKinds[b].ArtifactDigest {
				return c.Components[i].ArtifactKinds[a].ArtifactDigest < c.Components[i].ArtifactKinds[b].ArtifactDigest
			}
			return c.Components[i].ArtifactKinds[a].Kind < c.Components[i].ArtifactKinds[b].Kind
		})
		c.Components[i].EvidenceGaps = append([]EvidenceGapDeclaration(nil), c.Components[i].EvidenceGaps...)
		for j := range c.Components[i].EvidenceGaps {
			c.Components[i].EvidenceGaps[j].MissingTypes = append([]EvidenceType(nil), c.Components[i].EvidenceGaps[j].MissingTypes...)
			sort.Slice(c.Components[i].EvidenceGaps[j].MissingTypes, func(a, b int) bool {
				return c.Components[i].EvidenceGaps[j].MissingTypes[a] < c.Components[i].EvidenceGaps[j].MissingTypes[b]
			})
		}
		sort.Slice(c.Components[i].EvidenceGaps, func(a, b int) bool {
			if c.Components[i].EvidenceGaps[a].ArtifactDigest != c.Components[i].EvidenceGaps[b].ArtifactDigest {
				return c.Components[i].EvidenceGaps[a].ArtifactDigest < c.Components[i].EvidenceGaps[b].ArtifactDigest
			}
			return evidenceTypesKey(c.Components[i].EvidenceGaps[a].MissingTypes) < evidenceTypesKey(c.Components[i].EvidenceGaps[b].MissingTypes)
		})
	}
	sort.Slice(c.Components, func(i, j int) bool {
		if c.Components[i].Name != c.Components[j].Name {
			return c.Components[i].Name < c.Components[j].Name
		}
		return c.Components[i].Role < c.Components[j].Role
	})
	return &c
}

// Validate checks the inventory document before it can participate in identity or
// authorization. RequiredArtifacts are immutable artifact digests, not mutable
// names, and ArtifactKinds must classify that exact set bijectively.
func (in *Inventory) Validate() error {
	if in == nil {
		return e.New(e.CodeInventoryUnpinned, "inventory is required")
	}
	if in.SchemaVersion != InventorySchemaVersion {
		return e.New(e.CodeInvalidCatalog, "inventory schemaVersion %q is unsupported (want %q)", in.SchemaVersion, InventorySchemaVersion)
	}
	if strings.TrimSpace(in.Version) == "" || strings.TrimSpace(in.Version) != in.Version {
		return e.New(e.CodeInvalidCatalog, "inventory version must be a non-empty canonical value")
	}
	for _, compatibilityField := range []struct{ field, value string }{
		{"compatibility.kubernetes", in.Compatibility.Kubernetes},
		{"compatibility.nke", in.Compatibility.NKE},
		{"compatibility.platform", in.Compatibility.Platform},
	} {
		field, value := compatibilityField.field, compatibilityField.value
		if value != "" && strings.TrimSpace(value) != value {
			return e.New(e.CodeInvalidCatalog, "inventory %s has surrounding whitespace", field)
		}
	}
	if len(in.Components) == 0 {
		return e.New(e.CodeReleaseIncomplete, "inventory declares no required members")
	}
	seen := map[string]bool{}
	for _, component := range in.Components {
		if strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Name) != component.Name ||
			(component.Role != "" && strings.TrimSpace(component.Role) != component.Role) ||
			(component.VersionConstraint != "" && strings.TrimSpace(component.VersionConstraint) != component.VersionConstraint) {
			return e.New(e.CodeInvalidCatalog,
				"inventory member name, role, and version constraint must be canonical")
		}
		k := key(component.Name, component.Role)
		if seen[k] {
			return e.New(e.CodeInvalidCatalog, "inventory contains duplicate required member %s", label(component.Name, component.Role))
		}
		seen[k] = true
		if len(component.RequiredArtifacts) == 0 {
			return e.New(e.CodeReleaseIncomplete,
				"inventory member %s declares no required artifacts", label(component.Name, component.Role))
		}
		artifacts := map[string]bool{}
		for _, digest := range component.RequiredArtifacts {
			if !isDigest(digest) {
				return e.New(e.CodeInvalidCatalog, "inventory member %s has non-digest required artifact %q", label(component.Name, component.Role), digest)
			}
			if artifacts[digest] {
				return e.New(e.CodeInvalidCatalog, "inventory member %s repeats required artifact %s", label(component.Name, component.Role), digest)
			}
			artifacts[digest] = true
		}
		classified := map[string]bool{}
		for _, binding := range component.ArtifactKinds {
			if !isDigest(binding.ArtifactDigest) || !artifacts[binding.ArtifactDigest] {
				return e.New(e.CodeInvalidCatalog,
					"inventory member %s classifies non-required artifact %q",
					label(component.Name, component.Role), binding.ArtifactDigest)
			}
			if !binding.Kind.valid() {
				return e.New(e.CodeInvalidCatalog,
					"inventory member %s assigns an unknown kind to artifact %s",
					label(component.Name, component.Role), binding.ArtifactDigest)
			}
			if classified[binding.ArtifactDigest] {
				return e.New(e.CodeInvalidCatalog,
					"inventory member %s classifies artifact %s more than once",
					label(component.Name, component.Role), binding.ArtifactDigest)
			}
			classified[binding.ArtifactDigest] = true
		}
		for digest := range artifacts {
			if !classified[digest] {
				return e.New(e.CodeInvalidCatalog,
					"inventory member %s does not classify required artifact %s",
					label(component.Name, component.Role), digest)
			}
		}
		for _, external := range []struct {
			field  string
			ref    string
			digest string
		}{
			{field: "externalSbom", ref: component.ExternalSBOMRef, digest: component.ExternalSBOMDigest},
			{field: "externalProvenance", ref: component.ExternalProvenanceRef, digest: component.ExternalProvenanceDigest},
		} {
			if (external.ref == "") != (external.digest == "") {
				return e.New(e.CodeUnverifiedRef,
					"inventory member %s must bind %s reference and digest together",
					label(component.Name, component.Role), external.field)
			}
			if external.ref != "" && (!isCanonicalEvidenceRef(external.ref) || !isDigest(external.digest)) {
				return e.New(e.CodeUnverifiedRef,
					"inventory member %s has noncanonical %s reference or digest",
					label(component.Name, component.Role), external.field)
			}
		}
		gapTypes := map[string]bool{}
		for _, gap := range component.EvidenceGaps {
			if !artifacts[gap.ArtifactDigest] {
				return e.New(e.CodeInvalidCatalog,
					"inventory member %s evidence gap targets non-required artifact %q",
					label(component.Name, component.Role), gap.ArtifactDigest)
			}
			missing, err := normalizeEvidenceTypes(gap.MissingTypes)
			if err != nil {
				return err
			}
			if !gap.Reason.valid() {
				return e.New(e.CodeInvalidCatalog,
					"inventory member %s has unknown evidence gap reason", label(component.Name, component.Role))
			}
			for _, evidenceType := range missing {
				key := gap.ArtifactDigest + "\x00" + evidenceType.String()
				if gapTypes[key] {
					return e.New(e.CodeInvalidCatalog,
						"inventory member %s repeats %s gap for artifact %s",
						label(component.Name, component.Role), evidenceType.String(), gap.ArtifactDigest)
				}
				gapTypes[key] = true
				if (evidenceType == EvidenceSBOM && component.ExternalSBOMRef != "") ||
					(evidenceType == EvidenceProvenance && component.ExternalProvenanceRef != "") {
					return e.New(e.CodeInvalidCatalog,
						"inventory member %s declares both external %s evidence and a missing-evidence gap for artifact %s",
						label(component.Name, component.Role), evidenceType.String(), gap.ArtifactDigest)
				}
			}
		}
	}
	return nil
}

// Member is one verified member of a ReleaseSet, referenced by immutable digest.
type Member struct {
	Component           string `json:"component"`
	Role                string `json:"role,omitempty"`
	Arch                string `json:"arch"`
	Version             string `json:"version,omitempty"`
	RecipeDigest        string `json:"recipeDigest"`
	VectorDigest        string `json:"vectorDigest"`
	ResolvedInputDigest string `json:"resolvedInputDigest"`
	// ProfileDigest binds waiver and reproduction policy to the exact build
	// environment. Empty is reserved for byte-preserving, profile-independent
	// acquisition and is represented explicitly in waiver receipts.
	ProfileDigest   string   `json:"profileDigest,omitempty"`
	ArtifactDigests []string `json:"artifactDigests,omitempty"`
}

// ReleaseSet is the atomic, all-or-nothing promotion unit (ADR-16 §1). The required
// set is DERIVED from the Inventory (InventoryRef binds it), never hand-listed.
type ReleaseSet struct {
	SchemaVersion string   `json:"schemaVersion"`
	Version       string   `json:"version"`
	Members       []Member `json:"members"`
	InventoryRef  string   `json:"inventoryRef"` // inventory digest, bound into this set's digest
}

// Digest is the ReleaseSet's immutable identity; rebinding a different inventory or
// member yields a new identity (ADR-06 §1).
func (rs *ReleaseSet) Digest() (string, error) {
	if err := rs.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindReleaseSet, rs.sorted())
}

// PublicationIdentity is ADR-06's immutable lifecycle target over the normalized
// ReleaseSet document. It is distinct from Digest and must be used instead of
// passing an unsorted caller-owned ReleaseSet directly to status.PublicationIdentity.
func (rs *ReleaseSet) PublicationIdentity() (string, error) {
	if err := rs.Validate(); err != nil {
		return "", err
	}
	return status.PublicationIdentity(rs.sorted())
}

// Marshal returns the normalized canonical bytes that must be authenticated for a
// ReleaseSet to become an authorization capability.
func (rs *ReleaseSet) Marshal() ([]byte, error) {
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(rs.sorted())
}

func (rs *ReleaseSet) sorted() *ReleaseSet {
	c := *rs
	c.Members = append([]Member(nil), rs.Members...)
	for i := range c.Members {
		c.Members[i].ArtifactDigests = append([]string(nil), c.Members[i].ArtifactDigests...)
		sort.Strings(c.Members[i].ArtifactDigests)
	}
	sort.Slice(c.Members, func(i, j int) bool {
		if c.Members[i].Component != c.Members[j].Component {
			return c.Members[i].Component < c.Members[j].Component
		}
		if c.Members[i].Role != c.Members[j].Role {
			return c.Members[i].Role < c.Members[j].Role
		}
		return c.Members[i].Arch < c.Members[j].Arch
	})
	return &c
}

// Validate checks the release-set document. It does not authorize the set; that
// requires verified receipts through Authorize.
func (rs *ReleaseSet) Validate() error {
	if rs == nil {
		return e.New(e.CodeUnverifiedRef, "release set is required")
	}
	if rs.SchemaVersion != ReleaseSetSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "release-set schemaVersion %q is unsupported (want %q)", rs.SchemaVersion, ReleaseSetSchemaVersion)
	}
	if strings.TrimSpace(rs.Version) == "" || strings.TrimSpace(rs.Version) != rs.Version {
		return e.New(e.CodeInvalidCatalog, "release-set version must be a non-empty canonical value")
	}
	if !isDigest(rs.InventoryRef) {
		return e.New(e.CodeInventoryUnpinned, "release-set inventoryRef %q is not an immutable digest", rs.InventoryRef)
	}
	if len(rs.Members) == 0 {
		return e.New(e.CodeReleaseIncomplete, "release set has no members")
	}
	seen := map[string]bool{}
	for _, member := range rs.Members {
		if err := validateMember(member); err != nil {
			return err
		}
		k := memberKey(member)
		if seen[k] {
			return e.New(e.CodeInvalidCatalog, "release set contains duplicate member %s/%s/%s", member.Component, orNone(member.Role), member.Arch)
		}
		seen[k] = true
	}
	return nil
}

// ConformanceResult is the independently-signed, handed-off runtime-conformance gate
// (ADR-16 §2). resultsDigest is opaque to NKD; nkdctl never runs the suite.
type ConformanceResult struct {
	SchemaVersion     string   `json:"schemaVersion"`
	ReleaseSetDigest  string   `json:"releaseSetDigest"`
	InventoryDigest   string   `json:"inventoryDigest"`
	VectorDigests     []string `json:"vectorDigests"`
	Suite             string   `json:"suite"`
	SuitePolicyDigest string   `json:"suitePolicyDigest"`
	Verdict           string   `json:"verdict"` // passed | failed
	ResultsDigest     string   `json:"resultsDigest"`
	EffectiveEpoch    int64    `json:"effectiveEpoch"` // operator-supplied input, never a wall-clock read
	ExpiresEpoch      int64    `json:"expiresEpoch"`
}

// Validate checks a conformance document before signature verification.
func (cr ConformanceResult) Validate() error {
	if cr.SchemaVersion != ConformanceSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "conformance schemaVersion %q is unsupported (want %q)", cr.SchemaVersion, ConformanceSchemaVersion)
	}
	if !isDigest(cr.ReleaseSetDigest) || !isDigest(cr.InventoryDigest) || !isDigest(cr.SuitePolicyDigest) || !isDigest(cr.ResultsDigest) {
		return e.New(e.CodeUnverifiedRef, "conformance result must bind immutable release-set, inventory, suite-policy, and results digests")
	}
	if err := validateExactDigestSet("conformance vector", cr.VectorDigests, false); err != nil {
		return err
	}
	if strings.TrimSpace(cr.Suite) == "" {
		return e.New(e.CodeInvalidCatalog, "conformance result suite is required")
	}
	if cr.Verdict != ConformancePassed && cr.Verdict != ConformanceFailed {
		return e.New(e.CodeInvalidCatalog, "unknown conformance verdict %q", cr.Verdict)
	}
	if cr.EffectiveEpoch <= 0 || cr.ExpiresEpoch <= cr.EffectiveEpoch {
		return e.New(e.CodeGateUnmet, "conformance receipt requires positive effective and later expiry epochs")
	}
	return nil
}

// CompletenessGate fails closed (release-incomplete) unless every Inventory-required
// (component, role) is present as a member, naming EVERY missing one (ADR-16 §1). A
// partial set is not promotable.
func (rs *ReleaseSet) CompletenessGate(in *Inventory) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if err := rs.Validate(); err != nil {
		return err
	}
	invDigest, err := in.Digest()
	if err != nil {
		return err
	}
	if rs.InventoryRef != invDigest {
		return e.New(e.CodeInventoryUnpinned, "release set inventoryRef %s does not match supplied inventory %s", rs.InventoryRef, invDigest)
	}
	var missing []string
	for _, req := range in.Components {
		var matches []Member
		for _, member := range rs.Members {
			if member.Component == req.Name && (req.Role == "" || member.Role == req.Role) {
				matches = append(matches, member)
			}
		}
		if len(matches) == 0 {
			missing = append(missing, label(req.Name, req.Role))
			for _, digest := range req.RequiredArtifacts {
				missing = append(missing, label(req.Name, req.Role)+" artifact "+digest)
			}
			continue
		}
		haveArtifacts := map[string]bool{}
		for _, member := range matches {
			for _, digest := range member.ArtifactDigests {
				haveArtifacts[digest] = true
			}
		}
		for _, digest := range req.RequiredArtifacts {
			if !haveArtifacts[digest] {
				missing = append(missing, label(req.Name, req.Role)+" artifact "+digest)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return e.New(e.CodeReleaseIncomplete,
			"release set is incomplete: missing required member(s) %s", strings.Join(missing, ", "))
	}
	return nil
}

func validateMember(member Member) error {
	if strings.TrimSpace(member.Component) == "" || strings.TrimSpace(member.Component) != member.Component ||
		strings.TrimSpace(member.Arch) == "" || strings.TrimSpace(member.Arch) != member.Arch ||
		(member.Role != "" && strings.TrimSpace(member.Role) != member.Role) ||
		(member.Version != "" && strings.TrimSpace(member.Version) != member.Version) {
		return e.New(e.CodeInvalidCatalog,
			"release member component, role, arch, and version must be canonical")
	}
	if member.Arch != "amd64" && member.Arch != "arm64" {
		return e.New(e.CodeInvalidCatalog,
			"release member %s has unsupported architecture %q", member.Component, member.Arch)
	}
	if !isDigest(member.RecipeDigest) || !isDigest(member.VectorDigest) {
		return e.New(e.CodeUnverifiedRef, "member %s/%s must bind immutable recipe and vector digests", member.Component, member.Arch)
	}
	if !isDigest(member.ResolvedInputDigest) {
		return e.New(e.CodeUnverifiedRef, "member %s/%s must bind an immutable resolved-input digest", member.Component, member.Arch)
	}
	if member.ProfileDigest != "" && !isDigest(member.ProfileDigest) {
		return e.New(e.CodeUnverifiedRef, "member %s/%s has malformed builder-profile digest %q", member.Component, member.Arch, member.ProfileDigest)
	}
	if len(member.ArtifactDigests) == 0 {
		return e.New(e.CodeReleaseIncomplete,
			"member %s/%s declares no artifact digests", member.Component, member.Arch)
	}
	seen := map[string]bool{}
	for _, digest := range member.ArtifactDigests {
		if !isDigest(digest) {
			return e.New(e.CodeUnverifiedRef, "member %s/%s has malformed artifact digest %q", member.Component, member.Arch, digest)
		}
		if seen[digest] {
			return e.New(e.CodeInvalidCatalog, "member %s/%s repeats artifact digest %s", member.Component, member.Arch, digest)
		}
		seen[digest] = true
	}
	return nil
}

func memberKey(member Member) string {
	return member.Component + "\x00" + member.Role + "\x00" + member.Arch
}

func isDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	raw := strings.TrimPrefix(value, "sha256:")
	if strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func isCanonicalEvidenceRef(value string) bool {
	return value != "" && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func key(name, role string) string { return name + "\x00" + role }
func label(name, role string) string {
	if role == "" {
		return name
	}
	return name + "@" + role
}
func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}
