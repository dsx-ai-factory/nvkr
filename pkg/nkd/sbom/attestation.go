// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const BundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"
const CycloneDXPredicateType = "https://cyclonedx.org/bom"

// MaxDocumentBytes bounds complete inventory documents. Oversize evidence is
// refused rather than truncated.
const MaxDocumentBytes = 32 << 20

// MaxBundleBytes bounds signed transport independently: DSSE expands the
// inventory by 4/3, with remaining space for bounded verification metadata.
const MaxBundleBytes = 64 << 20

// AttestedDocument decodes exact SBOM semantics from a Sigstore bundle. It does
// not authenticate signatures; the signing adapter or enclosing release must do so.
func AttestedDocument(raw []byte, subject string) ([]byte, error) {
	if len(raw) > MaxBundleBytes || !canonical.IsDigest(subject) {
		return nil, e.New(e.CodeVerifyFailed, "invalid SBOM attestation input")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding SBOM bundle")
	}
	var bundle struct {
		MediaType            string          `json:"mediaType"`
		VerificationMaterial json.RawMessage `json:"verificationMaterial"`
		DSSE                 struct {
			PayloadType string `json:"payloadType"`
			Payload     string `json:"payload"`
			Signatures  []struct {
				Sig string `json:"sig"`
			} `json:"signatures"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding SBOM bundle")
	}
	if bundle.MediaType != BundleMediaType || bundle.DSSE.PayloadType != "application/vnd.in-toto+json" || len(bundle.DSSE.Signatures) != 1 || bundle.DSSE.Signatures[0].Sig == "" || len(bundle.VerificationMaterial) == 0 {
		return nil, e.New(e.CodeVerifyFailed, "SBOM requires a signed Sigstore v0.3 DSSE bundle")
	}
	payload, err := base64.StdEncoding.DecodeString(bundle.DSSE.Payload)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding SBOM statement")
	}
	if err := canonical.ValidateJSONDocument(payload); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding SBOM statement")
	}
	var statement struct {
		Type          string `json:"_type"`
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		Predicate json.RawMessage `json:"predicate"`
	}
	if err := json.Unmarshal(payload, &statement); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding SBOM statement")
	}
	if (statement.Type != "https://in-toto.io/Statement/v1" && statement.Type != "https://in-toto.io/Statement/v0.1") || statement.PredicateType != CycloneDXPredicateType || len(statement.Subject) != 1 || len(statement.Subject[0].Digest) != 1 || statement.Subject[0].Digest["sha256"] != strings.TrimPrefix(subject, "sha256:") {
		return nil, e.New(e.CodeVerifyFailed, "SBOM attestation is not bound to its exact subject")
	}
	if len(statement.Predicate) > MaxDocumentBytes {
		return nil, e.New(e.CodeVerifyFailed, "attested SBOM exceeds the inventory document limit")
	}
	var bom BOM
	decoder := json.NewDecoder(bytes.NewReader(statement.Predicate))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bom); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding attested CycloneDX document")
	}
	return bom.Marshal()
}
