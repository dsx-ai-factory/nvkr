// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

// SuiteLocatorV3 selects transport separately from required-suite trust policy.
// Its name joins exactly one independently selected PolicyV3 requirement.
type SuiteLocatorV3 struct {
	Name     string                          `json:"name"`
	Locator  testobservation.Locator         `json:"locator"`
	Registry testobservation.RegistryOptions `json:"registry"`
}

// PublishFilesV3 refuses raw-file authority before I/O. Hosted native smoke
// completion requires the independently authenticated triggering Run.
func PublishFilesV3(ctx context.Context) (*PublicationV3, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	return nil, e.New(e.CodeEngineCapability, "file-based qualification cannot authenticate hosted native smoke; use PublishTriggeredRunV3")
}

const SuiteConfigurationSchemaV3 = "nvkr.dev/qualification-suite-configuration/v3"
const maxSuiteConfigurationV3Bytes = 1 << 20

// SuiteConfigurationV3 is authored consumer configuration, never evidence.
// Generate its canonical JSON through the existing codec. Nested registry and
// credential option fields retain their owning types' established names.
type SuiteConfigurationV3 struct {
	SchemaVersion string           `json:"schemaVersion"`
	Policy        PolicyV3         `json:"policy"`
	Suites        []SuiteLocatorV3 `json:"suites"`
}

func (c SuiteConfigurationV3) Validate() error {
	if c.SchemaVersion != SuiteConfigurationSchemaV3 {
		return e.New(e.CodeVerifyFailed, "unknown qualification suite configuration schema")
	}
	return validateSuiteConfigurationV3(c.Policy, c.Suites)
}
func validateSuiteConfigurationV3(policy PolicyV3, suites []SuiteLocatorV3) error {
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

// LoadSuiteConfigurationV3 holds one bounded regular input. It performs no tool,
// provider, registry or output operation and never reports credential contents.
func LoadSuiteConfigurationV3(ctx context.Context, path string) (SuiteConfigurationV3, error) {
	var configuration SuiteConfigurationV3
	if err := suiteContext(ctx); err != nil {
		return configuration, err
	}
	if path == "" {
		return configuration, e.New(e.CodeUsage, "suite configuration path required")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "qualification suite configuration", MaxBytes: maxSuiteConfigurationV3Bytes, RequirePrivateMode: true})
	if err != nil {
		return configuration, suiteConfigurationReadErrorV3(ctx, err, "holding suite configuration")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxSuiteConfigurationV3Bytes)
	if err != nil {
		return configuration, suiteConfigurationReadErrorV3(ctx, err, "reading suite configuration")
	}
	if err := canonical.UnmarshalExact(raw, &configuration); err != nil {
		return SuiteConfigurationV3{}, e.Wrap(e.CodeVerifyFailed, err, "decoding suite configuration")
	}
	if err := configuration.Validate(); err != nil {
		return SuiteConfigurationV3{}, err
	}
	if err := suiteContext(ctx); err != nil {
		return SuiteConfigurationV3{}, err
	}
	return configuration, nil
}

const maxRetainedSuiteBytesV3 = testobservation.MaxTotalBytes

type retainedSuiteObservationV3 interface {
	Observation(context.Context) (testobservation.Observation, error)
	Close() error
}

// Newly verified bytes count both the canonical control document and each exact
// covered payload. Rejection closes the excess capability immediately; accepted
// ownership remains with the orchestration's common cleanup.
func admitRetainedSuiteBytesV3(ctx context.Context, observation retainedSuiteObservationV3, remaining int64) (int64, error) {
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

func suiteConfigurationReadErrorV3(ctx context.Context, err error, operation string) error {
	if canceled := ctx.Err(); canceled != nil {
		return e.Wrap(e.CodeTimeout, canceled, "%s canceled", operation)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return e.Wrap(e.CodeVerifyFailed, err, "%s", operation)
}
