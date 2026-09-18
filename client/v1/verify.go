package client

import (
	"context"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
)

// ValidationVectorInput identifies a signed vector and its detached verification
// evidence. Source reading is owned by the configured authority, not this facade.
type ValidationVectorInput struct {
	SourcePath string
	Signature  trust.SignatureMaterial
	Offline    bool
}

type documentValidationVectorAuthority struct {
	authenticator *trust.DocumentAuthenticator
	policy        trust.RolePolicy
}

// NewDocumentValidationVectorAuthority adapts the authoritative trust module to the
// narrow validation-vector seam. The policy is permanently bound to that signer role.
func NewDocumentValidationVectorAuthority(authenticator *trust.DocumentAuthenticator, policy trust.RolePolicy) (ValidationVectorAuthority, error) {
	if authenticator == nil {
		return nil, e.New(e.CodeUsage, "validation-vector authenticator is nil")
	}
	if policy.Role() != trust.RoleValidationVector {
		return nil, e.New(e.CodeTrustPolicyMissing,
			"validation-vector authority requires %q policy, got %q",
			trust.RoleValidationVector, policy.Role())
	}
	return documentValidationVectorAuthority{authenticator: authenticator, policy: policy}, nil
}

func (authority documentValidationVectorAuthority) Authenticate(ctx context.Context, input ValidationVectorInput) (trust.DocumentReceipt, error) {
	return authority.authenticator.Authenticate(ctx, trust.AuthenticateRequest{
		SourcePath: input.SourcePath,
		Signature:  input.Signature,
		Offline:    input.Offline,
		Role:       trust.RoleValidationVector,
		Policy:     authority.policy,
	})
}

// BuilderProfileInput identifies a signed profile and its detached verification
// evidence. It is intentionally distinct from ValidationVectorInput so roles cannot
// be swapped accidentally at call sites.
type BuilderProfileInput struct {
	SourcePath string
	Signature  trust.SignatureMaterial
	Offline    bool
}

type documentBuilderProfileAuthority struct {
	authenticator *trust.DocumentAuthenticator
	policy        trust.RolePolicy
}

// NewDocumentBuilderProfileAuthority adapts the authoritative trust module to the
// builder-profile role and rejects cross-role policy reuse at construction.
func NewDocumentBuilderProfileAuthority(authenticator *trust.DocumentAuthenticator, policy trust.RolePolicy) (BuilderProfileAuthority, error) {
	if authenticator == nil {
		return nil, e.New(e.CodeUsage, "builder-profile authenticator is nil")
	}
	if policy.Role() != trust.RoleBuilderProfile {
		return nil, e.New(e.CodeTrustPolicyMissing,
			"builder-profile authority requires %q policy, got %q",
			trust.RoleBuilderProfile, policy.Role())
	}
	return documentBuilderProfileAuthority{authenticator: authenticator, policy: policy}, nil
}

func (authority documentBuilderProfileAuthority) Authenticate(ctx context.Context, input BuilderProfileInput) (trust.DocumentReceipt, error) {
	return authority.authenticator.Authenticate(ctx, trust.AuthenticateRequest{
		SourcePath: input.SourcePath,
		Signature:  input.Signature,
		Offline:    input.Offline,
		Role:       trust.RoleBuilderProfile,
		Policy:     authority.policy,
	})
}

// AuthenticatedValidationVector retains both the opaque proof and a defensive parse
// of the exact bytes that proof authenticated.
type AuthenticatedValidationVector struct {
	Receipt   trust.DocumentReceipt
	Published attest.PublishedVectorReceipt
	Manifest  *attest.Manifest
}

// AuthenticateValidationVector accepts only an authenticated, correctly typed and
// role-bound receipt. No caller-provided boolean or decoded manifest can substitute.
func (c *Client) AuthenticateValidationVector(ctx context.Context, input ValidationVectorInput) (*AuthenticatedValidationVector, error) {
	if err := c.ready(ctx, "validation-vector authentication"); err != nil {
		return nil, err
	}
	if c.vectorAuthority == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "validation-vector authority is not configured")
	}
	receipt, err := c.vectorAuthority.Authenticate(ctx, input)
	if err != nil {
		return nil, normalizeError(ctx, err, "validation-vector authentication")
	}
	if err := contextError(ctx, "validation-vector authentication"); err != nil {
		return nil, err
	}
	if !receipt.Authenticated() {
		return nil, e.New(e.CodeUntrustedSigner, "validation-vector authority returned an unauthenticated receipt")
	}
	if receipt.Role() != trust.RoleValidationVector {
		return nil, e.New(e.CodeUntrustedSigner,
			"authenticated document has signer role %q, want %q", receipt.Role(), trust.RoleValidationVector)
	}
	if receipt.SchemaVersion() != attest.SchemaVersion {
		return nil, e.New(e.CodeVerifyFailed,
			"authenticated validation vector has schemaVersion %q, want %q",
			receipt.SchemaVersion(), attest.SchemaVersion)
	}
	switch receipt.Decoded().(type) {
	case attest.Manifest, *attest.Manifest:
	default:
		return nil, e.New(e.CodeVerifyFailed,
			"authenticated %s document decoded as %T", attest.SchemaVersion, receipt.Decoded())
	}
	manifest, err := attest.Parse(receipt.ExactBytes())
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx, "validation-vector authentication"); err != nil {
		return nil, err
	}
	published, err := attest.VerifyPublishedVectorReceipt(ctx, receipt)
	if err != nil {
		return nil, err
	}
	return &AuthenticatedValidationVector{Receipt: receipt, Published: published, Manifest: manifest}, nil
}

// AuthenticatedBuilderProfile retains the opaque receipt and a defensive copy of the
// exact profile value it authenticated.
type AuthenticatedBuilderProfile struct {
	Receipt trust.DocumentReceipt
	Profile *profile.BuilderProfile
	Digest  string
}

// AuthenticateBuilderProfile accepts only an authenticated builder-profile receipt,
// validates the decoded domain object, and computes its authoritative digest.
func (c *Client) AuthenticateBuilderProfile(ctx context.Context, input BuilderProfileInput) (*AuthenticatedBuilderProfile, error) {
	if err := c.ready(ctx, "builder-profile authentication"); err != nil {
		return nil, err
	}
	if c.profileAuthority == nil {
		return nil, e.New(e.CodeUntrustedProfile, "builder-profile authority is not configured")
	}
	receipt, err := c.profileAuthority.Authenticate(ctx, input)
	if err != nil {
		return nil, normalizeError(ctx, err, "builder-profile authentication")
	}
	if err := contextError(ctx, "builder-profile authentication"); err != nil {
		return nil, err
	}
	if !receipt.Authenticated() || receipt.Role() != trust.RoleBuilderProfile {
		return nil, e.New(e.CodeUntrustedProfile,
			"builder-profile authority returned no authenticated %q receipt", trust.RoleBuilderProfile)
	}
	if receipt.SchemaVersion() != profile.SchemaVersion {
		return nil, e.New(e.CodeUntrustedProfile,
			"authenticated builder profile has schemaVersion %q, want %q",
			receipt.SchemaVersion(), profile.SchemaVersion)
	}
	decodedValue := receipt.Decoded()
	switch decoded := decodedValue.(type) {
	case profile.BuilderProfile:
	case *profile.BuilderProfile:
		if decoded == nil {
			return nil, e.New(e.CodeUntrustedProfile,
				"authenticated %s document decoded as a nil profile", profile.SchemaVersion)
		}
	default:
		return nil, e.New(e.CodeUntrustedProfile,
			"authenticated %s document decoded as %T", profile.SchemaVersion, decodedValue)
	}
	// Rehydrate from the immutable authenticated bytes, never from a receipt-owned
	// pointer or slice an injected authority could retain and mutate.
	var decoded profile.BuilderProfile
	if err := canonical.UnmarshalExact(receipt.ExactBytes(), &decoded); err != nil {
		return nil, e.Wrap(e.CodeUntrustedProfile, err,
			"decoding exact authenticated builder-profile bytes")
	}
	if err := decoded.Validate(); err != nil {
		return nil, err
	}
	digest, err := decoded.Digest()
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx, "builder-profile authentication"); err != nil {
		return nil, err
	}
	return &AuthenticatedBuilderProfile{Receipt: receipt, Profile: &decoded, Digest: digest}, nil
}

// VerifyAuthenticatedRequest describes the qualified rebuild path.
type VerifyAuthenticatedRequest struct {
	Expected   ValidationVectorInput
	Profile    BuilderProfileInput
	Resolve    ResolveRequest
	RunOptions pipeline.RunOptions
}

// VerificationResult exposes the authenticated expectation and independently built
// observation used by the comparison.
type VerificationResult struct {
	Expected           *AuthenticatedValidationVector
	Profile            *AuthenticatedBuilderProfile
	ProfileDivergences []profile.Divergence
	Built              *attest.Manifest
	Report             *pipeline.Report
}

// Close revokes the same-process authority retained by a successful authenticated
// verification. The result's public projections remain readable, but they can no
// longer authorize evidence or qualified publication after Close.
func (result *VerificationResult) Close() error {
	if result == nil {
		return nil
	}
	if result.Built != nil {
		return result.Built.Close()
	}
	return result.Report.Close()
}

// VerifyAuthenticated authenticates the expected bytes before starting a build, then
// resolves, executes, derives, and compares through the authoritative modules.
func (c *Client) VerifyAuthenticated(ctx context.Context, request VerifyAuthenticatedRequest) (*VerificationResult, error) {
	if err := c.ready(ctx, "authenticated verification"); err != nil {
		return nil, err
	}
	if c.vectorAuthority == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "validation-vector authority is not configured")
	}
	if c.publisherEvidence == nil {
		return nil, e.New(e.CodeProvenanceMissing,
			"authenticated verification requires a publisher-evidence authority")
	}
	expected, err := c.AuthenticateValidationVector(ctx, request.Expected)
	if err != nil {
		return nil, err
	}
	profileDigest, err := boundProfileDigest(expected.Manifest)
	if err != nil {
		return nil, err
	}
	var (
		authenticatedProfile *AuthenticatedBuilderProfile
		profileDivergences   []profile.Divergence
		hostReceipt          profile.HostPreflightReceipt
	)
	if profileDigest != "" {
		if c.hostObserver == nil {
			return nil, e.New(e.CodePreflightFailed,
				"profile-bound verification requires a context-aware HostObserver")
		}
		if c.executionObserver == nil {
			return nil, e.New(e.CodePreflightFailed,
				"profile-bound verification requires a stage-two ExecutionObserver")
		}
		authenticatedProfile, err = c.AuthenticateBuilderProfile(ctx, request.Profile)
		if err != nil {
			return nil, err
		}
		if authenticatedProfile.Digest != profileDigest {
			return nil, e.New(e.CodeUntrustedProfile,
				"authenticated builder profile digest %s does not match vector-bound digest %s",
				authenticatedProfile.Digest, profileDigest)
		}
		observed, observeErr := c.hostObserver.Observe(ctx)
		if observeErr != nil {
			return nil, normalizeError(ctx, observeErr, "builder-profile host observation")
		}
		if err := contextError(ctx, "builder-profile preflight"); err != nil {
			return nil, err
		}
		hostReceipt, profileDivergences, err = authenticatedProfile.Profile.VerifyHost(ctx, observed)
		if err != nil {
			return nil, err
		}
	} else if builderProfileInputPresent(request.Profile) {
		return nil, e.New(e.CodeVerifyFailed,
			"unbound builder profile cannot participate in validation-vector verification")
	}

	// Authentication and host preflight deliberately precede catalog/input reads.
	resolution, err := c.ResolveBuild(ctx, request.Resolve)
	if err != nil {
		return nil, err
	}
	requiredBuilders := buildgraph.RequiredBuilders(resolution.Resolved)
	fromSource := len(requiredBuilders) > 0
	if fromSource && authenticatedProfile == nil {
		return nil, e.New(e.CodeUntrustedProfile,
			"from-source authenticated verification requires a vector-bound builder profile")
	}
	if !fromSource && authenticatedProfile != nil {
		return nil, e.New(e.CodeVerifyFailed,
			"profile-bound validation vector resolved to an all-prebuilt output")
	}
	if authenticatedProfile != nil {
		if err := authenticatedProfile.Profile.RequireBuilders(requiredBuilders); err != nil {
			return nil, err
		}
	}
	runOptions := request.RunOptions
	if authenticatedProfile != nil {
		runOptions.Qualification = &pipeline.QualifiedExecution{
			Profile: *authenticatedProfile.Profile, HostReceipt: hostReceipt,
			Observer: c.executionObserver,
		}
	}
	report, err := c.Run(ctx, RunRequest{
		Graph: resolution.Graph, BuildDigest: resolution.BuildDigest, Options: runOptions,
	})
	if err != nil {
		return nil, err
	}
	keepReport := false
	defer func() {
		if !keepReport {
			_ = report.Close()
		}
	}()
	publisherEvidence, err := c.publisherEvidence.Qualify(ctx, PublisherEvidenceRequest{
		Resolved: resolution.Resolved, Graph: resolution.Graph, Report: report,
	})
	if err != nil {
		return nil, normalizeError(ctx, err, "publisher-evidence qualification")
	}
	if err := publisherEvidence.VerifyAgainstPublishedVector(ctx, expected.Published); err != nil {
		return nil, err
	}
	built, err := c.CreateValidationVector(ctx, ValidationVectorRequest{
		Report: report, PublisherEvidence: publisherEvidence,
	})
	if err != nil {
		return nil, err
	}
	if fromSource {
		if got := report.QualificationProfileDigest(); got != authenticatedProfile.Digest {
			return nil, e.New(e.CodePreflightFailed,
				"qualified report profile %s does not match authenticated profile %s", got, authenticatedProfile.Digest)
		}
		if built.ProfileDigest != report.QualificationProfileDigest() {
			return nil, e.New(e.CodePreflightFailed,
				"qualified vector profile %s does not match report profile %s",
				built.ProfileDigest, report.QualificationProfileDigest())
		}
	}
	if err := contextError(ctx, "validation-vector comparison"); err != nil {
		return nil, err
	}
	if err := expected.Manifest.VerifyQualifiedAgainst(ctx, built); err != nil {
		return nil, err
	}
	if err := contextError(ctx, "validation-vector comparison"); err != nil {
		return nil, err
	}
	keepReport = true
	return &VerificationResult{
		Expected: expected, Profile: authenticatedProfile,
		ProfileDivergences: append([]profile.Divergence(nil), profileDivergences...),
		Built:              built, Report: report,
	}, nil
}

func boundProfileDigest(vector *attest.Manifest) (string, error) {
	if vector.BuilderProfile == nil {
		return vector.ProfileDigest, nil
	}
	if strings.TrimSpace(vector.BuilderProfile.Digest) == "" {
		return "", e.New(e.CodeVerifyFailed,
			"authenticated vector has a builderProfile reference without a digest")
	}
	if vector.ProfileDigest != vector.BuilderProfile.Digest {
		return "", e.New(e.CodeVerifyFailed,
			"authenticated vector has inconsistent profileDigest %q and builderProfile.digest %q",
			vector.ProfileDigest, vector.BuilderProfile.Digest)
	}
	return vector.BuilderProfile.Digest, nil
}

func builderProfileInputPresent(input BuilderProfileInput) bool {
	return strings.TrimSpace(input.SourcePath) != "" ||
		strings.TrimSpace(input.Signature.SignaturePath) != "" ||
		strings.TrimSpace(input.Signature.CertificatePath) != "" ||
		strings.TrimSpace(input.Signature.BundlePath) != "" ||
		strings.TrimSpace(input.Signature.TrustedRootPath) != "" || input.Offline
}
