// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func executeAPTPackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact) error {
	packages := make([]delivery.DebianPackageInput, 0, len(inputs))
	staging := filepath.Join(filepath.Dir(output.Path), "."+output.Name+"-deb-inputs")
	if err := os.RemoveAll(staging); err != nil {
		return e.Wrap(e.CodeInternal, err, "resetting APT package staging")
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating APT package staging")
	}
	defer func() { _ = os.RemoveAll(staging) }()
	for _, input := range inputs {
		version, epoch, arch, err := packageInputMetadata(plan, input)
		if err != nil {
			return err
		}
		deb := filepath.Join(staging, fmt.Sprintf("%s_%s_%s.deb", input.Name, version, arch))
		// Factory execution is explicitly unqualified (TierStaticUnqualified). This deterministic
		// projection makes the planned APT dataflow executable without claiming a
		// descriptor-held qualified package receipt.
		//nolint:staticcheck
		if _, err := assemble.Deb(input.Path, input.Name, version, epoch, arch, deb); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "assembling Debian package for %s", input.ID)
		}
		packages = append(packages, delivery.DebianPackageInput{Path: deb})
	}
	_, err := delivery.BuildAPTRepository(ctx, output.Path, delivery.APTRequest{
		Distribution: node.Distribution, Component: node.PackageComponent, Packages: packages,
	})
	return err
}

func packageInputMetadata(plan *Plan, artifact Artifact) (string, int64, string, error) {
	for _, buildPlan := range plan.Builds {
		if buildPlan.Name != artifact.BuildTarget || buildPlan.Graph == nil {
			continue
		}
		for _, node := range buildPlan.Graph.Nodes {
			if node.Component != artifact.Component || (node.Kind != ir.KindBuild && node.Kind != ir.KindPull) {
				continue
			}
			version := node.Version
			if version == "" {
				version = "0"
			}
			epoch := int64(0)
			for _, env := range node.Env {
				if env.Name == "SOURCE_DATE_EPOCH" {
					epoch, _ = strconv.ParseInt(env.Value, 10, 64)
					break
				}
			}
			arch := strings.TrimPrefix(node.Platform, "linux/")
			if epoch <= 0 || (arch != "amd64" && arch != "arm64") {
				return "", 0, "", e.New(e.CodePipelineDrift,
					"artifact %s lacks pinned Debian version/epoch/platform metadata", artifact.ID)
			}
			return version, epoch, arch, nil
		}
	}
	return "", 0, "", e.New(e.CodePipelineDrift, "artifact %s has no producing build node", artifact.ID)
}
