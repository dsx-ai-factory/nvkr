package factory

import (
	"context"

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
