// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

// SuiteLocatorV2 selects transport separately from required-suite trust policy.
// Its name joins exactly one independently selected PolicyV2 requirement.
type SuiteLocatorV2 struct {
	Name     string                          `json:"name"`
	Locator  testobservation.Locator         `json:"locator"`
	Registry testobservation.RegistryOptions `json:"registry"`
}
type FilesOptionsV2 struct {
	ArtifactFiles         lifecycle.QualificationFilesOptions
	QualificationWorkflow string
	Signer                Options
	Policy                PolicyV2
	Suites                []SuiteLocatorV2
}

func admitFilesV2(options FilesOptionsV2) error {
	if err := validateSuiteConfigurationV2(options.Policy, options.Suites); err != nil {
		return err
	}
	if err := admitFiles(FilesOptions{ArtifactFiles: options.ArtifactFiles, QualificationWorkflow: options.QualificationWorkflow, Signer: options.Signer}); err != nil {
		return err
	}
	if err := options.ArtifactFiles.Consumer.Registry.Validate(); err != nil {
		return err
	}
	for _, credential := range []sign.Credential{options.ArtifactFiles.Consumer.Credential, options.Signer.Credential} {
		if _, err := sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.Signer.CosignBinary, BinaryDigest: options.Signer.CosignDigest, Identity: "^qualification-admission$", Issuer: "https://token.actions.githubusercontent.com", Credential: credential}); err != nil {
			return err
		}
	}
	if err := options.ArtifactFiles.Source.Validate(); err != nil {
		return err
	}
	qualifier := options.ArtifactFiles.Source
	qualifier.Workflow = options.QualificationWorkflow
	if err := qualifier.Validate(); err != nil {
		return err
	}
	for _, selected := range options.Suites {
		if selected.Registry.CosignBinary != options.Signer.CosignBinary || selected.Registry.CosignDigest != options.Signer.CosignDigest {
			return e.New(e.CodeUnpinnedInput, "suite verifier differs from the admitted qualification executable")
		}
	}

	return nil
}

// PublishFilesV2 authenticates the source/invocation, reuses the complete A gate,
// freshly verifies every exact suite locator and issues opaque suite receipts.
// It accepts no file-authored smoke verdicts; the missing M3-B issuer still blocks
// applicable tool outputs. All owned receipts are closed on every return path.
func PublishFilesV2(ctx context.Context, options FilesOptionsV2) (*PublicationV2, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	// Admit complete policy and configured transport before static tool or network work.
	if err := admitFilesV2(options); err != nil {
		return nil, err
	}
	publisher, err := NewPublisherV2(ctx, options.Signer)
	if err != nil {
		return nil, err
	}
	provider := qualificationorigin.NewGitHub(options.ArtifactFiles.GitHubToken)
	source, err := provider.AuthenticateSource(ctx, options.ArtifactFiles.Source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	invocation, err := provider.AuthenticateInvocation(ctx, source, options.QualificationWorkflow)
	if err != nil {
		return nil, err
	}
	defer func() { _ = invocation.Close() }()
	checks, report, err := lifecycle.CheckQualificationFiles(ctx, options.ArtifactFiles)
	if err != nil {
		if report == nil {
			return nil, err
		}
		return &PublicationV2{ArtifactChecks: report, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
	}
	defer func() { _ = checks.Close() }()
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	if err := invocation.RequireSource(description.Origin); err != nil {
		return nil, err
	}
	requirements := map[string]SuiteRequirement{}
	for _, r := range options.Policy.RequiredSuites {
		requirements[r.Expected.Suite.Name] = r
	}
	receipts := make([]*SuiteReceipt, 0, len(options.Suites))
	verified := make([]*testobservation.Verified, 0, len(options.Suites))
	defer func() {
		for _, r := range receipts {
			_ = r.Close()
		}
		for _, r := range verified {
			_ = r.Close()
		}
	}()
	var retainedBytes int64
	for _, selected := range options.Suites {
		requirement := requirements[selected.Name]
		observation, err := testobservation.VerifyLocator(ctx, selected.Locator, requirement.Expected, selected.Registry)
		if err != nil {
			return &PublicationV2{ArtifactChecks: &description, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
		}
		size, err := admitRetainedSuiteBytesV2(ctx, observation, maxRetainedSuiteBytesV2-retainedBytes)
		if err != nil {
			return &PublicationV2{ArtifactChecks: &description, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
		}
		retainedBytes += size
		verified = append(verified, observation)
		receipt, err := AuthenticateSuite(ctx, observation, checks, requirement)
		if err != nil {
			return &PublicationV2{ArtifactChecks: &description, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
		}
		receipts = append(receipts, receipt)
	}
	return publisher.Publish(ctx, checks, invocation, nil, options.Policy, receipts)
}

const SuiteConfigurationSchemaV2 = "nvkr.dev/qualification-suite-configuration/v2"
const maxSuiteConfigurationV2Bytes = 1 << 20

// SuiteConfigurationV2 is authored consumer configuration, never evidence.
// Generate its canonical JSON through the existing codec. Nested registry and
// credential option fields retain their owning types' established names.
type SuiteConfigurationV2 struct {
	SchemaVersion string           `json:"schemaVersion"`
	Policy        PolicyV2         `json:"policy"`
	Suites        []SuiteLocatorV2 `json:"suites"`
}

func (c SuiteConfigurationV2) Validate() error {
	if c.SchemaVersion != SuiteConfigurationSchemaV2 {
		return e.New(e.CodeVerifyFailed, "unknown qualification suite configuration schema")
	}
	return validateSuiteConfigurationV2(c.Policy, c.Suites)
}
func validateSuiteConfigurationV2(policy PolicyV2, suites []SuiteLocatorV2) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if len(suites) != len(policy.RequiredSuites) {
		return e.New(e.CodeGateUnmet, "required suite transport set is incomplete or contains extra members")
	}
	requirements := map[string]SuiteRequirement{}
	for _, r := range policy.RequiredSuites {
		requirements[r.Expected.Suite.Name] = r
	}
	seen := map[string]bool{}
	for _, selected := range suites {
		requirement, exists := requirements[selected.Name]
		if !exists || seen[selected.Name] {
			return e.New(e.CodeGateUnmet, "suite locator names must match required suite policy exactly once")
		}
		seen[selected.Name] = true
		if err := selected.Locator.Validate(); err != nil {
			return err
		}
		if selected.Locator.ObservationDigest != requirement.Expected.ObservationDigest {
			return e.New(e.CodeGateUnmet, "suite locator differs from independent observation expectation")
		}
		if selected.Registry.Transport.Anonymous && selected.Registry.Credential != (sign.Credential{}) {
			return e.New(e.CodeUsage, "anonymous suite transport rejects explicit cosign credentials")
		}
		if err := selected.Registry.ValidateReference(selected.Locator.Reference); err != nil {
			return err
		}
		if selected.Registry.Identity != requirement.Identity || selected.Registry.Issuer != requirement.Issuer {
			return e.New(e.CodeUntrustedSigner, "suite transport signer differs from independently selected suite policy")
		}
		if selected.Registry.CosignBinary == "" || !canonical.IsDigest(selected.Registry.CosignDigest) {
			return e.New(e.CodeUnpinnedInput, "suite authentication requires an exact executable pin")
		}

		// The signer constructor validates credential shape without reading files,
		// invoking a process or touching a registry. Actual preflight follows.
		if _, err := sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: selected.Registry.CosignBinary, BinaryDigest: selected.Registry.CosignDigest, Identity: selected.Registry.Identity, Issuer: selected.Registry.Issuer, Credential: selected.Registry.Credential}); err != nil {
			return err
		}
	}
	return nil
}

// LoadSuiteConfigurationV2 holds one bounded regular input. It performs no tool,
// provider, registry or output operation and never reports credential contents.
func LoadSuiteConfigurationV2(ctx context.Context, path string) (SuiteConfigurationV2, error) {
	var configuration SuiteConfigurationV2
	if err := suiteContext(ctx); err != nil {
		return configuration, err
	}
	if path == "" {
		return configuration, e.New(e.CodeUsage, "suite configuration path required")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "qualification suite configuration", MaxBytes: maxSuiteConfigurationV2Bytes})
	if err != nil {
		return configuration, e.Wrap(e.CodeVerifyFailed, err, "holding suite configuration")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxSuiteConfigurationV2Bytes)
	if err != nil {
		return configuration, e.Wrap(e.CodeVerifyFailed, err, "reading suite configuration")
	}
	if err := canonical.UnmarshalExact(raw, &configuration); err != nil {
		return SuiteConfigurationV2{}, e.Wrap(e.CodeVerifyFailed, err, "decoding suite configuration")
	}
	if err := configuration.Validate(); err != nil {
		return SuiteConfigurationV2{}, err
	}
	if err := suiteContext(ctx); err != nil {
		return SuiteConfigurationV2{}, err
	}
	return configuration, nil
}

const maxRetainedSuiteBytesV2 = testobservation.MaxTotalBytes

type retainedSuiteObservationV2 interface {
	Observation(context.Context) (testobservation.Observation, error)
	Close() error
}

// Newly verified bytes count both the canonical control document and each exact
// covered payload. Rejection closes the excess capability immediately; accepted
// ownership remains with the orchestration's common cleanup.
func admitRetainedSuiteBytesV2(ctx context.Context, observation retainedSuiteObservationV2, remaining int64) (int64, error) {
	accepted := false
	defer func() {
		if !accepted && observation != nil {
			_ = observation.Close()
		}
	}()
	if err := suiteContext(ctx); err != nil {
		return 0, err
	}
	if observation == nil {
		return 0, e.New(e.CodeGateUnmet, "authenticated suite observation is unavailable")
	}
	description, err := observation.Observation(ctx)
	if err != nil {
		return 0, err
	}
	raw, err := description.Canonical()
	if err != nil {
		return 0, err
	}
	size := int64(len(raw))
	for _, file := range description.Files {
		size += file.Size
	}
	if remaining < 0 || size > remaining {
		return 0, e.New(e.CodeVerifyFailed, "required suite set exceeds aggregate retained-byte budget")
	}
	if err := suiteContext(ctx); err != nil {
		return 0, err
	}
	accepted = true
	return size, nil
}
