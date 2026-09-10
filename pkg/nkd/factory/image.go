package factory

import (
	"context"

	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
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
		for _, recipe := range plan.Recipes {
			if recipe.Name == input.RecipeTarget {
				platform = "linux/" + recipe.Criteria.Arch
			}
		}
	}
	if count != 1 || platform == "" {
		return delivery.ImageInspection{}, e.New(e.CodeVerifyFailed, "image output %q has no unique recipe platform", artifact.Name)
	}
	return delivery.InspectContainerImage(ctx, artifact.Path, platform)
}
