// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func allOutputRequirements(output authored.DefinitionOutput, definition authored.Definition, environment authored.Environment, builds map[string]BuildPlan) (LifecycleOutputPlan, error) {
	if output.Derived == nil {
		return outputRequirements(output, builds)
	}
	if environment.Publication == nil {
		return LifecycleOutputPlan{}, e.New(e.CodeEngineCapability, "derived output requires publication")
	}
	if err := definition.Validate(); err != nil {
		return LifecycleOutputPlan{}, err
	}
	binding := &derivedoutput.Binding{Encoding: output.Derived.Encoding, RepositoryPrefix: environment.Publication.RepositoryPrefix}
	for _, name := range output.Derived.Outputs {
		found := false
		for _, sibling := range definition.Outputs {
			if sibling.Name != name {
				continue
			}
			required, err := outputRequirements(sibling, builds)
			if err != nil {
				return LifecycleOutputPlan{}, err
			}
			binding.Inputs = append(binding.Inputs, derivedoutput.Input{Name: name, Kind: sibling.Format, BuildDigest: required.BuildDigest})
			found = true
		}
		if !found {
			return LifecycleOutputPlan{}, e.New(e.CodeReleaseIncomplete, "derived output omits named sibling %q", name)
		}
	}
	digest, err := binding.BuildDigest()
	if err != nil {
		return LifecycleOutputPlan{}, err
	}
	return LifecycleOutputPlan{Name: output.Name, Kind: output.Format, ArtifactID: "package/" + output.Name, BuildDigest: digest, Derived: binding}, nil
}

func validateDerivedPackageInputs(spec packageSpec, inputs []string, artifacts map[string]Artifact) error {
	if spec.Kind != PackageBundle || spec.ImageIndex != nil || spec.HelmImage != nil || len(inputs) != len(spec.Derived.Inputs) {
		return e.New(e.CodePipelineDrift, "derived inventory requires its exact sibling packages")
	}
	if err := spec.Derived.Validate(); err != nil {
		return err
	}
	for i, input := range spec.Derived.Inputs {
		id := "package/" + input.Name
		artifact, found := artifacts[id]
		if !found || inputs[i] != id || artifact.Name != input.Name || artifact.Kind != lifecyclePackageKind(input.Kind) {
			return e.New(e.CodePipelineDrift, "derived inventory has a substituted sibling package")
		}
	}
	return nil
}
