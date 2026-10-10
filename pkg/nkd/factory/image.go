// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func inspectFactoryImage(ctx context.Context, plan *Plan, artifact Artifact) (delivery.ImageInspection, error) {
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, declared := range plan.Graph.Artifacts {
		artifacts[declared.ID] = declared
	}
	node := planNode(plan, artifact.Producer)
	var platform string
	count := 0
	for _, id := range node.Inputs {
		input := artifacts[id]
		if input.Kind != "oci-image" {
			continue
		}
		count++
		for _, build := range plan.Builds {
			if build.Name == input.BuildTarget {
				platform = build.Platform
			}
		}
	}
	if count != 1 || platform == "" {
		return delivery.ImageInspection{}, e.New(e.CodeVerifyFailed, "image output %q has no unique build platform", artifact.Name)
	}
	return delivery.InspectContainerImage(ctx, artifact.Path, platform)
}

func executeImagePackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact, artifacts map[string]Artifact, stdout io.Writer) error {
	if len(inputs) != 1 || inputs[0].Kind != "oci-image" {
		return e.New(e.CodeInvalidCatalog, "container image package %q requires one OCI export", node.ID)
	}
	if planNode(plan, inputs[0].Producer).Kind == NodeAcquire {
		recordID := "result/" + inputs[0].BuildTarget + "/" + inputs[0].Component
		if !containsString(node.Inputs, recordID) {
			return e.New(e.CodeReleaseIncomplete, "acquired image package omits its exact producer result")
		}
		observed, err := authenticateAcquiredImagePayload(ctx, plan, inputs[0], artifacts)
		if err != nil {
			return err
		}
		image, err := delivery.ImportAcquiredContainerImage(ctx, inputs[0].Path, observed.SHA256, "linux/"+packageArch(plan, inputs), output.Path)
		if err == nil {
			fmt.Fprintf(stdout, "Packaged acquired container image %q without rewriting manifest %s\n", output.Name, image.ManifestDigest)
		}
		return err
	}
	digest, err := digestFactoryPath(ctx, inputs[0].Path)
	if err != nil {
		return err
	}
	image, err := delivery.ImportContainerImage(ctx, inputs[0].Path, digest, "linux/"+packageArch(plan, inputs), output.Path)
	if err == nil {
		fmt.Fprintf(stdout, "Packaged container image %q: manifest %s\n", output.Name, image.ManifestDigest)
	}
	return err
}

// factoryImagePayloadDigest selects a descriptor only after the owning image
// inspector has checked its exact concrete platform or frozen aggregate set.
func factoryImagePayloadDigest(ctx context.Context, plan *Plan, artifact Artifact) (string, error) {
	if planNode(plan, artifact.Producer).ImageIndex != nil {
		index, err := inspectFactoryImageIndex(ctx, plan, artifact)
		return index.Digest, err
	}
	image, err := inspectFactoryImage(ctx, plan, artifact)
	return image.ManifestDigest, err
}
