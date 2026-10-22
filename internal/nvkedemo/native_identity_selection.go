// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const nativeIdentityHelper = "nvkr-site-identity-helper"

// Lifecycle rederives the frozen Run inputs; delivery separately verifies bytes.
func nativeSelectedRunPlan(ctx context.Context, root, outputName, format string) (*factory.Plan, lifecycle.Artifact, error) {
	plan, run, err := lifecycle.InspectLocalRunInputs(ctx, root)
	if err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	for _, output := range run.Observed {
		if output.Name == outputName && output.Kind == format {
			return plan, output, nil
		}
	}
	return nil, lifecycle.Artifact{}, fmt.Errorf("completed Run omits %s output %q", format, outputName)
}

// This value is private and issued only from the recompiled tool/image selections.
// Its hashes bind extracted bytes, not a new release or build identity.
type nativeIdentityAssets struct{ helperSHA string }

func nativeIdentityComponent(plan *factory.Plan, output, buildDigest, component string) (catalog.Component, error) {
	var selected *catalog.Component
	for _, build := range plan.Builds {
		if build.Name != output {
			continue
		}
		if build.BuildDigest != buildDigest || build.Platform != "linux/arm64" {
			return catalog.Component{}, errors.New("identity input differs from the selected ARM64 output")
		}
		for _, item := range build.ResolvedComponents {
			if item.Name != component {
				continue
			}
			if selected != nil {
				return catalog.Component{}, errors.New("ambiguous identity source component")
			}
			c := item.Component
			selected = &c
		}
	}
	if selected == nil || !selected.FromSource || selected.Repo == "" || selected.Commit == "" || selected.TreeHash == "" || len(selected.Patches) != 0 {
		return catalog.Component{}, errors.New("identity input requires its unmodified source producer")
	}
	return *selected, nil
}

func nativeIdentitySources(tools, disk *factory.Plan, toolDigest, diskDigest, diskOutput string) error {
	if _, err := nativeIdentityComponent(tools, "site-startup-tools-arm64", toolDigest, nativeIdentityHelper); err != nil {
		return err
	}
	_, err := nativeIdentityComponent(disk, diskOutput, diskDigest, "nvke-spire-agent")
	if err != nil {
		return err
	}
	return nil
}

func prepareNativeIdentityAssets(ctx context.Context, cfg NativeAssetConfig, assets *NativeAssets) error {
	if assets == nil {
		return errors.New("identity inputs require a prepared asset owner")
	}
	diskOutput, err := selectedDiskOutput(cfg.Network)
	if err != nil {
		return err
	}
	diskRun := cfg.OSRun
	if diskRun == "" {
		diskRun = cfg.BootstrapRun
	}
	tools, toolOutput, err := nativeSelectedRunPlan(ctx, cfg.ToolsRun, "site-startup-tools-arm64", "executable")
	if err != nil {
		return err
	}
	disk, diskArtifact, err := nativeSelectedRunPlan(ctx, diskRun, diskOutput, "disk-image")
	if err != nil {
		return err
	}
	if err := nativeIdentitySources(tools, disk, toolOutput.BuildDigest, diskArtifact.BuildDigest, diskOutput); err != nil {
		return err
	}
	if err := verifyToolMembers(assets.SiteTools, []string{"nerdctl", nativeIdentityHelper}); err != nil {
		return err
	}
	helper, err := DigestFile(ctx, filepath.Join(assets.SiteTools, nativeIdentityHelper))
	if err != nil {
		return err
	}
	assets.identity = &nativeIdentityAssets{helperSHA: helper}
	return nil
}
