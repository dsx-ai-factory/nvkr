// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
)

func projectQualificationDerived(plan *factory.Plan, publication delivery.PublicationDescription, output delivery.PublicationOutput) ([]byte, error) {
	var required *factory.LifecycleOutputPlan
	for i := range plan.LifecycleOutputs {
		if plan.LifecycleOutputs[i].Name == output.Name {
			required = &plan.LifecycleOutputs[i]
		}
	}
	if required == nil {
		return nil, e.New(e.CodeReleaseIncomplete, "qualified output lacks its original requirement")
	}
	if !reflect.DeepEqual(required.Derived, output.Derived) {
		return nil, e.New(e.CodeVerifyFailed, "derived member differs from the original Plan")
	}
	if output.Derived == nil {
		return nil, nil
	}
	if err := output.Derived.ValidateOutput(output.Name, output.Kind, output.BuildDigest, output.Components); err != nil {
		return nil, err
	}
	if err := output.Derived.ValidateMaterials(output.DerivedMaterials); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(output.Derived.Inputs))
	for i, input := range output.Derived.Inputs {
		matched := 0
		for _, sibling := range publication.Outputs {
			if sibling.Name != input.Name {
				continue
			}
			if sibling.Derived != nil || sibling.Kind != input.Kind || sibling.BuildDigest != input.BuildDigest || sibling.Digest != output.DerivedMaterials[i].Digest || sibling.Reference != output.Derived.RepositoryPrefix+"/"+input.Name+"@"+sibling.Digest {
				return nil, e.New(e.CodeVerifyFailed, "derived inventory sibling differs from publication-bound input")
			}
			matched++
		}
		if matched != 1 {
			return nil, e.New(e.CodeReleaseIncomplete, "derived inventory lacks exact sibling membership")
		}
		names = append(names, input.Name)
	}
	description, err := releaseview.Describe(publication, releaseview.Options{})
	if err != nil {
		return nil, err
	}
	inventory, err := releaseview.ArtifactInventory(description, names...)
	if err != nil {
		return nil, err
	}
	return inventory, nil
}

// Re-projection reads only the bounded inventory layer from the held verified
// closure. Descriptions never authorize reopening a caller-supplied pathname.
func checkQualificationDerived(ctx context.Context, plan *factory.Plan, publication delivery.PublicationDescription, output delivery.PublicationOutput, held qualificationPublication) error {
	inventory, err := projectQualificationDerived(plan, publication, output)
	if err != nil || output.Derived == nil {
		return err
	}
	return held.CompareDerivedInventory(ctx, output.Name, inventory)
}
