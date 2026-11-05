// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// Existing inspection profiles supply pinned tools and bounded expansion only;
// this projection does not claim disk runtime inspection or qualification.
func debianPublicationInspectionProfile(plan *Plan) (definition.DiskInspectionProfile, error) {
	profile, ok := plan.Environment.DiskInspectionProfiles[plan.Environment.UtilityPlatform]
	if !ok || plan.Definition.SBOMTool == "" {
		return definition.DiskInspectionProfile{}, e.New(e.CodeEngineCapability, "Debian dependency inspection requires its pinned package-reader/scanner profile")
	}
	if err := profile.Validate(plan.Environment.UtilityPlatform); err != nil {
		return definition.DiskInspectionProfile{}, err
	}
	if toolExecutableDigest(plan, plan.Definition.SBOMTool) != profile.ScannerDigest {
		return definition.DiskInspectionProfile{}, e.New(e.CodeVerifyFailed, "Debian profile changes selected scanner executable")
	}
	return profile, nil
}

func inspectDebianProducerSBOM(ctx context.Context, plan *Plan, payload Artifact, artifacts map[string]Artifact, scanner sbom.Scanner, bom *sbom.BOM) (result *sbom.BOM, returnErr error) {
	if !plan.debianPublicationTarget(payload.BuildTarget) {
		return nil, e.New(e.CodeVerifyFailed, "Debian scan requires exact selected published output")
	}
	var expected LifecycleOutputPlan
	for _, output := range plan.LifecycleOutputs {
		if output.Name == payload.BuildTarget && output.Kind == PackageDebian {
			expected = output
		}
	}
	if expected.ArtifactID != payload.ID {
		return nil, e.New(e.CodeVerifyFailed, "Debian scan substitutes original raw producer")
	}
	profile, err := debianPublicationInspectionProfile(plan)
	if err != nil {
		return nil, err
	}
	if scanner.Digest != profile.ScannerDigest || scanner.Path == "" {
		return nil, e.New(e.CodeVerifyFailed, "Debian scan changes selected scanner")
	}
	observed, err := ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(profile.TimeoutSeconds)*time.Second)
	defer cancel()
	subject, err := heldfile.OpenContext(ctx, payload.Path, heldfile.Options{Label: "original Debian scan input", ExpectedDigest: observed.Digest, MaxBytes: 1 << 30})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding original Debian package for scan")
	}
	defer func() {
		if err := subject.Close(); err != nil {
			returnErr = joinFactoryCleanupError(returnErr, err, "closing Debian scan input")
			result = nil
		}
	}()
	platform := ""
	for _, build := range plan.Builds {
		if build.Name == expected.Name && build.BuildDigest == expected.BuildDigest {
			platform = build.Platform
		}
	}
	receipt, err := sbom.InspectHeldDebianDependencies(ctx, subject, platform, scanner, profile.PackageReaderDigest, int64(profile.MaxFilesystemBytes))
	if err != nil {
		return nil, err
	}
	return sbom.WithDebianPayloadDependencies(bom, receipt)
}
