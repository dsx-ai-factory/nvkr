package canonical

import (
	"errors"
	"math"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// TestMarshalGolden pins the canonical byte form: sorted keys, no whitespace,
// literal <>& (not \u-escaped as encoding/json would), integers verbatim.
func TestMarshalGolden(t *testing.T) {
	in := map[string]any{
		"schemaVersion": "v1",
		"b":             1748736000,
		"a":             "x < y & z > w",
		"nested":        map[string]any{"z": true, "a": []any{3, 2, 1}},
	}
	want := `{"a":"x < y & z > w","b":1748736000,"nested":{"a":[3,2,1],"z":true},"schemaVersion":"v1"}`
	got, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("Marshal mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestMarshalStableAcrossKeyOrder proves the output is independent of Go map
// iteration order (a map has none) and struct field order.
func TestMarshalStableAcrossKeyOrder(t *testing.T) {
	type doc struct {
		Z string `json:"z"`
		A string `json:"a"`
	}
	b1, _ := Marshal(doc{Z: "1", A: "2"})
	b2, _ := Marshal(map[string]any{"a": "2", "z": "1"})
	if string(b1) != string(b2) {
		t.Errorf("struct and map disagree: %s vs %s", b1, b2)
	}
	if string(b1) != `{"a":"2","z":"1"}` {
		t.Errorf("keys not sorted: %s", b1)
	}
}

// TestDigestDomainSeparation proves the same bytes under different kinds yield
// different digests, and that Digest is stable.
func TestDigestDomainSeparation(t *testing.T) {
	v := map[string]any{"k": "v"}
	d1, err := Digest(KindBuild, v)
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := Digest(KindBuilderProfile, v)
	if d1 == d2 {
		t.Errorf("domain separation failed: build and profile digests equal (%s)", d1)
	}
	again, _ := Digest(KindBuild, v)
	if d1 != again {
		t.Errorf("Digest not stable: %s vs %s", d1, again)
	}
	if len(d1) != len("sha256:")+64 {
		t.Errorf("unexpected digest form %q", d1)
	}
}

func TestDigestRejectsUnregisteredDomain(t *testing.T) {
	if _, err := Digest("nkd.recipe-typo", map[string]string{"value": "same"}); err == nil {
		t.Fatal("an unregistered digest domain must fail instead of minting a new identity family")
	}
}

func TestRetiredDomainsRemainReservedButCannotMint(t *testing.T) {
	for _, allocation := range []struct{ kind, want string }{
		{KindLegacyRecipe, "nkd.recipe"},
		{KindClusterDefinition, "nkd.cluster-definition"},
		{KindClusterComposition, "nkd.cluster-composition"},
		{KindClusterRealization, "nkd.cluster-realization/v1"},
		{KindStagingManifest, "nkd.staging-manifest"},
	} {
		t.Run(allocation.want, func(t *testing.T) {
			kind := allocation.kind
			if kind != allocation.want || !registeredKind(kind) {
				t.Fatal("retired domain allocation must remain reserved")
			}
			value := map[string]string{"value": "new-object"}
			for _, test := range []struct {
				name string
				mint func() (string, error)
			}{
				{"digest", func() (string, error) { return Digest(kind, value) }},
				{"typed digest", func() (string, error) {
					digest, err := DigestTyped(kind, value)
					if digest.Kind != "" {
						t.Fatal("rejected typed identity retained a kind")
					}
					return digest.Value, err
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					digest, err := test.mint()
					var coded *e.Error
					if digest != "" || !errors.As(err, &coded) || coded.Code != e.CodeUnsupported {
						t.Fatalf("retired identity = %q, %v; want no digest and %s", digest, err, e.CodeUnsupported)
					}
				})
			}
		})
	}
}

func TestAICRBundleHasItsOwnDomain(t *testing.T) {
	value := map[string]any{"schemaVersion": "nkd.dev/aicr-bundle/v1"}
	aicrDigest, err := Digest(KindAICRBundle, value)
	if err != nil {
		t.Fatal(err)
	}
	pipelineDigest, err := Digest(KindBundle, value)
	if err != nil {
		t.Fatal(err)
	}
	if aicrDigest == pipelineDigest {
		t.Fatal("AICR output tree can be substituted for a pipeline bundle")
	}
}

func TestAuthorityFamiliesRejectSameByteSubstitution(t *testing.T) {
	value := map[string]any{"schemaVersion": "same-canonical-bytes", "epoch": 1}
	for _, pair := range []struct {
		name        string
		left, right string
	}{
		{name: "publication identity vs manifest", left: KindPublication, right: KindPublicationManifest},
		{name: "publisher evidence vs reproduction", left: KindPublisherEvidence, right: KindReproductionObservation},
		{name: "trusted time vs checkpoint policy", left: KindTrustedTime, right: KindStatusCheckpointPolicy},
		{name: "provenance index vs statement", left: KindProvenance, right: KindProvenanceStatement},
	} {
		t.Run(pair.name, func(t *testing.T) {
			left, err := Digest(pair.left, value)
			if err != nil {
				t.Fatal(err)
			}
			right, err := Digest(pair.right, value)
			if err != nil {
				t.Fatal(err)
			}
			if left == right {
				t.Fatalf("same canonical bytes crossed authority families %q and %q", pair.left, pair.right)
			}
		})
	}
}

// TestStringEscaping covers the ECMAScript/JCS escape set: only the two
// mandatory escapes and the control-char shorthands are escaped.
func TestStringEscaping(t *testing.T) {
	in := "a\tb\"c\\d\ne"
	got, _ := Marshal(map[string]any{"s": in})
	want := `{"s":"a\tb\"c\\d\ne"}`
	if string(got) != want {
		t.Errorf("escaping mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestKindsUnique enforces ADR-01 §3: every domain-separation tag is distinct and
// shares the nkd. prefix, so no object's digest can be mistaken for another's.
func TestKindsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range registeredKinds {
		if seen[k] {
			t.Errorf("duplicate kind tag %q", k)
		}
		seen[k] = true
		if len(k) < 4 || k[:4] != "nkd." {
			t.Errorf("kind %q is missing the nkd. prefix", k)
		}
	}
}

func TestSourceInputHasIndependentDigestDomain(t *testing.T) {
	value := map[string]string{"commit": "same-bytes"}
	sourceInput, err := Digest(KindSourceInput, value)
	if err != nil {
		t.Fatal(err)
	}
	sourceClosure, err := Digest(KindSourceClosure, value)
	if err != nil {
		t.Fatal(err)
	}
	vendorSpec, err := Digest(KindVendorSpec, value)
	if err != nil {
		t.Fatal(err)
	}
	if sourceInput == sourceClosure || sourceInput == vendorSpec || sourceClosure == vendorSpec {
		t.Fatal("source input, vendor specification, and fetched source closure must use independent digest domains")
	}
}

func TestUnmarshalExactRejectsAlternateAndAmbiguousRepresentations(t *testing.T) {
	type document struct {
		Name string `json:"name"`
	}
	for _, raw := range [][]byte{
		[]byte("{\"name\": \"x\"}"),
		[]byte("{\"name\":\"x\"}\n"),
		[]byte("{\"name\":\"x\",\"name\":\"y\"}"),
		[]byte("{\"name\":\"x\",\"future\":true}"),
		append([]byte("{\"name\":\"x\"}"), 0xff),
	} {
		var got document
		if err := UnmarshalExact(raw, &got); err == nil {
			t.Fatalf("accepted non-exact canonical JSON %q", raw)
		}
	}
	var got document
	if err := UnmarshalExact([]byte(`{"name":"x"}`), &got); err != nil || got.Name != "x" {
		t.Fatalf("exact canonical JSON rejected: %+v %v", got, err)
	}
}

func TestValidateJSONDocumentRejectsAmbiguousExternalJSON(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"subject":"a","subject":"b"}`),
		[]byte(`{"subject":"a"}{"subject":"b"}`),
		append([]byte(`{"subject":"a"}`), 0xff),
	} {
		if err := ValidateJSONDocument(raw); err == nil {
			t.Fatalf("accepted ambiguous JSON document %q", raw)
		}
	}
	if err := ValidateJSONDocument([]byte(" {\n\t\"subject\": \"a\"\n} \n")); err != nil {
		t.Fatalf("valid non-canonical external JSON rejected: %v", err)
	}
}

func TestCanonicalIntegerProfilePreservesUint64AndRejectsFractionalNumbers(t *testing.T) {
	got, err := Marshal(struct {
		Sequence uint64 `json:"sequence"`
	}{Sequence: math.MaxUint64})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"sequence":18446744073709551615}`; string(got) != want {
		t.Fatalf("large integer changed during canonicalization: got %s want %s", got, want)
	}
	if _, err := Marshal(map[string]any{"fraction": 1.5}); err == nil {
		t.Fatal("fractional number entered the integer-only canonical profile")
	}

	var exact struct {
		Sequence uint64 `json:"sequence"`
	}
	if err := UnmarshalExact(got, &exact); err != nil || exact.Sequence != math.MaxUint64 {
		t.Fatalf("exact large integer did not round-trip: %+v %v", exact, err)
	}
}

func TestMarshalRejectsInvalidUTF8BeforeIdentityCollapses(t *testing.T) {
	invalid := string([]byte{0xff})
	replacement := "\ufffd"

	validBytes, err := Marshal(map[string]string{"value": replacement})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Marshal(map[string]string{"value": invalid}); err == nil {
		t.Fatal("invalid UTF-8 value was silently normalized")
	}
	if _, err := Marshal(map[string]string{invalid: "value"}); err == nil {
		t.Fatal("invalid UTF-8 object name was silently normalized")
	}
	if _, err := Digest(KindBuild, map[string]string{"value": invalid}); err == nil {
		t.Fatal("invalid UTF-8 value minted a digest")
	}

	want := `{"value":"�"}`
	if string(validBytes) != want {
		t.Fatalf("valid replacement character changed: got %s want %s", validBytes, want)
	}
}
