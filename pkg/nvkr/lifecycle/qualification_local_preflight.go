// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"runtime"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// PreflightLocalQualification admits a prepared Plan and local signing inputs
// before producers execute. It issues no receipt, publishes nothing and cannot
// predict final payload capacity or network availability. Execution repeats all
// checks; this observation must never be used as durable authorization.
func PreflightLocalQualification(ctx context.Context, plan *factory.Plan, options LocalQualificationOptions) error {
	return preflightLocalQualification(ctx, plan, options, runtime.GOOS+"/"+runtime.GOARCH)
}

func preflightLocalQualification(ctx context.Context, plan *factory.Plan, options LocalQualificationOptions, platform string) error {
	if ctx == nil || plan == nil || options.PolicyPath == "" || options.Repository == "" || options.Scratch == "" || options.EvidenceOut == "" || options.CosignExecutable == "" || options.BuilderKeyPath == "" || options.SmokeKeyPath == "" || options.QualifierKeyPath == "" {
		return e.New(e.CodeUsage, "local qualification preflight requires Plan, policy, three keys, cosign, registry, scratch and evidence destination")
	}
	if runtime.GOOS != "linux" || !qualificationNativePlatform(platform) {
		return e.New(e.CodeEngineCapability, "local qualification preflight requires native Linux")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := plan.Validate(); err != nil {
		return err
	}
	policy, err := LoadLocalQualificationPolicy(ctx, options.PolicyPath)
	if err != nil {
		return err
	}
	if policy.Platform != platform {
		return e.New(e.CodeEngineCapability, "local qualification policy differs from native platform")
	}
	if err := policy.RequirePlan(plan); err != nil {
		return err
	}
	if err := options.Registry.Validate(); err != nil {
		return err
	}
	var disks []string
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == OutputDiskImage {
			disks = append(disks, output.Name)
		}
	}
	if err := validateLocalPublicationTargets(options.Repository, disks, options.Registry); err != nil {
		return err
	}
	if err := admitLocalEvidenceDestination(options.EvidenceOut); err != nil {
		return err
	}
	parent, _, err := securepath.OpenDirectory(options.Scratch)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "opening qualification scratch")
	}
	defer parent.Close()
	available, err := securepath.AvailableDiskBackedBytes(ctx, parent)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "observing qualification scratch")
	}
	if available < 1<<30 {
		return e.New(e.CodeEngineCapability, "local qualification needs at least 1 GiB preliminary scratch headroom")
	}
	keys, err := admitLocalSigningKeys(ctx, policy, options)
	clearLocalSigningKeys(keys)
	return err
}
