// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// Fixture envelopes exercise transport and subject/predicate contracts, not crypto.
func fixtureSBOMAttestation(t *testing.T) func(context.Context, string, []byte) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, digest string, document []byte) ([]byte, error) {
		statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": sbom.CycloneDXPredicateType,
			"subject": []any{map[string]any{"name": "artifact", "digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(document)})
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{}, "dsseEnvelope": map[string]any{
			"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
	}
}

func attestFixtureLayout(t *testing.T, source string) (string, delivery.Result) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "attested")
	result, err := delivery.AttestPreparedEvidence(t.Context(), source, out, fixtureSBOMAttestation(t))
	if err != nil {
		t.Fatal(err)
	}
	return out, result
}

func TestAttestationConversionRejectsWrongSubjectOrDocument(t *testing.T) {
	source, _ := testPreparedPublicationLayout(t)
	for _, tc := range []struct {
		name                              string
		wrongSubject, wrongDocument, fail bool
	}{
		{name: "wrong subject", wrongSubject: true}, {name: "wrong document", wrongDocument: true}, {name: "signing failure", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "rejected")
			_, err := delivery.AttestPreparedEvidence(t.Context(), source, out, func(ctx context.Context, digest string, document []byte) ([]byte, error) {
				if tc.fail {
					return nil, e.New(e.CodeSignatureInvalid, "fixture refusal")
				}
				if tc.wrongSubject {
					digest = "sha256:" + strings.Repeat("a", 64)
				}
				if tc.wrongDocument {
					var bom sbom.BOM
					if err := json.Unmarshal(document, &bom); err != nil {
						return nil, err
					}
					bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "zzfixture", Value: "substituted"})
					var err error
					document, err = bom.Marshal()
					if err != nil {
						return nil, err
					}
				}
				return fixtureSBOMAttestation(t)(ctx, digest, document)
			})
			code := e.CodeVerifyFailed
			if tc.fail {
				code = e.CodeSignatureInvalid
			}
			assertCode(t, err, code)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("failed conversion exposed layout")
			}
		})
	}
}

func TestReleaseConsumerRejectsRawSBOMEvidence(t *testing.T) {
	source, _ := testPreparedPublicationLayout(t)
	inspection, err := delivery.Inspect(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	assertCode(t, delivery.RequireAttestedEvidence(inspection), e.CodeProvenanceMissing)
}
