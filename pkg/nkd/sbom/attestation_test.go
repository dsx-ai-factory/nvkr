package sbom

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestAttestedDocumentRejectsAmbiguousOrUnboundStatements(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	bom, err := NewSubject(digest, "component", digest)
	if err != nil {
		t.Fatal(err)
	}
	document, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any, map[string]any)
	}{
		{name: "exact"},
		{name: "missing signature", mutate: func(_ map[string]any, b map[string]any) { b["dsseEnvelope"].(map[string]any)["signatures"] = []any{} }},
		{name: "multiple subjects", mutate: func(s, b map[string]any) { s["subject"] = append(s["subject"].([]any), s["subject"].([]any)[0]) }},
		{name: "wrong predicate", mutate: func(s, b map[string]any) { s["predicateType"] = "https://slsa.dev/provenance/v1" }},
		{name: "wrong digest", mutate: func(s, b map[string]any) {
			s["subject"].([]any)[0].(map[string]any)["digest"] = map[string]string{"sha256": strings.Repeat("b", 64)}
		}},
		{name: "unsigned document", mutate: func(s, b map[string]any) { b["mediaType"] = "application/vnd.cyclonedx+json" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statement := map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": CycloneDXPredicateType, "subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(document)}
			envelope := map[string]any{"payloadType": "application/vnd.in-toto+json", "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}
			bundle := map[string]any{"mediaType": BundleMediaType, "dsseEnvelope": envelope, "verificationMaterial": map[string]any{}}
			if tc.mutate != nil {
				tc.mutate(statement, bundle)
			}
			payload, err := json.Marshal(statement)
			if err != nil {
				t.Fatal(err)
			}
			envelope["payload"] = base64.StdEncoding.EncodeToString(payload)
			raw, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			got, err := AttestedDocument(raw, digest)
			if tc.mutate == nil {
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(document) {
					t.Fatal("document changed")
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("wrong rejection: %v", err)
			}
		})
	}
}
