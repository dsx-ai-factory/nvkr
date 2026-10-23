// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
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
	output, err := selectedRunOutput(run, outputName, format)
	if err != nil {
		return nil, lifecycle.Artifact{}, err
	}
	return plan, output, nil
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

// Source selections are checked against the operation's inspected Plans before
// extraction. This step binds the verified helper bytes in the private view.
func prepareNativeIdentityAssets(ctx context.Context, assets *NativeAssets) error {
	if assets == nil {
		return errors.New("identity inputs require a prepared asset owner")
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
