// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// Fixture envelopes exercise exact subjects and unchanged predicates, not crypto.
func imageIndexFixtureAttestation(digest string, document []byte) ([]byte, error) {
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": sbom.CycloneDXPredicateType,
		"subject": []any{map[string]any{"name": "artifact", "digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(document)})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{}, "dsseEnvelope": map[string]any{
		"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
}

func TestImageIndexAttestationBindsNativeSubjectAndAggregateAttachment(t *testing.T) {
	for _, bad := range []bool{false, true} {
		name := "exact child subjects"
		if bad {
			name = "substituted aggregate subject"
		}
		t.Run(name, func(t *testing.T) {
			source, _, index := registryImageIndexFixture(t)
			destination := filepath.Join(t.TempDir(), "attested")
			seen := map[string]bool{}
			result, err := AttestEvidence(t.Context(), source, destination, func(_ context.Context, subject string, document []byte) ([]byte, error) {
				seen[subject] = true
				if bad {
					subject = index.Digest
				}
				return imageIndexFixtureAttestation(subject, document)
			})
			if bad {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatalf("substituted attestation exposed output: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(seen) != len(index.Children) {
				t.Fatal("attester did not see every distinct native image")
			}
			for _, child := range index.Children {
				if !seen[child.ManifestDigest] {
					t.Fatal("missing native manifest attestation")
				}
			}
			inspection, err := Inspect(t.Context(), destination)
			if err != nil {
				t.Fatal(err)
			}
			if err := RequireAttestedEvidence(inspection); err != nil {
				t.Fatal(err)
			}
			for _, object := range inspection.Root.Objects {
				if object.Evidence == nil {
					continue
				}
				manifest, err := flatManifest(t.Context(), destination, object)
				if err != nil {
					t.Fatal(err)
				}
				if manifest.Subject == nil || manifest.Subject.MediaType != MediaOCIIndex || manifest.Subject.Digest != index.Digest || manifest.Subject.Size != index.Size {
					t.Fatal("SBOM referrer is not attached to its aggregate output")
				}
			}
			held, err := OpenPublication(t.Context(), destination, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
