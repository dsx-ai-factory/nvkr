// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"runtime"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

// PublishTriggeredRunV3 preserves all native v2 gates and additionally consumes
// independent passive suite observations. It never runs a cluster suite.
// Generated v3 workflow activation is a separate, deferred capability.
func PublishTriggeredRunV3(ctx context.Context, policyPath, configurationPath string, options lifecycle.QualificationRuntimeOptions) (*PublicationV3, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "qualification signing requires Linux pinned cosign execution")
	}
	policy, err := lifecycle.LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return nil, err
	}
	if err := admitRunPolicy(policy); err != nil {
		return nil, err
	}
	configuration, err := LoadSuiteConfigurationV3(ctx, configurationPath)
	if err != nil {
		return nil, err
	}
	if err := admitTriggeredSuiteConfigurationV3(policy, configuration); err != nil {
		return nil, err
	}
	if err := CheckGeneratedPolicy(ctx, ".", policy); err != nil {
		return nil, err
	}
	pin, err := policy.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	publisher, err := NewPublisherV3(ctx, Options{CosignBinary: policy.Cosign.Path, CosignDigest: pin, Credential: options.Credential})
	if err != nil {
		return nil, err
	}
	run, diagnostic, err := authenticateTriggeredRun(ctx, policy, options)
	if err != nil {
		if diagnostic == nil {
			return nil, err
		}
		return &PublicationV3{ArtifactChecks: diagnostic.ArtifactChecks, Verdicts: diagnostic.Verdicts, PublishedSubjects: []string{}}, err
	}
	defer run.close()
	description, err := run.checks.Description()
	if err != nil {
		return nil, err
	}
	receipts := make([]*SuiteReceipt, 0, len(configuration.Suites))
	verified := make([]*testobservation.Verified, 0, len(configuration.Suites))
	defer func() {
		for _, receipt := range receipts {
			_ = receipt.Close()
		}
		for _, receipt := range verified {
			_ = receipt.Close()
		}
	}()
	requirements := map[string]SuiteRequirement{}
	for _, requirement := range configuration.Policy.RequiredSuites {
		requirements[requirement.Expected.Suite.Name] = requirement
	}
	var retainedBytes int64
	for _, selected := range configuration.Suites {
		requirement := requirements[selected.Name]
		observation, err := testobservation.VerifyLocator(ctx, selected.Locator, requirement.Expected, selected.Registry)
		if err != nil {
			return &PublicationV3{ArtifactChecks: &description, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
		}
		size, err := admitRetainedSuiteBytesV3(ctx, observation, maxRetainedSuiteBytesV3-retainedBytes)
		if err != nil {
			return &PublicationV3{ArtifactChecks: &description, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
		}
		retainedBytes += size
		verified = append(verified, observation)
		receipt, err := AuthenticateSuite(ctx, observation, run.checks, requirement)
		if err != nil {
			return &PublicationV3{ArtifactChecks: &description, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
		}
		receipts = append(receipts, receipt)
	}
	return publisher.Publish(ctx, run.checks, run.invocation, run.smoke, configuration.Policy, receipts)
}

func admitTriggeredSuiteConfigurationV3(native lifecycle.QualificationPolicy, configuration SuiteConfigurationV3) error {
	if err := admitRunPolicy(native); err != nil {
		return err
	}
	if err := configuration.Validate(); err != nil {
		return err
	}
	pin, err := native.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := matchNativePolicyV3(configuration.Policy, native, pin); err != nil {
		return err
	}
	for _, selected := range configuration.Suites {
		if selected.Registry.CosignBinary != native.Cosign.Path || selected.Registry.CosignDigest != pin {
			return e.New(e.CodeUnpinnedInput, "suite verifier differs from independent native qualification executable")
		}
	}
	return nil
}
