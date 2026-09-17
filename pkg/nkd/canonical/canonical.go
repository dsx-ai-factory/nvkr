// Package canonical is the single canonical codec for every digest-addressed
// object in nkdctl (ADR-01). It implements NKD's explicitly narrower,
// JCS-derived integer-only profile: UTF-8, object keys sorted lexicographically by
// UTF-16 code unit, no insignificant whitespace, ECMAScript string escaping, and
// arbitrary-precision canonical integer scalars. It is not a claim of accepting
// every RFC-8785 number. The byte form it produces IS the identity — an auditor can
// recompute the same bytes independently in any language, without nkdctl.
//
// ADR-01 is explicit that there is NO grandfathered exception: the recipe
// identity (recipeDigest) uses this codec, exactly like the trust objects. It
// deliberately replaces the earlier deterministic-YAML digest. Human-authored
// inputs (the catalog) stay in their friendly format; only the HASHED forms are
// canonical JSON.
//
// Digests are domain-separated: Digest(kind, obj) = sha256(kind-tag ∥ 0x00 ∥
// canonical-bytes). The kind tag guards against one object's digest being
// accepted as another's, even before the bytes are deserialized (ADR-01 §3).
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Kinds are the domain-separation tags (ADR-01 §3). A new digest-addressed object
// must also be added to registeredKinds below; TestKindsUnique enforces that no two
// objects share a tag, so the anti-collision guarantee is by construction, not by
// luck.
const (
	KindRecipe                  = "nkd.recipe"                   // the compiled IR / recipeDigest (ADR-02)
	KindBuilderProfile          = "nkd.builder-profile"          // the signed builder profile (ADR-07)
	KindValidationVector        = "nkd.validation-vector"        // the expected-digests manifest (ADR-05)
	KindEvidence                = "nkd.evidence"                 // the evidence bundle (ADR-05 §4)
	KindPublisherEvidence       = "nkd.publisher-evidence"       // publisher SBOM/provenance core (ADR-05)
	KindReproductionObservation = "nkd.reproduction-observation" // later operator comparison record (ADR-05)
	KindPromotionPolicy         = "nkd.promotion-policy"         // authenticated release-governance policy (ADR-16)
	KindReproductionResult      = "nkd.reproduction-result"      // exact member reproduction receipt (ADR-16)
	KindCompatibilityResult     = "nkd.compatibility-result"     // exhaustive compatibility receipt (ADR-16)
	KindConformanceResult       = "nkd.conformance-result"       // independent runtime-conformance receipt (ADR-16)
	KindEvidenceWaiver          = "nkd.evidence-waiver"          // authenticated exact evidence-gap waiver (ADR-13/16)
	KindProvenance              = "nkd.provenance"               // the SLSA provenance index (ADR-09)
	KindProvenanceStatement     = "nkd.provenance-statement"     // one artifact-scoped SLSA statement (ADR-09)
	KindSBOM                    = "nkd.sbom"                     // a per-artifact / aggregate CycloneDX SBOM (ADR-13)
	KindSBOMIndex               = "nkd.sbom-index"               // the per-artifact SBOM index (ADR-13 §1)
	KindVulnReport              = "nkd.vuln-report"              // a deterministic vulnerability report (ADR-13 §3)
	KindBundle                  = "nkd.bundle"                   // an emitted CI pipeline's bundleDigest (ADR-21)
	KindInventory               = "nkd.inventory"                // the release inventory (ADR-05 §2)
	KindReleaseSet              = "nkd.release-set"              // the atomic release set (ADR-05/16)
	KindClearance               = "nkd.clearance"                // the downstream clearance result (ADR-16 §4)
	KindStatus                  = "nkd.status"                   // a status declaration (ADR-06 §2)
	KindStatusCheckpoint        = "nkd.status-checkpoint"        // authenticated lifecycle checkpoint (ADR-06 §3)
	KindTrustedTime             = "nkd.status-trusted-time/v1"   // policy-owned release freshness observation (ADR-06)
	KindPublication             = "nkd.publication"              // the immutable publication identity (ADR-06 §1)
	KindPublicationManifest     = "nkd.publication-manifest"     // exact transport/referrer closure (ADR-20)
	KindPublicationRoot         = "nkd.publication-root"         // OCI bootstrap object for one delivery closure
	KindBinaryBundle            = "nkd.binary-bundle"            // exact installable loose-binary projection
	KindAPTRepository           = "nkd.apt-repository"           // exact Debian repository projection
	KindClusterDefinition       = "nkd.cluster-definition"       // the ClusterDefinition (ADR-19)
	KindClusterComposition      = "nkd.cluster-composition"      // pre-release exact recipe composition (ADR-19)
	KindDiskManifest            = "nkd.disk-manifest"            // the unified canonical-disk manifest (ADR-14 §1)
	KindStagingManifest         = "nkd.staging-manifest"         // the node-SKU staging manifest (ADR-14 §4)
	KindSourceClosure           = "nkd.source-closure"           // fetched/vendor-resolved source bytes
	KindVendorSpec              = "nkd.vendor-spec"              // dependency materialization argv + image content identity
	KindSourceInput             = "nkd.source-input"             // commit/tree/vendor content identity used by IR lowering
	KindResolvedInput           = "nkd.resolved-input"           // per-build source closure after ordered patches
	KindAICRBundle              = "nkd.aicr-bundle"              // validated deterministic AICR bundle tree (ADR-12)
	KindCatalogSnapshot         = "nkd.catalog-snapshot/v1"      // one fully resolved catalog state
	KindCatalogOverlay          = "nkd.catalog-overlay/v1"       // one ordered component-level catalog overlay
	KindArtifactGraph           = "nkd.artifact-graph/v1"        // typed build/package/delivery dataflow
	KindExecutionSettings       = "nkd.execution-settings/v1"    // lowered environment execution and publication settings
	KindCompileSpec             = "nkd.compile-spec/v1"          // compiler-owned handoff derived from Definition
	KindRunPlan                 = "nkd.run-plan/v1"              // static lowered execution plan
	KindFactoryObservation      = "nkd.factory-observation/v1"   // unqualified terminal byte observation
	KindDefinition              = "nkd.definition"               // simplified authored product definition (ADR-17 §6)
	KindEnvironment             = "nkd.environment"              // independently selected execution/delivery settings (ADR-20 §6)
	KindVerificationPolicy      = "nkd.verification-policy"      // consumer-selected release acceptance policy (ADR-04 §5)
	KindReleaseContent          = "nkd.release-content/v1"       // internal exact artifact content subject (ADR-17 §6)
	KindClusterRealization      = "nkd.cluster-realization/v1"   // composition-to-content member binding (ADR-19 §4)
	KindLifecycleBinding        = "nkd.lifecycle-binding/v1"     // signed output and cluster publication binding (ADR-20 §7)
	// Internal capability bindings are registered too. They are not wire schemas,
	// but their domain tags are just as security-sensitive as object identities:
	// a typo must fail instead of silently minting a different receipt family.
	KindQualifiedObservation   = "nkd.attest.qualified-observation/v1"
	KindQualifiedPublication   = "nkd.attest.qualified-observation/v1/publication"
	KindQualifiedReport        = "nkd.pipeline.qualified-report/v1"
	KindTerminalVerification   = "nkd.pipeline.terminal-verification/v1"
	KindTerminalTree           = "nkd.pipeline.terminal-tree/v1"
	KindStatusCheckpointPolicy = "nkd.status-checkpoint-policy/v1"
	KindHostObservation        = "nkd.profile.host-observation/v1"
	KindExecutionSelection     = "nkd.profile.execution-selection/v1"
	KindExecutionObservation   = "nkd.profile.execution-observation/v1"
	KindRuntimeIdentity        = "nkd.execution.runtime-identity/v1"
)

// KindBuildMaterialManifest identifies deterministic execution-local component
// inputs. It is separate from ADR-14's final node-SKU staging manifest.
const KindBuildMaterialManifest = "nkd.build-material-manifest/v1"

// registeredKinds is the private authority registry used by Digest.
var registeredKinds = [...]string{
	KindRecipe, KindBuilderProfile, KindValidationVector, KindEvidence, KindPublisherEvidence, KindReproductionObservation, KindPromotionPolicy,
	KindReproductionResult, KindCompatibilityResult, KindConformanceResult, KindEvidenceWaiver, KindProvenance, KindProvenanceStatement,
	KindSBOM, KindSBOMIndex, KindVulnReport, KindBundle, KindInventory,
	KindReleaseSet, KindClearance, KindStatus, KindStatusCheckpoint, KindTrustedTime, KindPublication, KindPublicationManifest, KindPublicationRoot, KindBinaryBundle, KindAPTRepository, KindClusterDefinition, KindClusterComposition,
	KindDiskManifest, KindStagingManifest, KindBuildMaterialManifest,
	KindSourceClosure, KindVendorSpec, KindSourceInput, KindResolvedInput, KindAICRBundle,
	KindCatalogSnapshot, KindCatalogOverlay, KindArtifactGraph, KindExecutionSettings, KindCompileSpec, KindRunPlan, KindFactoryObservation,
	KindDefinition, KindEnvironment, KindVerificationPolicy, KindReleaseContent, KindClusterRealization, KindLifecycleBinding,
	KindQualifiedReport, KindTerminalVerification, KindTerminalTree,
	KindStatusCheckpointPolicy, KindQualifiedObservation, KindQualifiedPublication,
	KindHostObservation, KindExecutionSelection, KindExecutionObservation, KindRuntimeIdentity,
}

// Marshal renders v as NKD's JCS-derived integer-only canonical JSON. It first marshals through
// encoding/json (which validates the value and yields well-formed JSON), then
// re-serializes canonically so the result is independent of Go struct field
// order and map iteration order.
func Marshal(v any) ([]byte, error) {
	// encoding/json silently replaces malformed UTF-8 in Go strings with U+FFFD.
	// That behavior is safe for display JSON, but it would collapse distinct Go
	// values onto the same identity bytes. Reject such values before the standard
	// encoder can normalize them.
	if err := validateUTF8Value(reflect.ValueOf(v), make(map[valueVisit]bool)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "canonical: marshaling value")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve integer tokens; JSON floats would lose precision
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "canonical: decoding intermediate JSON")
	}
	var out bytes.Buffer
	if err := writeValue(&out, tree); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type valueVisit struct {
	typ  reflect.Type
	ptr  uintptr
	kind reflect.Kind
	len  int
}

// validateUTF8Value walks the ordinary Go values that encoding/json would
// inspect. Shared/cyclic containers are visited once; encoding/json remains the
// authority for rejecting unsupported values and actual cycles.
func validateUTF8Value(v reflect.Value, seen map[valueVisit]bool) error {
	if !v.IsValid() {
		return nil
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return e.New(e.CodeInternal, "canonical: value contains an invalid UTF-8 string")
		}
	case reflect.Pointer:
		if v.IsNil() || alreadyVisited(v, seen) {
			return nil
		}
		return validateUTF8Value(v.Elem(), seen)
	case reflect.Map:
		if v.IsNil() || alreadyVisited(v, seen) {
			return nil
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := validateUTF8Value(iter.Key(), seen); err != nil {
				return err
			}
			if err := validateUTF8Value(iter.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if v.IsNil() || alreadyVisited(v, seen) {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := validateUTF8Value(v.Index(i), seen); err != nil {
				return err
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := validateUTF8Value(v.Index(i), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := typ.Field(i)
			// Match encoding/json's visibility boundary while still following an
			// anonymous embedded value whose exported fields may be promoted.
			if field.PkgPath != "" && !field.Anonymous {
				continue
			}
			if !utf8.ValidString(string(field.Tag)) {
				return e.New(e.CodeInternal, "canonical: JSON field metadata contains invalid UTF-8")
			}
			if err := validateUTF8Value(v.Field(i), seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func alreadyVisited(v reflect.Value, seen map[valueVisit]bool) bool {
	visit := valueVisit{typ: v.Type(), ptr: v.Pointer(), kind: v.Kind()}
	if v.Kind() == reflect.Slice {
		visit.len = v.Len()
	}
	if seen[visit] {
		return true
	}
	seen[visit] = true
	return false
}

// UnmarshalExact decodes an identity-bearing JSON document only when the supplied
// bytes are already its one canonical representation. It rejects invalid UTF-8,
// duplicate object names, unknown struct fields, trailing values, and insignificant
// whitespace. Callers therefore cannot accidentally authenticate one byte string
// and execute a different decoded meaning.
func UnmarshalExact(raw []byte, out any) error {
	if err := ValidateJSONDocument(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding canonical JSON")
	}
	if err := requireEOF(dec); err != nil {
		return err
	}
	want, err := Marshal(out)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, want) {
		return e.New(e.CodeVerifyFailed, "JSON bytes are not the canonical representation of the decoded document")
	}
	return nil
}

// ValidateJSONDocument checks the representation-level invariants required at
// trust boundaries even when the document comes from an external JSON profile
// that NKD cannot re-marshal canonically. It rejects malformed UTF-8, duplicate
// names, trailing values, and malformed JSON. It does not assert any schema.
func ValidateJSONDocument(raw []byte) error {
	if !utf8.Valid(raw) {
		return e.New(e.CodeVerifyFailed, "JSON document is not valid UTF-8")
	}
	return rejectDuplicateNames(raw)
}

func rejectDuplicateNames(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkJSON(dec); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "validating canonical JSON member names")
	}
	return requireEOF(dec)
}

func walkJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return e.New(e.CodeVerifyFailed, "JSON object member name is not a string")
			}
			if seen[name] {
				return e.New(e.CodeVerifyFailed, "duplicate JSON object member %q", name)
			}
			seen[name] = true
			if err := walkJSON(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return e.New(e.CodeVerifyFailed, "malformed JSON object")
		}
	case '[':
		for dec.More() {
			if err := walkJSON(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return e.New(e.CodeVerifyFailed, "malformed JSON array")
		}
	default:
		return e.New(e.CodeVerifyFailed, "unexpected JSON delimiter %q", delim)
	}
	return nil
}

func requireEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return e.New(e.CodeVerifyFailed, "canonical JSON contains more than one value")
	}
	return e.Wrap(e.CodeVerifyFailed, err, "checking canonical JSON boundary")
}

// Digest returns the domain-separated content digest of v as "sha256:"+hex:
// sha256(kind ∥ 0x00 ∥ canonical-bytes) (ADR-01 §3).
func Digest(kind string, v any) (string, error) {
	if !registeredKind(kind) {
		return "", e.New(e.CodeInternal,
			"canonical: unregistered digest domain %q", kind)
	}
	b, err := Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0x00})
	h.Write(b)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func registeredKind(kind string) bool {
	for _, registered := range registeredKinds {
		if kind == registered {
			return true
		}
	}
	return false
}

// IsDigest reports whether s is a canonical content digest: the exact
// sha256:<64 lowercase hex> shape every identity string in NKD uses. It is the single
// owner of that predicate — sign, release, cluster, ir, catalog, attest and the rest
// call here instead of each re-compiling `^sha256:[0-9a-f]{64}$`, so the shape cannot
// drift between the seams that must agree on it (ADR-01). It is allocation-free and
// does not authenticate the referent; it only validates the string's form.
func IsDigest(s string) bool {
	const prefix = "sha256:"
	if len(s) != len(prefix)+64 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for i := len(prefix); i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// TypedDigest is a digest paired with the domain-separation Kind it was computed under.
// Digest returns a bare string with the kind tag hashed in and unrecoverable, so nothing
// stops a KindRecipe digest being used where a KindReleaseSet is expected — cross-kind
// confusion is a runtime, not a compile, concern. A TypedDigest carries the kind
// alongside the value so boundary code can demand a specific family (Expect) and
// re-derive-and-compare identity (Verify) instead of trusting a shape-checked string.
type TypedDigest struct {
	Kind  string
	Value string
}

// DigestTyped computes v's domain-separated digest and returns it tagged with kind.
func DigestTyped(kind string, v any) (TypedDigest, error) {
	value, err := Digest(kind, v)
	if err != nil {
		return TypedDigest{}, err
	}
	return TypedDigest{Kind: kind, Value: value}, nil
}

// ParseTypedDigest tags an existing digest string as kind after checking its shape and
// that kind is a registered domain. It authenticates nothing about the referent — only
// that the string is a well-formed digest of a real domain.
func ParseTypedDigest(kind, value string) (TypedDigest, error) {
	if !registeredKind(kind) {
		return TypedDigest{}, e.New(e.CodeInternal, "canonical: unregistered digest domain %q", kind)
	}
	if !IsDigest(value) {
		return TypedDigest{}, e.New(e.CodeUnverifiedRef, "canonical: %q is not a sha256 digest", value)
	}
	return TypedDigest{Kind: kind, Value: value}, nil
}

// Expect returns the digest value only if it was produced under kind, converting a
// silent cross-kind mixup into an explicit error at the seam.
func (d TypedDigest) Expect(kind string) (string, error) {
	if d.Kind != kind || !IsDigest(d.Value) {
		return "", e.New(e.CodeUnverifiedRef,
			"canonical: expected a %q digest, have kind %q value %q", kind, d.Kind, d.Value)
	}
	return d.Value, nil
}

// Verify recomputes v's identity under d.Kind and reports whether it equals d. This is
// the recompute-and-compare that turns a "trusted description" — a digest carried across
// a stage seam — into an authenticated identity: the bytes are re-derived, not believed.
func (d TypedDigest) Verify(v any) error {
	if !IsDigest(d.Value) {
		return e.New(e.CodeUnverifiedRef, "canonical: digest %q is malformed", d.Value)
	}
	recomputed, err := Digest(d.Kind, v)
	if err != nil {
		return err
	}
	if recomputed != d.Value {
		return e.New(e.CodeUnverifiedRef,
			"canonical: %q identity mismatch: recomputed %s != declared %s", d.Kind, recomputed, d.Value)
	}
	return nil
}

// String returns the digest value, so a TypedDigest can be used where a display string
// is expected.
func (d TypedDigest) String() string { return d.Value }

func writeValue(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeString(buf, t)
	case json.Number:
		return writeNumber(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, elem := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeValue(buf, elem); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		return writeObject(buf, t)
	default:
		return e.New(e.CodeInternal, "canonical: unexpected JSON type %T", v)
	}
	return nil
}

func writeObject(buf *bytes.Buffer, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// RFC-8785: sort object member keys by their UTF-16 code-unit sequence.
	sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeString(buf, k)
		buf.WriteByte(':')
		if err := writeValue(buf, m[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// writeNumber emits the repository's deliberately narrow JCS number profile.
// Identity-bearing NKD schemas use integers only, so accepting binary floating
// point would add cross-language rounding and exponent-format ambiguity for no
// domain benefit. Arbitrarily large JSON integers are normalized exactly instead
// of being routed through float64 (which would silently change uint64 values).
func writeNumber(buf *bytes.Buffer, n json.Number) error {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return e.New(e.CodeInternal,
			"canonical: non-integer number %q is outside the NKD canonical-number profile", s)
	}
	i, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return e.New(e.CodeInternal, "canonical: uninterpretable number %q", s)
	}
	buf.WriteString(i.String())
	return nil
}

// writeString emits s with ECMAScript/RFC-8785 escaping: escape only " and \ and
// the control characters below 0x20; every other code point (including <, >, &,
// and all non-ASCII) is emitted as literal UTF-8.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// utf16Less reports whether a sorts before b by UTF-16 code-unit sequence, as
// RFC-8785 requires for object keys. For ASCII keys this coincides with byte
// ordering; the explicit encoding keeps it correct for any key.
func utf16Less(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
