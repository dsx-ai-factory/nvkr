// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

const (
	CosignSignaturePredicateType = "https://sigstore.dev/cosign/sign/v1"
	SignatureBundlePlaceholder   = "${NVKR_SIGNATURE_BUNDLE}"
	ProvenanceBundlePlaceholder  = "${NVKR_PROVENANCE_BUNDLE}"
	maxVerificationBundles       = 32
)

func ociDownloadBundleArgs(ref, predicateType string) []string {
	return []string{"download", "attestation", "--predicate-type", predicateType, ref}
}

func ociVerifyBundleArgs(path, digest, predicateType string, policy VerifyPolicy) []string {
	return []string{"verify-blob-attestation", "--bundle", path, "--type", predicateType,
		"--digest", strings.TrimPrefix(digest, "sha256:"), "--digestAlg", "sha256",
		"--certificate-identity-regexp", policy.IdentityRegexp, "--certificate-oidc-issuer", policy.Issuer}
}

// decodeOCIBundles admits only bounded, complete Sigstore bundle documents.
// Legacy DSSE reports are not a fallback. Registry order cannot select authority.
func decodeOCIBundles(raw []byte) ([][]byte, error) {
	if int64(len(raw)) > maxCosignOutputBytes {
		return nil, e.New(e.CodeSignatureInvalid, "bundle download exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var bundles [][]byte
	for {
		var document json.RawMessage
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			return nil, e.Wrap(e.CodeSignatureInvalid, err, "decoding downloaded bundles")
		}
		if len(bundles) == maxVerificationBundles {
			return nil, e.New(e.CodeSignatureInvalid, "too many verification bundles")
		}
		if err := canonical.ValidateJSONDocument(document); err != nil {
			return nil, e.Wrap(e.CodeSignatureInvalid, err, "invalid verification bundle")
		}
		bundles = append(bundles, document)
	}
	if len(bundles) == 0 {
		return nil, e.New(e.CodeSignatureInvalid, "no required attestation bundles found")
	}
	sort.Slice(bundles, func(i, j int) bool { return bytes.Compare(bundles[i], bundles[j]) < 0 })
	return bundles, nil
}

// ociBundleClaims describes the exact certificate and statement supplied to
// cosign. These claims become verified only after that same bundle passes cosign.
func ociBundleClaims(raw []byte, digest, predicateType string, policy VerifyPolicy) (string, string, error) {
	var bundle struct {
		MediaType string `json:"mediaType"`
		DSSE      struct {
			PayloadType string `json:"payloadType"`
			Payload     string `json:"payload"`
		} `json:"dsseEnvelope"`
		VerificationMaterial struct {
			Certificate struct {
				RawBytes string `json:"rawBytes"`
			} `json:"certificate"`
			PublicKey json.RawMessage `json:"publicKey"`
		} `json:"verificationMaterial"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return "", "", e.Wrap(e.CodeSignatureInvalid, err, "decoding keyless OCI bundle")
	}
	if bundle.MediaType != sbom.BundleMediaType || bundle.DSSE.PayloadType != "application/vnd.in-toto+json" ||
		bundle.VerificationMaterial.Certificate.RawBytes == "" || len(bundle.VerificationMaterial.PublicKey) != 0 {
		return "", "", e.New(e.CodeSignatureInvalid, "OCI verification requires a keyless Sigstore v0.3 DSSE bundle")
	}
	payload, err := base64.StdEncoding.DecodeString(bundle.DSSE.Payload)
	if err != nil {
		return "", "", e.Wrap(e.CodeSignatureInvalid, err, "decoding OCI bundle statement")
	}
	if err := canonical.ValidateJSONDocument(payload); err != nil {
		return "", "", e.Wrap(e.CodeSignatureInvalid, err, "invalid OCI bundle statement")
	}
	var statement inTotoStatement
	if err := json.Unmarshal(payload, &statement); err != nil {
		return "", "", e.Wrap(e.CodeSignatureInvalid, err, "decoding OCI statement")
	}
	if (statement.Type != "https://in-toto.io/Statement/v1" && statement.Type != "https://in-toto.io/Statement/v0.1") ||
		statement.PredicateType != predicateType || len(statement.Subject) != 1 || len(statement.Subject[0].Digest) != 1 ||
		statement.Subject[0].Digest["sha256"] != strings.TrimPrefix(digest, "sha256:") {
		return "", "", e.New(e.CodeVerifyFailed, "OCI bundle does not bind the required predicate and exact subject")
	}

	if len(policy.ExpectedPredicate) > 0 {
		expected, err := canonical.Marshal(json.RawMessage(policy.ExpectedPredicate))
		if err != nil {
			return "", "", e.Wrap(e.CodeVerifyFailed, err, "canonicalize expected predicate")
		}
		actual, err := canonical.Marshal(statement.Predicate)
		if policy.exactPredicateBytes {
			expected = policy.ExpectedPredicate
		}
		if err != nil || !bytes.Equal(expected, actual) {
			return "", "", e.New(e.CodeVerifyFailed, "authenticated statement does not bind the expected predicate")
		}
	}
	der, err := base64.StdEncoding.DecodeString(bundle.VerificationMaterial.Certificate.RawBytes)
	if err != nil {
		return "", "", e.Wrap(e.CodeSignatureInvalid, err, "decoding OCI signing certificate")
	}
	certificate, err := parseCertificate(der)
	if err != nil {
		return "", "", err
	}
	issuer, err := certificateOIDCIssuer(certificate)
	if err != nil {
		return "", "", err
	}
	pattern, err := regexp.Compile(policy.IdentityRegexp)
	if err != nil {
		return "", "", e.Wrap(e.CodeTrustPolicyMissing, err, "invalid certificate identity policy")
	}
	if issuer != policy.Issuer {
		return "", "", e.New(e.CodeUntrustedSigner, "OCI signing certificate has an untrusted issuer")
	}
	var identity string
	for _, candidate := range certificateIdentities(certificate) {
		if pattern.MatchString(candidate) {
			if identity != "" {
				return "", "", e.New(e.CodeUntrustedSigner, "OCI certificate identity is ambiguous")
			}
			identity = candidate
		}
	}
	if identity == "" {
		return "", "", e.New(e.CodeUntrustedSigner, "OCI signing certificate has an untrusted identity")
	}
	return identity, issuer, nil
}

func (s *CosignOCISigner) verifyOCIBundles(ctx context.Context, subject Subject, predicateType string,
	policy VerifyPolicy, workspace string, environment []string) (string, string, [][]string, error) {
	download := ociDownloadBundleArgs(subject.Ref, predicateType)
	raw, err := s.capture(ctx, download, environment)
	if err != nil {
		return "", "", nil, err
	}
	bundles, err := decodeOCIBundles(raw)
	if err != nil {
		return "", "", nil, err
	}
	ran := [][]string{argv(s.binary, download)}
	var lastErr error
	var claimsRejected, verificationFailed int
	for _, bundle := range bundles {
		identity, issuer, err := ociBundleClaims(bundle, subject.Digest, predicateType, policy)
		if err != nil {
			claimsRejected++
			lastErr = err
			continue
		}
		path := filepath.Join(workspace, strings.TrimPrefix(digestBytes(bundle), "sha256:")+".json")
		if err := writePrivateFile(ctx, path, bundle); err != nil {
			return "", "", nil, err
		}
		args := ociVerifyBundleArgs(path, subject.Digest, predicateType, policy)
		ran = append(ran, argv(s.binary, args))
		if _, err := s.capture(ctx, args, environment); err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return "", "", nil, ctxErr
			}
			lastErr = err
			verificationFailed++
			continue
		}
		if err := assertPrivateFileUnchanged(path, bundle); err != nil {
			return "", "", nil, err
		}
		return identity, issuer, ran, nil
	}
	return "", "", nil, fmt.Errorf("all %d OCI bundles failed (%d rejected by claims checks, %d failed cosign verification); last failure: %w",
		len(bundles), claimsRejected, verificationFailed, lastErr)
}
