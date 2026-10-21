// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const nativeIdentityHelper = "nvkr-site-identity-helper"
const nativeSpireRendererSource = "nvke-spire-configure-source"

// Only this owner authenticates the captured source inputs against the completed
// Run. The caller's paths and an on-disk plan alone do not select a producer.
func nativeSelectedRunPlan(ctx context.Context, root, outputName, format string) (*factory.Plan, lifecycle.Artifact, error) {
	output, err := CompleteOutput(ctx, root, outputName, format)
	if err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	view, err := lifecycle.InspectRun(ctx, filepath.Join(root, "lifecycle.json"))
	if err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	if view.Status != lifecycle.RunComplete || view.CleanupIncomplete {
		return nil, lifecycle.Artifact{}, errors.New("native input requires a committed complete Run")
	}
	raw, err := readRunFile(ctx, root, ".runner/plan.json", factory.MaxPlanBytes)
	if err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	var original factory.Plan
	if err := canonical.UnmarshalExact(raw, &original); err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	raw, err = readRunFile(ctx, root, "compile-inputs.json", 64<<20)
	if err != nil {
		return nil, lifecycle.Artifact{}, fmt.Errorf("native input requires captured source inputs: %w", err)
	}
	var snapshot factory.InputSnapshot
	if err := canonical.UnmarshalExact(raw, &snapshot); err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	plan, err := factory.CompileSnapshot(ctx, snapshot, original.GeneratorVersion)
	if err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	digest, err := plan.Digest()
	if err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	if digest != view.PlanDigest {
		return nil, lifecycle.Artifact{}, errors.New("native source inputs differ from the completed Run")
	}
	return plan, output, nil
}

// This value is private and issued only from the recompiled tool/image selections.
// Its hashes bind extracted bytes, not a new release or build identity.
type nativeIdentityAssets struct{ helperSHA, rendererSHA string }

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
	renderer, err := nativeIdentityComponent(tools, "site-startup-tools-arm64", toolDigest, nativeSpireRendererSource)
	if err != nil {
		return err
	}
	if _, err := nativeIdentityComponent(tools, "site-startup-tools-arm64", toolDigest, nativeIdentityHelper); err != nil {
		return err
	}
	spire, err := nativeIdentityComponent(disk, diskOutput, diskDigest, "nvke-spire-agent")
	if err != nil {
		return err
	}
	if renderer.Repo != spire.Repo || renderer.Commit != spire.Commit || renderer.TreeHash != spire.TreeHash {
		return errors.New("site renderer source differs from the node image SPIRE package source")
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
	if err := verifyToolMembers(assets.SiteTools, []string{"nerdctl", nativeSpireRendererSource, nativeIdentityHelper}); err != nil {
		return err
	}
	helper, err := DigestFile(ctx, filepath.Join(assets.SiteTools, nativeIdentityHelper))
	if err != nil {
		return err
	}
	renderer, err := DigestFile(ctx, filepath.Join(assets.SiteTools, nativeSpireRendererSource))
	if err != nil {
		return err
	}
	assets.identity = &nativeIdentityAssets{helperSHA: helper, rendererSHA: renderer}
	return nil
}
