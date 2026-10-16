// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"runtime"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/name"
)

const QualificationHandoffSchema = "nvkr.dev/qualification-handoff/v1"

// QualificationHandoff is generated transport from one producing attempt. It
// contains no consumer policy and cannot restore source or artifact authority.
type QualificationHandoff struct {
	SchemaVersion string                          `json:"schemaVersion"`
	Source        qualificationorigin.Description `json:"source"`
	Plan          []byte                          `json:"plan"`
	Inputs        factory.InputSnapshot           `json:"inputs"`
	Candidate     QualificationCandidate          `json:"candidate"`
}

// QualificationRuntimeOptions contains process configuration, not per-Run trust
// decisions. Verifier selection and its byte pin come from the trusted policy.
type QualificationRuntimeOptions struct {
	GitHubToken    string
	CIStorageToken string
	WorkspaceRoot  string
	Credential     sign.Credential
	Registry       delivery.RegistryOptions
}

// CheckQualificationRun selects a complete saved Run using two authored inputs:
// its exact attempt URL and independently selected policy. The returned receipt
// owns the authenticated source lifetime; stdout diagnostics cannot restore it.
func CheckQualificationRun(ctx context.Context, runURL, policyPath string, options QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	if err := admitQualificationRun(ctx); err != nil {
		return nil, nil, err
	}
	locator, err := qualificationorigin.ParseRunURL(runURL)
	if err != nil {
		return nil, nil, err
	}
	policy, err := LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return nil, nil, err
	}
	return checkQualificationRunWithPolicyAndReference(ctx, locator, policy, options, "")
}

func checkQualificationRunWithPolicyAndReference(ctx context.Context, locator qualificationorigin.RunLocator, policy QualificationPolicy, options QualificationRuntimeOptions, reference string) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	provider := qualificationorigin.NewGitHubWithCIStorage(options.GitHubToken, options.CIStorageToken)
	source, err := provider.AuthenticateRun(ctx, locator, policy.Source)
	if err != nil {
		return nil, nil, err
	}
	return completeQualificationRunReference(ctx, reference, source.Close,
		func(ctx context.Context) ([]byte, error) { return provider.DownloadQualificationHandoff(ctx, source) },
		func(ctx context.Context, raw []byte) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
			return CheckQualificationHandoff(ctx, raw, source, policy, options)
		})
}

// Keep ownership and exact-reference checks in one production path, independently
// of the provider transport. These private callbacks cannot issue public authority.
func completeQualificationRunReference(ctx context.Context, reference string, closeSource func() error, download func(context.Context) ([]byte, error), check func(context.Context, []byte) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error)) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	raw, err := download(ctx)
	if err != nil {
		_ = closeSource()
		return nil, nil, err
	}
	if reference != "" {
		if err := requireQualificationHandoffReference(raw, reference); err != nil {
			_ = closeSource()
			return nil, nil, err
		}
	}
	receipt, report, err := check(ctx, raw)
	if err != nil {
		if receipt != nil {
			_ = receipt.Close()
		}
		_ = closeSource()
		return nil, report, err
	}
	receipt.lifetime.closeSource = closeSource
	return receipt, report, nil
}

// CheckQualificationHandoff borrows an already authenticated source, allowing the
// independent qualifier to share source admission with artifact checks. The
// candidate's files and runtime choices never become the consumer's trust policy.
func CheckQualificationHandoff(ctx context.Context, raw []byte, source *qualificationorigin.SourceRun, policy QualificationPolicy, options QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	if err := admitQualificationRun(ctx); err != nil {
		return nil, nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, nil, err
	}
	origin, err := source.Description()
	if err != nil {
		return nil, nil, err
	}
	if origin.Repository != policy.Source.Repository || origin.Workflow != policy.Source.Workflow || origin.DefaultBranch != policy.Source.DefaultBranch {
		return nil, nil, e.New(e.CodeUntrustedSigner, "authenticated Run differs from the independently selected qualification policy")
	}
	handoff, plan, err := prepareQualificationHandoff(ctx, raw, origin)
	if err != nil {
		return nil, nil, err
	}
	consumer, err := newQualificationConsumer(handoff.Candidate.Reference, policy, options)
	if err != nil {
		return nil, nil, err
	}
	return consumer.CheckQualification(ctx, QualificationRequest{Reference: handoff.Candidate.Reference,
		Definition: plan.Definition, Environment: plan.Environment, Plan: plan, Origin: source,
		Predicates: handoff.Candidate.Predicates})
}

func newQualificationConsumer(reference string, policy QualificationPolicy, options QualificationRuntimeOptions) (*Consumer, error) {
	binary := policy.Cosign.Path
	if binary == "" {
		binary = "cosign"
	}
	digest, err := policy.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	return newQualificationReferenceConsumer(reference, ConsumerOptions{CosignBinary: binary, CosignDigest: digest,
		WorkspaceRoot: options.WorkspaceRoot, Credential: options.Credential, Registry: options.Registry})
}

// Explicit pull credentials need an independently selected host: signatures
// authenticate content digests, so valid bundles may also exist at an untrusted
// mirror. Every qualification adapter uses this admission before constructing a
// consumer. Candidate and Environment records cannot select the credential host.
// Delivery confines child and evidence references to root siblings; cosign scopes
// its separate credential through a private Docker config.
func newQualificationReferenceConsumer(reference string, options ConsumerOptions) (*Consumer, error) {
	if err := options.Registry.Validate(); err != nil {
		return nil, err
	}
	if strings.Contains(reference, "://") {
		return nil, e.New(e.CodeUnverifiedRef, "qualification publication requires an OCI reference, not a URL")
	}
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return nil, err
	}
	if options.Registry.Password != "" {
		registry, err := name.NewRegistry(options.Credential.Registry, name.StrictValidation)
		if err != nil || registry.Name() != options.Credential.Registry {
			return nil, e.New(e.CodeTrustPolicyMissing, "explicit qualification registry credentials require an independent canonical registry host")
		}
		reference, err := name.NewDigest(reference, name.StrictValidation)
		if err != nil || reference.Context().RegistryStr() != registry.RegistryStr() {
			return nil, e.New(e.CodeUntrustedSigner, "qualification publication registry differs from the independently configured credential host")
		}
	}
	return NewConsumer(options)
}

func admitQualificationRun(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "artifact qualification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "artifact qualification canceled")
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "artifact qualification requires Linux pinned-executable verification")
	}
	return nil
}

func prepareQualificationHandoff(ctx context.Context, raw []byte, origin qualificationorigin.Description) (QualificationHandoff, *factory.Plan, error) {
	var handoff QualificationHandoff
	if len(raw) == 0 || len(raw) > qualificationorigin.MaxQualificationHandoffBytes {
		return handoff, nil, e.New(e.CodeProvenanceMissing, "qualification handoff is empty or exceeds its byte limit")
	}
	if err := canonical.UnmarshalExact(raw, &handoff); err != nil {
		return handoff, nil, e.Wrap(e.CodeProvenanceMissing, err, "decoding qualification handoff")
	}
	if handoff.SchemaVersion != QualificationHandoffSchema || handoff.Source != origin {
		return handoff, nil, e.New(e.CodeVerifyFailed, "qualification handoff differs from the exact authenticated source attempt")
	}
	if handoff.Candidate.SchemaVersion != QualificationCandidateSchema || handoff.Candidate.Reference == "" || len(handoff.Candidate.Predicates) == 0 {
		return handoff, nil, e.New(e.CodeProvenanceMissing, "qualification handoff omits the complete original publication evidence")
	}
	if len(handoff.Plan) == 0 || int64(len(handoff.Plan)) > factory.MaxPlanBytes {
		return handoff, nil, e.New(e.CodeProvenanceMissing, "qualification handoff Plan is empty or exceeds its byte limit")
	}
	var original factory.Plan
	if err := canonical.UnmarshalExact(handoff.Plan, &original); err != nil {
		return handoff, nil, e.Wrap(e.CodeProvenanceMissing, err, "decoding original qualification Plan")
	}
	if err := original.Validate(); err != nil {
		return handoff, nil, err
	}
	if err := factory.AdmitQualificationHandoff(&original); err != nil {
		return handoff, nil, err
	}
	// Replay reads authored inputs only from the captured closure. Catalog-owned
	// pinned upstream Git validation remains required; this is not offline mode.
	compiled, err := factory.CompileSnapshot(ctx, handoff.Inputs, original.GeneratorVersion)
	if err != nil {
		return handoff, nil, err
	}
	want, err := compiled.Digest()
	if err != nil {
		return handoff, nil, err
	}
	got, err := original.Digest()
	if err != nil {
		return handoff, nil, err
	}
	if got != want {
		return handoff, nil, e.New(e.CodePipelineDrift, "qualification Plan differs from its exact captured inputs")
	}
	return handoff, compiled, nil
}
