package delivery

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestContentDigestPreservesFrozenCanonicalIdentity(t *testing.T) {
	// Independently calculated SHA-256 over the frozen nkd.release-content/v1
	// prefix, NUL separator and canonical {artifacts:[...]} bytes. These exact
	// fields match the pre-consolidation producer and consumer representations.
	outputs := []Output{
		{Name: "tools", Kind: "executable", Digest: "sha256:" + strings.Repeat("c", 64), BuildDigest: "sha256:" + strings.Repeat("d", 64), Components: []string{"cli"}},
		{Name: "chart", Kind: "helm-oci", Digest: "sha256:" + strings.Repeat("a", 64), BuildDigest: "sha256:" + strings.Repeat("b", 64)},
	}
	for _, withReferences := range []bool{false, true} {
		if withReferences {
			outputs[0].Reference = "registry.example.test/tools@" + outputs[0].Digest
			outputs[1].Reference = "registry.example.test/chart@" + outputs[1].Digest
		}
		got, err := ContentDigest(outputs)
		if err != nil {
			t.Fatal(err)
		}
		if got != "sha256:d191a5fafcf68301c8e88580473ee9daf3b0f0f91f7d93c94f138af5b02db539" {
			t.Fatalf("content canonical bytes changed: %s", got)
		}
		if outputs[0].Name != "tools" || outputs[0].Components[0] != "cli" || (withReferences && outputs[0].Reference == "") {
			t.Fatal("content digest mutated the caller's projection")
		}
	}
}

func TestContentDigestRejectsAmbiguousOutputs(t *testing.T) {
	valid := Output{Name: "tools", Kind: "executable", Digest: "sha256:" + strings.Repeat("a", 64), BuildDigest: "sha256:" + strings.Repeat("b", 64), Components: []string{"cli"}}
	for _, tc := range []struct {
		name      string
		change    func(*Output)
		duplicate bool
		code      e.Code
	}{
		{name: "duplicate output", duplicate: true, code: e.CodeVerifyFailed},
		{name: "missing build", change: func(o *Output) { o.BuildDigest = "" }, code: e.CodeVerifyFailed},
		{name: "unsafe component", change: func(o *Output) { o.Components = []string{"../cli"} }, code: e.CodeVerifyFailed},
		{name: "duplicate component", change: func(o *Output) { o.Components = []string{"cli", "cli"} }, code: e.CodeVerifyFailed},
		{name: "missing digest", change: func(o *Output) { o.Digest = "" }, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := valid
			if tc.change != nil {
				tc.change(&output)
			}
			outputs := []Output{output}
			if tc.duplicate {
				outputs = append(outputs, output)
			}
			_, err := ContentDigest(outputs)
			assertRegistryCode(t, err, tc.code)
		})
	}
	_, err := ContentDigest(nil)
	assertRegistryCode(t, err, e.CodeReleaseIncomplete)
}
