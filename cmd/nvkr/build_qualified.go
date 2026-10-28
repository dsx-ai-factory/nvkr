// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/profile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
	"github.com/urfave/cli/v2"
)

type qualifiedLocalBuild struct {
	Report             *pipeline.Report
	Resolved           *buildgraph.Resolved
	Profile            *profile.BuilderProfile
	ProfileDigest      string
	ProfileDivergences []profile.Divergence
}

type authenticatedProducerProfile struct {
	Profile     profile.BuilderProfile
	Digest      string
	HostReceipt profile.HostPreflightReceipt
	Divergences []profile.Divergence
}

func authenticateProducerProfile(c *cli.Context) (*authenticatedProducerProfile, error) {
	verifier, err := resolveQualifiedDocumentVerifier(c, nil)
	if err != nil {
		return nil, err
	}
	offline := c.Bool("profile-offline")
	material := trust.SignatureMaterial{
		SignaturePath: c.String("profile-signature"), CertificatePath: c.String("profile-certificate"),
		BundlePath: c.String("profile-bundle"), TrustedRootPath: c.String("profile-trusted-root"),
	}
	if err := validateQualifiedMaterial("builder profile", material, offline); err != nil {
		return nil, err
	}
	storeFile, err := loadTrustStoreFile(c.Context, trustStorePath(c))
	if err != nil {
		return nil, err
	}
	policy, err := qualifiedRolePolicy(c, storeFile, trust.RoleBuilderProfile)
	if err != nil {
		return nil, err
	}
	policy, err = qualifiedPolicyWithTrustedRoot(policy,
		c.String("profile-trusted-root"), c.String("profile-trusted-root-digest"), offline)
	if err != nil {
		return nil, err
	}
	authenticator, err := trust.NewDocumentAuthenticator(verifier,
		trust.JSONSchema[profile.BuilderProfile](profile.SchemaVersion))
	if err != nil {
		return nil, err
	}
	receipt, err := authenticator.Authenticate(c.Context, trust.AuthenticateRequest{
		SourcePath: c.String("profile"), Signature: material, Offline: offline,
		Role: trust.RoleBuilderProfile, Policy: policy,
	})
	if err != nil {
		return nil, err
	}
	profileValue, ok := receipt.Decoded().(profile.BuilderProfile)
	if !ok {
		return nil, e.New(e.CodeInternal,
			"authenticated builder-profile decoder returned %T", receipt.Decoded())
	}
	if err := profileValue.Validate(); err != nil {
		return nil, err
	}
	digest, err := profileValue.Digest()
	if err != nil {
		return nil, err
	}
	hostReceipt, divergences, err := profileValue.VerifyHost(c.Context, profile.ObserveHost())
	if err != nil {
		return nil, err
	}
	return &authenticatedProducerProfile{
		Profile: profileValue, Digest: digest, HostReceipt: hostReceipt, Divergences: divergences,
	}, nil
}

// runQualifiedLocalBuild is the only local path allowed to feed attest/release or a
// Reproduced claim. Host preflight yields an opaque stage-one receipt; Local's
// production observer measures stage two and the report must retain that exact
// profile/node-bound qualification. --skip-preflight is never valid here.
func runQualifiedLocalBuild(c *cli.Context, sourceRoot string) (*qualifiedLocalBuild, error) {
	if err := rejectQualifiedPackaging(c); err != nil {
		return nil, err
	}
	if c.Bool("skip-preflight") {
		return nil, e.New(e.CodePreflightFailed,
			"--skip-preflight is limited to unqualified development builds")
	}
	var authenticatedProfile *authenticatedProducerProfile
	if strings.TrimSpace(c.String("profile")) != "" {
		var err error
		authenticatedProfile, err = authenticateProducerProfile(c)
		if err != nil {
			return nil, err
		}
	}
	resolution, err := resolveBuildResult(c)
	if err != nil {
		return nil, err
	}
	res := resolution.Resolved
	result := &qualifiedLocalBuild{Resolved: res}
	only := selectedComponents(c)
	options := pipeline.RunOptions{
		Ctx: c.Context, SourceRoot: sourceRoot, Only: only, Jobs: c.Int("jobs"),
		KeepGoing: c.Bool("keep-going"), Package: c.Bool("package"), Runtime: c.String("runtime"),
		Stdout: os.Stdout, Stderr: os.Stderr,
	}
	if buildHasSource(res) {
		if authenticatedProfile == nil {
			return result, e.New(e.CodeUntrustedProfile,
				"qualified from-source output requires a separately authenticated builder profile with runner, runtime, toolchain, and sandbox execution identities")
		}
		builderProfile := authenticatedProfile.Profile
		if err := builderProfile.RequireBuilders(buildgraph.RequiredBuilders(res)); err != nil {
			return nil, err
		}
		result.Profile = &builderProfile
		result.ProfileDigest = authenticatedProfile.Digest
		result.ProfileDivergences = authenticatedProfile.Divergences
		options.Qualification = &pipeline.QualifiedExecution{
			Profile: builderProfile, HostReceipt: authenticatedProfile.HostReceipt,
			Observer: pipeline.LocalExecutionObserver{},
		}
	} else if authenticatedProfile != nil {
		return result, e.New(e.CodeUntrustedProfile,
			"an all-prebuilt qualified build must remain profile-independent")
	}
	result.Report, err = pipeline.Local{}.Run(resolution.Graph, resolution.BuildDigest, options)
	if err != nil {
		if result.Report != nil {
			_ = result.Report.Close()
		}
		return result, err
	}
	if err := result.Report.RequireQualified(c.Context); err != nil {
		_ = result.Report.Close()
		return result, err
	}
	if result.Profile != nil && result.Report.QualificationProfileDigest() != result.ProfileDigest {
		_ = result.Report.Close()
		return result, e.New(e.CodePreflightFailed,
			"qualified report profile %s does not match selected profile %s",
			result.Report.QualificationProfileDigest(), result.ProfileDigest)
	}
	return result, nil
}

func rejectQualifiedPackaging(c *cli.Context) error {
	if c != nil && c.Bool("package") {
		return e.New(e.CodeEngineCapability,
			"qualified --package is unavailable until OCI/deb assembly streams from a held artifact capability into a crash-safe transaction")
	}
	return nil
}
