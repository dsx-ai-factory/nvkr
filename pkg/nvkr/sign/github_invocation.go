// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"crypto/x509"
	"encoding/asn1"
	"regexp"
	"strings"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

// Fulcio's GitHub mapping uses workflow_ref/workflow_sha for the initiating
// workflow and run_id/run_attempt for the invocation, not job_workflow_ref or sha:
// https://github.com/sigstore/fulcio/blob/main/config/identity/config.yaml
// All three extensions are DER UTF8String, not the legacy raw-string extensions:
// https://github.com/sigstore/fulcio/blob/main/docs/oid-info.md
var (
	fulcioBuildConfigURI    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 18}
	fulcioBuildConfigDigest = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 19}
	fulcioRunInvocationURI  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 21}
	githubRunURI            = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*)/actions/runs/[1-9][0-9]{0,19}/attempts/[1-9][0-9]{0,19}$`)
	githubConfigURI         = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*)/\.github/workflows/[A-Za-z0-9][A-Za-z0-9_.-]*\.ya?ml@refs/heads/([A-Za-z0-9][A-Za-z0-9._/-]*)$`)
	githubWorkflowCommit    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// snapshotGitHubInvocationConstraints admits and copies the optional constraint
// before filesystem or subprocess work. It does not authenticate the expected
// values or authorize a worker role; those belong to the requesting owner.
func snapshotGitHubInvocationConstraints(req trust.DocumentVerificationRequest) (*trust.GitHubInvocationConstraints, error) {
	if req.GitHubInvocation == nil {
		return nil, nil
	}
	expected := *req.GitHubInvocation
	run := githubRunURI.FindStringSubmatch(expected.RunInvocationURI)
	config := githubConfigURI.FindStringSubmatch(expected.BuildConfigURI)
	if req.OIDCIssuer != "https://token.actions.githubusercontent.com" ||
		len(req.AllowedIdentities) == 0 || len(run) != 2 || len(config) != 3 || run[1] != config[1] ||
		!githubWorkflowCommit.MatchString(expected.BuildConfigDigest) {
		return nil, e.New(e.CodeTrustPolicyMissing,
			"GitHub invocation verification requires the GitHub issuer, signer identity and exact same-repository Run attempt, workflow URI and commit")
	}
	// Reject ambiguous branch spellings rather than normalize authenticated facts.
	for _, segment := range strings.Split(config[2], "/") {
		if segment == "" || segment == "." || strings.Contains(segment, "..") ||
			strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return nil, e.New(e.CodeTrustPolicyMissing, "GitHub invocation workflow URI contains an invalid branch")
		}
	}
	return &expected, nil
}

// verifiedGitHubInvocation is private and is called only after cosign succeeds
// and the held protocol-selected certificate and material remain unchanged.
func verifiedGitHubInvocation(cert *x509.Certificate, expected *trust.GitHubInvocationConstraints) (*trust.GitHubInvocationConstraints, error) {
	if expected == nil {
		return nil, nil
	}
	var actual trust.GitHubInvocationConstraints
	for _, claim := range []struct {
		name string
		oid  asn1.ObjectIdentifier
		want string
		got  *string
	}{
		{"RunInvocationURI", fulcioRunInvocationURI, expected.RunInvocationURI, &actual.RunInvocationURI},
		{"BuildConfigURI", fulcioBuildConfigURI, expected.BuildConfigURI, &actual.BuildConfigURI},
		{"BuildConfigDigest", fulcioBuildConfigDigest, expected.BuildConfigDigest, &actual.BuildConfigDigest},
	} {
		value, err := exactUTF8CertificateExtension(cert, claim.oid, claim.name)
		if err != nil {
			return nil, err
		}
		if value != claim.want {
			return nil, e.New(e.CodeUntrustedSigner,
				"verified certificate %s %q does not match expected %q", claim.name, value, claim.want)
		}
		*claim.got = value
	}
	return &actual, nil
}

func exactUTF8CertificateExtension(cert *x509.Certificate, oid asn1.ObjectIdentifier, name string) (string, error) {
	var encoded []byte
	count := 0
	for _, extension := range cert.Extensions {
		if extension.Id.Equal(oid) {
			count++
			encoded = extension.Value
		}
	}
	if count != 1 {
		return "", e.New(e.CodeSignatureInvalid,
			"verified certificate requires exactly one Fulcio %s extension, found %d", name, count)
	}
	var value asn1.RawValue
	rest, err := asn1.Unmarshal(encoded, &value)
	if err != nil || len(rest) != 0 || value.Class != asn1.ClassUniversal || value.Tag != asn1.TagUTF8String ||
		value.IsCompound || len(value.Bytes) == 0 || !utf8.Valid(value.Bytes) {
		return "", e.New(e.CodeSignatureInvalid, "verified certificate Fulcio %s extension is not one DER UTF8String", name)
	}
	return string(value.Bytes), nil
}
