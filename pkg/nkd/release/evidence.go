package release

import (
	"encoding/json"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// EvidenceType is a closed set. Its JSON form is a human-readable string, but Go
// callers cannot accidentally turn an arbitrary string into a passing requirement.
type EvidenceType uint8

const (
	EvidenceSBOM EvidenceType = iota + 1
	EvidenceProvenance
	EvidenceVulnerability
)

func (evidenceType EvidenceType) String() string {
	switch evidenceType {
	case EvidenceSBOM:
		return "sbom"
	case EvidenceProvenance:
		return "provenance"
	case EvidenceVulnerability:
		return "vulnerability"
	default:
		return ""
	}
}

func (evidenceType EvidenceType) valid() bool { return evidenceType.String() != "" }

func (evidenceType EvidenceType) MarshalJSON() ([]byte, error) {
	if !evidenceType.valid() {
		return nil, e.New(e.CodeInvalidCatalog, "unknown evidence type %d", evidenceType)
	}
	return json.Marshal(evidenceType.String())
}

func (evidenceType *EvidenceType) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "decoding evidence type")
	}
	switch value {
	case "sbom":
		*evidenceType = EvidenceSBOM
	case "provenance":
		*evidenceType = EvidenceProvenance
	case "vulnerability":
		*evidenceType = EvidenceVulnerability
	default:
		return e.New(e.CodeInvalidCatalog, "unknown evidence type %q", value)
	}
	return nil
}

// EvidenceGapReason records why a signed Inventory requires a waiver. It never
// grants a waiver by itself.
type EvidenceGapReason uint8

const (
	EvidenceNotDeclared EvidenceGapReason = iota + 1
	EvidenceNone
)

func (reason EvidenceGapReason) String() string {
	switch reason {
	case EvidenceNotDeclared:
		return "not-declared"
	case EvidenceNone:
		return "evidence:none"
	default:
		return ""
	}
}

func (reason EvidenceGapReason) valid() bool { return reason.String() != "" }

func (reason EvidenceGapReason) MarshalJSON() ([]byte, error) {
	if !reason.valid() {
		return nil, e.New(e.CodeInvalidCatalog, "unknown evidence gap reason %d", reason)
	}
	return json.Marshal(reason.String())
}

func (reason *EvidenceGapReason) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "decoding evidence gap reason")
	}
	switch value {
	case "not-declared":
		*reason = EvidenceNotDeclared
	case "evidence:none":
		*reason = EvidenceNone
	default:
		return e.New(e.CodeInvalidCatalog, "unknown evidence gap reason %q", value)
	}
	return nil
}

// EvidenceGapDeclaration is policy data in the digest-bound Inventory. It names
// an exact artifact and closed set of missing evidence classes. It is not an
// authorization capability. Evidence-waiver authorization is unimplemented.
type EvidenceGapDeclaration struct {
	ArtifactDigest string            `json:"artifactDigest"`
	MissingTypes   []EvidenceType    `json:"missingTypes"`
	Reason         EvidenceGapReason `json:"reason"`
}

func normalizeEvidenceTypes(types []EvidenceType) ([]EvidenceType, error) {
	if len(types) == 0 {
		return nil, e.New(e.CodeInvalidCatalog, "evidence gap must name at least one missing type")
	}
	seen := make(map[EvidenceType]bool, len(types))
	copy := append([]EvidenceType(nil), types...)
	for _, evidenceType := range copy {
		if !evidenceType.valid() {
			return nil, e.New(e.CodeInvalidCatalog, "evidence gap contains unknown type %d", evidenceType)
		}
		if seen[evidenceType] {
			return nil, e.New(e.CodeInvalidCatalog, "evidence gap repeats type %s", evidenceType.String())
		}
		seen[evidenceType] = true
	}
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	return copy, nil
}

func evidenceTypesKey(types []EvidenceType) string {
	parts := make([]string, len(types))
	for index, evidenceType := range types {
		parts[index] = evidenceType.String()
	}
	return strings.Join(parts, ",")
}
