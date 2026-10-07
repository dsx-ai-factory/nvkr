package lifecycle

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

type releaseMaterializer func(context.Context, string) (*delivery.Publication, error)

// ConsumerOptions configures transport and signature verification once. Each
// Verify call then accepts only the Release reference and Verification Policy.
type ConsumerOptions struct {
	CosignBinary  string
	CosignDigest  string
	Credential    sign.Credential
	Registry      delivery.RegistryOptions
	WorkspaceRoot string
}

type Consumer struct {
	verifier    sign.OCISigner
	verifierFor func(VerificationPolicy) (sign.OCISigner, error)
	materialize releaseMaterializer
}

// VerifiedRelease is an opaque, policy-bound live capability. Description is a
// defensive public projection; Close releases its verified local closure.
type VerifiedRelease struct {
	mu          sync.RWMutex
	valid       bool
	release     Release
	publication *delivery.Publication
}

func NewConsumer(options ConsumerOptions) (*Consumer, error) {
	root := options.WorkspaceRoot
	if root == "" {
		root = filepath.ToSlash(filepath.Join(".nkd", "verified-releases"))
	}
	return &Consumer{verifierFor: func(policy VerificationPolicy) (sign.OCISigner, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary,
			BinaryDigest: options.CosignDigest, Identity: policy.IdentityRegexp, Issuer: policy.Issuer, Credential: options.Credential})
	}, materialize: func(ctx context.Context, reference string) (*delivery.Publication, error) {
		return delivery.PullPublication(ctx, reference, root, options.Registry)
	}}, nil
}

func (verified *VerifiedRelease) Description() (Release, error) {
	if verified == nil {
		return Release{}, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	verified.mu.RLock()
	defer verified.mu.RUnlock()
	if !verified.valid {
		return Release{}, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	description := verified.release
	description.Artifacts = cloneArtifacts(verified.release.Artifacts)
	return description, nil
}

func (verified *VerifiedRelease) Close() error {
	if verified == nil {
		return nil
	}
	verified.mu.Lock()
	defer verified.mu.Unlock()
	if !verified.valid {
		return nil
	}
	verified.valid = false
	return verified.publication.Close()
}

// Verify consumes one pinned reference under one explicit policy. The currently
// implemented positive scope authenticates a pipeline-produced publication root
// signature and SLSA provenance attestation, then independently pulls and checks
// every reachable descriptor. Full release and qualified scopes remain closed.
func (consumer *Consumer) Verify(ctx context.Context, reference string, policy VerificationPolicy) (*VerifiedRelease, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "release verification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release verification canceled")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	digest := referenceDigest(reference)
	subject := sign.Subject{Ref: reference, Digest: digest}
	if err := subject.Validate(); err != nil {
		return nil, err
	}
	if policy.Lane == LaneQualified {
		return nil, e.New(e.CodeEngineCapability, "qualified Release verification requires authenticated discovery, clearance, trusted-time, status, and publication-commit adapters")
	}
	if policy.Scope == ScopeRelease {
		return nil, e.New(e.CodeEngineCapability, "pipeline-attested full Release verification requires qualification and lifecycle-state adapters")
	}
	if consumer == nil || (consumer.verifier == nil && consumer.verifierFor == nil) || consumer.materialize == nil {
		return nil, e.New(e.CodeEngineCapability, "artifact-publication verification requires configured signature and registry adapters")
	}
	verifier := consumer.verifier
	if verifier == nil {
		configured, configureErr := consumer.verifierFor(policy)
		if configureErr != nil {
			return nil, configureErr
		}
		verifier = configured
	}
	record, err := verifier.Verify(ctx, subject, sign.VerifyPolicy{IdentityRegexp: policy.IdentityRegexp,
		Issuer: policy.Issuer, RequireAttestation: true})
	if err != nil {
		return nil, err
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.DryRun || !record.SignatureVerified || !record.AttestationVerified || record.Ref != reference || record.Digest != digest {
		return nil, e.New(e.CodeSignatureInvalid, "publication signature and provenance do not authenticate the exact requested subject")
	}
	identityPattern, err := regexp.Compile(policy.IdentityRegexp)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "compiling release identity policy")
	}
	if !identityPattern.MatchString(record.ObservedIdentity) || record.ObservedIssuer != policy.Issuer {
		return nil, e.New(e.CodeUntrustedSigner, "observed release signer does not satisfy the requested identity and issuer policy")
	}
	publication, err := consumer.materialize(ctx, reference)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = publication.Close()
		}
	}()
	artifacts, contentSubject, err := publication.Artifacts(reference)
	if err != nil {
		return nil, err
	}
	if err := requireDiskEvidence(policy, artifacts); err != nil {
		return nil, err
	}
	if err := publication.RequireAttestedEvidence(); err != nil {
		return nil, err
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	description := Release{SchemaVersion: ReleaseSchemaVersion, Subject: reference, Lane: policy.Lane,
		Scope: policy.Scope, Artifacts: artifacts, ContentSubject: contentSubject, PolicyDigest: policyDigest}
	if err := description.validate(); err != nil {
		return nil, err
	}
	failed = false
	return &VerifiedRelease{valid: true, release: description, publication: publication}, nil
}

func requireDiskEvidence(policy VerificationPolicy, artifacts []delivery.Output) error {
	minimum := policy.MinimumDiskEvidence
	if minimum == "" {
		minimum = DiskEvidenceStrict
	}
	for _, artifact := range artifacts {
		if artifact.Kind != OutputDiskImage {
			continue
		}
		if artifact.Disk == nil {
			return e.New(e.CodeVerifyFailed, "disk artifact %q has no evidence binding", artifact.Name)
		}
		switch artifact.Disk.EvidenceMode {
		case "": // Strict inspected evidence is stronger than either policy floor.
		case string(DiskEvidenceBuildProvenance):
			if minimum != DiskEvidenceBuildProvenance {
				return e.New(e.CodeGateUnmet, "disk artifact %q has producer-only evidence, below consumer minimum %q", artifact.Name, minimum)
			}
		default:
			return e.New(e.CodeVerifyFailed, "disk artifact %q has unsupported evidence mode %q", artifact.Name, artifact.Disk.EvidenceMode)
		}
	}
	return nil
}

// InspectReleaseLayout returns exact Artifact descriptions from an already
// materialized publication closure. It authenticates no release lane.
func InspectReleaseLayout(ctx context.Context, reference, layoutPath string) ([]Artifact, error) {
	digest := referenceDigest(reference)
	if err := (sign.Subject{Ref: reference, Digest: digest}).Validate(); err != nil {
		return nil, err
	}
	publication, err := delivery.OpenPublication(ctx, layoutPath, digest)
	if err != nil {
		return nil, err
	}
	defer func() { _ = publication.Close() }()
	artifacts, _, err := publication.Artifacts(reference)
	return artifacts, err
}

func (release Release) validate() error {
	if release.SchemaVersion != ReleaseSchemaVersion || release.Lane != LanePipeline || release.Scope != ScopeArtifactPublication ||
		!canonical.IsDigest(release.PolicyDigest) || !canonical.IsDigest(release.ContentSubject) || len(release.Artifacts) == 0 {
		return e.New(e.CodeGateUnmet, "Release does not contain a supported policy-bound verified closure")
	}
	digest := referenceDigest(release.Subject)
	if err := (sign.Subject{Ref: release.Subject, Digest: digest}).Validate(); err != nil {
		return err
	}
	previous := ""
	for _, artifact := range release.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
		key := artifact.Name + "\x00" + artifact.Kind
		if key <= previous {
			return e.New(e.CodeVerifyFailed, "Release artifacts must be sorted and unique")
		}
		previous = key
	}
	return nil
}

func referenceDigest(reference string) string {
	if strings.Count(reference, "@") != 1 {
		return ""
	}
	return reference[strings.LastIndexByte(reference, '@')+1:]
}
