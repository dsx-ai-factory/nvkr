// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

const derivedDependencyPrefix = "nvkr:derived/"

func derivedDependencyURI(output string, binding *derivedoutput.Binding, input string) string {
	return derivedDependencyPrefix + output + "/" + input + "?" + url.Values{"repository": {binding.RepositoryPrefix + "/" + input}}.Encode()
}

func derivedMaterialDependencies(output string, binding *derivedoutput.Binding, materials []derivedoutput.Material) ([]provenance.Dependency, error) {
	if binding == nil {
		if len(materials) != 0 {
			return nil, e.New(e.CodeVerifyFailed, "ordinary output carries derived dependencies")
		}
		return nil, nil
	}
	if err := binding.ValidateMaterials(materials); err != nil {
		return nil, err
	}
	dependencies := make([]provenance.Dependency, 0, len(materials))
	for _, material := range materials {
		dependencies = append(dependencies, provenance.Dependency{URI: derivedDependencyURI(output, binding, material.Name), Digest: map[string]string{"sha256": strings.TrimPrefix(material.Digest, "sha256:")}})
	}
	sort.Slice(dependencies, func(i, j int) bool { return dependencies[i].URI < dependencies[j].URI })
	return dependencies, nil
}

// Metadata describes the same subject the signing owner checked. Final consumer
// qualification separately joins these subjects to verified sibling payloads.
func publicationDerivedDependencies(inspection delivery.Inspection) ([]provenance.Dependency, error) {
	var result []provenance.Dependency
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			continue
		}
		dependencies, err := derivedMaterialDependencies(object.Name, object.Output.Derived, object.Output.DerivedMaterials)
		if err != nil {
			return nil, err
		}
		result = append(result, dependencies...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].URI < result[j].URI })
	return result, nil
}

// Admit runtime subjects only at exact compiler-declared dependency positions.
// This checks descriptions; signature and publication joins confer authority.
func declaredDerivedDependencies(plan *Plan, dependencies []provenance.Dependency) ([]provenance.Dependency, error) {
	allowed := map[string]string{}
	counts := map[string]int{}
	for _, output := range plan.LifecycleOutputs {
		if output.Derived == nil {
			continue
		}
		for _, input := range output.Derived.Inputs {
			allowed[derivedDependencyURI(output.Name, output.Derived, input.Name)] = output.Name
		}
		counts[output.Name] = len(output.Derived.Inputs)
	}
	var result []provenance.Dependency
	seen := map[string]bool{}
	observed := map[string]int{}
	for _, dependency := range dependencies {
		if !strings.HasPrefix(dependency.URI, derivedDependencyPrefix) {
			continue
		}
		owner, found := allowed[dependency.URI]
		if !found || seen[dependency.URI] || len(dependency.Digest) != 1 || !canonical.IsDigest("sha256:"+dependency.Digest["sha256"]) {
			return nil, e.New(e.CodeProvenanceMissing, "production predicate substitutes a derived dependency")
		}
		seen[dependency.URI] = true
		observed[owner]++
		result = append(result, dependency)
	}
	for owner, count := range observed {
		if count != counts[owner] {
			return nil, e.New(e.CodeProvenanceMissing, "production predicate omits a derived dependency")
		}
	}
	return result, nil
}

func validateDerivedPredicateMaterials(raw []byte, plan *Plan, output delivery.PublicationOutput) error {
	expected, err := derivedMaterialDependencies(output.Name, output.Derived, output.DerivedMaterials)
	if err != nil {
		return err
	}
	return validateDerivedDependencies(raw, plan, expected)
}

// ValidateQualificationRootMaterials joins the root predicate to the complete
// authenticated publication description. The caller must verify both signatures.
func ValidateQualificationRootMaterials(raw []byte, plan *Plan, publication delivery.PublicationDescription) error {
	var expected []provenance.Dependency
	for _, output := range publication.Outputs {
		materials, err := derivedMaterialDependencies(output.Name, output.Derived, output.DerivedMaterials)
		if err != nil {
			return err
		}
		expected = append(expected, materials...)
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i].URI < expected[j].URI })
	return validateDerivedDependencies(raw, plan, expected)
}

func validatePublicationDerivedMaterials(raw []byte, plan *Plan, inspection delivery.Inspection) error {
	expected, err := publicationDerivedDependencies(inspection)
	if err != nil {
		return err
	}
	return validateDerivedDependencies(raw, plan, expected)
}

func validateDerivedDependencies(raw []byte, plan *Plan, expected []provenance.Dependency) error {
	if plan == nil {
		return e.New(e.CodeProvenanceMissing, "derived provenance requires the exact Plan")
	}
	var predicate provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &predicate); err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "decoding derived output provenance")
	}
	actual, err := declaredDerivedDependencies(plan, predicate.BuildDefinition.ResolvedDependencies)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return e.New(e.CodeVerifyFailed, "production provenance differs from exact derived package materials")
	}
	return nil
}

func hasDerivedOutputs(plan *Plan) bool {
	for _, output := range plan.LifecycleOutputs {
		if output.Derived != nil {
			return true
		}
	}
	return false
}
