// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestAttestedDocumentInventoryAndBundleSizeBounds(t *testing.T) {
	testutil.RequireExtended(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		padding  int
		wantCode e.Code
	}{
		{name: "maximum inventory fits encoded bundle", padding: MaxDocumentBytes - 4096},
		{name: "oversized inventory", padding: MaxDocumentBytes, wantCode: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bom, err := NewSubject(digest, "component", digest)
			if err != nil {
				t.Fatal(err)
			}
			bom.Metadata.Component.Properties = []Property{{Name: "fixture", Value: strings.Repeat("x", tc.padding)}}
			// Encode directly so the negative fixture can exercise decoder admission.
			document, err := json.Marshal(bom)
			if err != nil {
				t.Fatal(err)
			}
			statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": CycloneDXPredicateType,
				"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(document)})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"mediaType": BundleMediaType, "verificationMaterial": map[string]any{},
				"dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) <= MaxDocumentBytes {
				t.Fatal("fixture did not exercise base64 expansion")
			}
			got, err := AttestedDocument(raw, digest)
			if tc.wantCode != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.wantCode {
					t.Fatalf("wrong rejection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatal("document changed")
			}
		})
	}
}

func TestAttestedDocumentRejectsOversizedBundleBeforeDecode(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	bom, err := NewSubject(digest, "component", digest)
	if err != nil {
		t.Fatal(err)
	}
	document, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": CycloneDXPredicateType,
		"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(document)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"mediaType": BundleMediaType, "verificationMaterial": map[string]any{},
		"dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		size int
		code e.Code
	}{
		{name: "exact limit", size: MaxBundleBytes},
		{name: "one byte over", size: MaxBundleBytes + 1, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Whitespace is valid JSON padding; without the size gate both
			// fixtures would decode to the same otherwise valid document.
			padded := append(bytes.Clone(raw), bytes.Repeat([]byte{' '}, tc.size-len(raw))...)
			got, err := AttestedDocument(padded, digest)
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code || got != nil {
					t.Fatalf("wrong rejection: %v", err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, document) {
				t.Fatalf("exact-limit document changed: %v", err)
			}
		})
	}
}

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

func TestAttestedDocumentBundleByteBoundBeforeDecoding(t *testing.T) {
	raw, err := AttestedDocument([]byte(strings.Repeat(" ", MaxBundleBytes+1)), "sha256:"+strings.Repeat("a", 64))
	assertDependencyCode(t, err, e.CodeVerifyFailed)
	if raw != nil || !strings.Contains(err.Error(), "invalid SBOM attestation input") {
		t.Fatalf("oversized bundle reached JSON decoding or returned a document: bytes=%d error=%v", len(raw), err)
	}
}

func TestAttestedDocumentInnerInventoryBound(t *testing.T) {
	testutil.RequireExtended(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	// A valid statement and bundle remain below the transport bound. The
	// independent inner limit must reject before decoding the BOM fields.
	predicate := `{"padding":"` + strings.Repeat("x", MaxDocumentBytes-len(`{"padding":""}`)+1) + `"}`
	if len(predicate) != MaxDocumentBytes+1 || !json.Valid([]byte(predicate)) {
		t.Fatal("fixture does not exceed exactly the inner document bound")
	}
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": CycloneDXPredicateType,
		"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(predicate)})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.Marshal(map[string]any{"mediaType": BundleMediaType, "verificationMaterial": map[string]any{},
		"dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
	if err != nil || len(bundle) >= MaxBundleBytes {
		t.Fatalf("fixture reached the outer bound: %d %v", len(bundle), err)
	}
	raw, err := AttestedDocument(bundle, digest)
	assertDependencyCode(t, err, e.CodeVerifyFailed)
	if raw != nil || !strings.Contains(err.Error(), "attested SBOM exceeds the inventory document limit") {
		t.Fatalf("inner byte admission was bypassed: %v", err)
	}
}
