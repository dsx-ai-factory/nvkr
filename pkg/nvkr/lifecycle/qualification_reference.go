// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"regexp"
	"runtime"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// CheckQualificationReference selects an immutable published root, authenticates
// its source invocation under the independently selected policy, then checks the
// complete source Run. The returned receipt owns its source and payload lifetime.
// Neither a detached description nor an unsigned local Run can restore it.
func CheckQualificationReference(ctx context.Context, reference, policyPath string, options QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	if err := admitQualificationRun(ctx); err != nil {
		return nil, nil, err
	}
	if err := admitQualificationReference(reference); err != nil {
		return nil, nil, err
	}
	if err := admitQualificationReferenceTransport(reference, options); err != nil {
		return nil, nil, err
	}
	policy, err := LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return nil, nil, err
	}
	binary := policy.Cosign.Path
	if binary == "" {
		binary = sign.DefaultBinary
	}
	digest, err := policy.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return nil, nil, err
	}
	pinned, err := sign.NewPinnedCosignSigner(binary, digest)
	if err != nil {
		return nil, nil, err
	}
	if err := pinned.Preflight(ctx); err != nil {
		return nil, nil, err
	}
	consumer, err := newQualificationConsumer(reference, policy, options)
	if err != nil {
		return nil, nil, err
	}
	return checkQualificationReferenceWithPolicy(ctx, reference, policy, options, consumer.Authenticate, func(ctx context.Context, locator qualificationorigin.RunLocator, frozen QualificationPolicy, runtimeOptions QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
		return checkQualificationRunWithPolicyAndReference(ctx, locator, frozen, runtimeOptions, reference)
	})
}

func admitQualificationReference(reference string) error {
	if strings.Contains(reference, "://") {
		return e.New(e.CodeUnverifiedRef, "qualification publication requires an OCI reference, not a URL")
	}
	return delivery.ValidatePublicationReference(reference)
}

type authenticateQualificationReference func(context.Context, string, VerificationPolicy) (*AuthenticatedRelease, error)
type checkSelectedQualificationRun func(context.Context, qualificationorigin.RunLocator, QualificationPolicy, QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error)

// Private dependencies exercise orchestration without providing public receipt
// issuers or replacing the production signature/provider owners.
func checkQualificationReferenceWithPolicy(ctx context.Context, reference string, policy QualificationPolicy, options QualificationRuntimeOptions, authenticate authenticateQualificationReference, checkRun checkSelectedQualificationRun) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "qualification reference requires context")
	}
	if err := authenticityContext(ctx); err != nil {
		return nil, nil, err
	}
	if err := admitQualificationReference(reference); err != nil {
		return nil, nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, nil, err
	}
	if authenticate == nil || checkRun == nil {
		return nil, nil, e.New(e.CodeInternal, "qualification reference dependencies are incomplete")
	}
	builder := "https://github.com/" + policy.Source.Repository + "/" + policy.Source.Workflow + "@refs/heads/" + policy.Source.DefaultBranch
	trust := VerificationPolicy{Lane: LanePipeline, Scope: ScopeAuthenticity, IdentityRegexp: "^" + regexp.QuoteMeta(builder) + "$", Issuer: "https://token.actions.githubusercontent.com"}
	authenticated, err := authenticate(ctx, reference, trust)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = authenticated.Close() }()
	invocation, err := authenticated.sourceInvocation(ctx)
	if err != nil {
		return nil, nil, err
	}
	locator, err := qualificationorigin.ParseRunURL(invocation)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeProvenanceMissing, err, "authenticated publication has no exact source attempt")
	}
	if locator.Repository != policy.Source.Repository {
		return nil, nil, e.New(e.CodeUntrustedSigner, "authenticated publication source differs from the selected policy")
	}
	receipt, report, err := checkRun(ctx, locator, policy, options)
	if err != nil {
		if receipt != nil {
			_ = receipt.Close()
		}
		return nil, report, err
	}
	description, err := receipt.Description()
	if err == nil && description.Inputs.ReleaseSubject != reference {
		err = e.New(e.CodeVerifyFailed, "authenticated source Run does not bind the explicitly selected publication")
	}
	if err == nil {
		err = authenticityContext(ctx)
	}
	if err != nil {
		_ = receipt.Close()
		return nil, nil, err
	}
	return receipt, &description, nil
}

// Reject a substituted candidate before its consumer contacts another publication.
// The existing handoff owner subsequently validates the full source and Plan.
func requireQualificationHandoffReference(raw []byte, reference string) error {
	if len(raw) == 0 || len(raw) > qualificationorigin.MaxQualificationHandoffBytes {
		return e.New(e.CodeProvenanceMissing, "qualification handoff is empty or exceeds its byte limit")
	}
	var handoff QualificationHandoff
	if err := canonical.UnmarshalExact(raw, &handoff); err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "decoding selected qualification handoff")
	}
	if handoff.Candidate.Reference != reference {
		return e.New(e.CodeVerifyFailed, "source handoff does not bind the explicitly selected publication")
	}
	return nil
}

// The upstream ambient keychain does not honor context cancellation while it
// invokes credential helpers. This adapter therefore admits only explicit auth
// choices, leaving legacy consumer behavior unchanged.
func admitQualificationReferenceTransport(reference string, options QualificationRuntimeOptions) error {
	if err := options.Credential.Validate(); err != nil {
		return err
	}
	if _, err := newQualificationReferenceConsumer(reference, ConsumerOptions{Credential: options.Credential, Registry: options.Registry}); err != nil {
		return err
	}
	if options.Registry.Anonymous {
		if options.Credential != (sign.Credential{}) {
			return e.New(e.CodeUsage, "anonymous qualification reference access cannot carry signing registry credentials")
		}
		return nil
	}
	if options.Registry.Password == "" {
		return e.New(e.CodeEngineCapability, "qualification reference access requires explicit anonymous access or independently scoped registry credentials; ambient credential helpers are unsupported")
	}
	return nil
}
