// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"runtime"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// qualificationIssuer is the only OIDC issuer the hosted qualification lane admits.
const qualificationIssuer = "https://token.actions.githubusercontent.com"

// Policy is selected independently of the evidence it authenticates. The
// evidence cannot name its own trusted signer or qualification policy.
type Policy struct {
	// QualificationIdentity is the exact expected certificate SAN, not a pattern.
	QualificationIdentity     string
	QualificationPolicyDigest string
}

type Options struct {
	CosignBinary string
	CosignDigest string
	Credential   sign.Credential
}

// Issuer authenticates complete passing M3 evidence, then signs and verifies
// one release manifest under the qualification identity.
type Issuer struct {
	newSigner func(identity string) (manifestSigner, error)
}

type manifestSigner interface {
	VerifyQualification(context.Context, sign.Subject, []byte) error
	AttestReleaseManifest(context.Context, sign.Subject, []byte) error
	VerifyReleaseManifest(context.Context, sign.Subject, []byte) error
}

// Private test seam; the public operation accepts only the lifecycle receipt.
type checkReceipt interface {
	Description() (lifecycle.ArtifactCheckDescription, error)
	ReleaseMembers() ([]lifecycle.QualificationReleaseMember, error)
}

// NewIssuer admits the Linux pinned signer before callers request registry work.
// There is no production fake or dry-run selector.
func NewIssuer(options Options) (*Issuer, error) {
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "release manifest signing requires Linux pinned cosign execution")
	}
	if !canonical.IsDigest(options.CosignDigest) || options.CosignBinary == "" {
		return nil, e.New(e.CodeUnpinnedInput, "release manifest signing requires an exact cosign executable pin")
	}
	return &Issuer{newSigner: func(identity string) (manifestSigner, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary, BinaryDigest: options.CosignDigest,
			Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: qualificationIssuer, Credential: options.Credential})
	}}, nil
}

// Receipt is issued only after the manifest attestation on the publication root
// has been independently verified. Decoded manifests cannot restore it, and it
// grants no tag, channel or qualified-lane authority.
type Receipt struct {
	raw     string
	digest  string
	subject sign.Subject
}

// Manifest returns a defensive copy of the exact issued document.
func (receipt *Receipt) Manifest() (Manifest, error) {
	if err := receipt.require(); err != nil {
		return Manifest{}, err
	}
	return Decode([]byte(receipt.raw))
}

// Canonical returns a copy of the exact signed bytes.
func (receipt *Receipt) Canonical() ([]byte, error) {
	if err := receipt.require(); err != nil {
		return nil, err
	}
	return []byte(receipt.raw), nil
}

// Digest returns the nvkr.release-manifest identity of the issued document.
func (receipt *Receipt) Digest() (string, error) {
	if err := receipt.require(); err != nil {
		return "", err
	}
	return receipt.digest, nil
}

// Subject returns the publication root that carries the manifest attestation.
func (receipt *Receipt) Subject() (sign.Subject, error) {
	if err := receipt.require(); err != nil {
		return sign.Subject{}, err
	}
	return receipt.subject, nil
}

func (receipt *Receipt) require() error {
	if receipt == nil || receipt.raw == "" || receipt.digest == "" {
		return e.New(e.CodeGateUnmet, "release manifest has not been issued")
	}
	return nil
}

// Issue authenticates the exact canonical qualification evidence on every
// member subject under the independently selected identity, binds it to the
// live artifact-check receipt, and only then attests and re-verifies the
// manifest on the publication root. It never rebuilds or substitutes artifact
// bytes. An attestation failure may leave an attachment and never succeeds.
func (issuer *Issuer) Issue(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, evidence []byte, policy Policy) (*Receipt, error) {
	if checks == nil {
		return nil, e.New(e.CodeGateUnmet, "release manifest requires a live artifact check receipt")
	}
	return issuer.issue(ctx, checks, evidence, policy)
}

func (issuer *Issuer) issue(ctx context.Context, checks checkReceipt, evidence []byte, policy Policy) (*Receipt, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "release manifest issuance requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release manifest issuance canceled")
	}
	if issuer == nil || issuer.newSigner == nil {
		return nil, e.New(e.CodeEngineCapability, "release manifest issuer is unavailable")
	}
	identity := policy.QualificationIdentity
	if identity == "" || strings.TrimSpace(identity) != identity || strings.ContainsAny(identity, " \t\r\n") {
		return nil, e.New(e.CodeTrustPolicyMissing, "release manifest requires the exact independently selected qualification identity")
	}
	if !canonical.IsDigest(policy.QualificationPolicyDigest) {
		return nil, e.New(e.CodeTrustPolicyMissing, "release manifest requires the independently selected qualification policy digest")
	}
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	members, err := checks.ReleaseMembers()
	if err != nil {
		return nil, err
	}
	decoded, err := qualification.Decode(evidence)
	if err != nil {
		return nil, err
	}
	exact, err := decoded.Canonical()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(exact, evidence) {
		return nil, e.New(e.CodeVerifyFailed, "qualification evidence bytes are not the canonical attested document")
	}
	if err := sameChecks(decoded.ArtifactChecks, description); err != nil {
		return nil, err
	}
	if decoded.Invocation.BuilderID != identity {
		return nil, e.New(e.CodeUntrustedSigner, "qualification evidence names another qualification identity")
	}
	if decoded.ToolSmoke.PolicyDigest != policy.QualificationPolicyDigest {
		return nil, e.New(e.CodeTrustPolicyMissing, "qualification evidence was produced under another qualification policy")
	}
	manifest, err := fromEvidence(decoded, members)
	if err != nil {
		return nil, err
	}
	raw, err := manifest.Canonical()
	if err != nil {
		return nil, err
	}
	digest, err := manifest.Digest()
	if err != nil {
		return nil, err
	}
	root := sign.Subject{Ref: manifest.Release.Reference, Digest: manifest.Release.Digest}
	recheck := func(stage string) error {
		if cause := ctx.Err(); cause != nil {
			return e.Wrap(e.CodeTimeout, cause, "release manifest issuance canceled %s", stage)
		}
		current, err := checks.Description()
		if err != nil {
			return err
		}
		return sameChecks(current, description)
	}
	if err := recheck("before signer admission"); err != nil {
		return nil, err
	}
	signer, err := issuer.newSigner(identity)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, e.New(e.CodeEngineCapability, "release manifest signing adapter is unavailable")
	}
	// One surviving attachment cannot prove the set: every member must carry
	// these exact evidence bytes under the qualification identity.
	for _, member := range manifest.Members {
		if err := recheck("before qualification verification"); err != nil {
			return nil, err
		}
		if err := signer.VerifyQualification(ctx, sign.Subject{Ref: member.Reference, Digest: member.Digest}, exact); err != nil {
			return nil, withContext(err, "qualification evidence is not authenticated for member %q", member.Name)
		}
	}
	if err := recheck("before manifest attestation"); err != nil {
		return nil, err
	}
	if err := signer.AttestReleaseManifest(ctx, root, raw); err != nil {
		return nil, withContext(err, "release manifest attestation failed; an attachment may remain")
	}
	if err := recheck("before manifest verification"); err != nil {
		return nil, err
	}
	if err := signer.VerifyReleaseManifest(ctx, root, raw); err != nil {
		return nil, withContext(err, "release manifest attestation did not verify; an attachment may remain")
	}
	if err := recheck("after manifest verification"); err != nil {
		return nil, err
	}
	return &Receipt{raw: string(raw), digest: digest, subject: root}, nil
}

func sameChecks(left, right lifecycle.ArtifactCheckDescription) error {
	a, err := canonical.Marshal(left)
	if err != nil {
		return err
	}
	b, err := canonical.Marshal(right)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return e.New(e.CodeVerifyFailed, "qualification evidence describes another artifact check")
	}
	return nil
}

// withContext adds the issuance stage while preserving an adapter's coded cause.
func withContext(err error, format string, args ...any) error {
	code := e.CodeGateUnmet
	var coded *e.Error
	if errors.As(err, &coded) {
		code = coded.Code
	}
	return e.Wrap(code, err, format, args...)
}
