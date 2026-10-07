// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// derivePublication creates package operations and one publication route from named
// outputs. These are executable dataflow, never another authored document model.
func derivePublication(definition authored.Definition, environment authored.Environment, builds []BuildPlan) ([]packageSpec, map[string]Target, []Route, error) {
	packages := make([]packageSpec, 0, len(definition.Outputs))
	closureInputs := make([]string, 0, len(definition.Outputs))
	for _, output := range definition.Outputs {
		if output.Format == authored.OutputDiskImage {
			// Local raw-output diagnostics retain their existing lifecycle subject.
			// Publication adds a distinct wrapper rather than changing that subject.
			if environment.Publication != nil {
				var root string
				for _, build := range builds {
					if build.Name == output.Name {
						var err error
						root, err = lifecycleArtifactID(output, build.ResolvedComponents)
						if err != nil {
							return nil, nil, nil, err
						}
					}
				}
				if root == "" {
					return nil, nil, nil, e.New(e.CodePipelineDrift, "disk output %q has no resolved raw subject", output.Name)
				}
				packages = append(packages, packageSpec{Name: output.Name, Kind: PackageDiskImage, Inputs: []string{root}})
				closureInputs = append(closureInputs, "package/"+output.Name)
			}
			continue
		}
		selectors := make([]string, 0, len(output.Components))
		for _, component := range output.Components {
			selectors = append(selectors, output.Name+"/"+component)
		}
		spec := packageSpec{Name: output.Name, Kind: PackageBinaryBundle, Inputs: selectors}
		switch output.Format {
		case authored.OutputOCI:
			spec.Kind = PackageImage
		case authored.OutputAPT:
			spec.Kind = PackageAPT
			spec.Distribution = output.APT.Distribution
			spec.Component = output.APT.Component
		case authored.OutputHelm:
			spec.Kind = PackageHelm
		}

		if output.Format == authored.OutputHelm {
			binding, err := resolveHelmBinding(output, builds)
			if err != nil {
				return nil, nil, nil, err
			}
			spec.HelmImage = binding
			if binding != nil {
				seen := map[string]bool{}
				for _, slot := range sortedKeys(binding.Images) {
					from := binding.Images[slot].From
					if !seen[from] {
						spec.Inputs = append(spec.Inputs, from)
						seen[from] = true
					}
				}
			}
		}
		packages = append(packages, spec)
		closureInputs = append(closureInputs, "package/"+output.Name)
	}
	if environment.Publication == nil {
		return packages, nil, nil, nil
	}
	for _, output := range definition.Outputs {
		if output.Name == "release" || output.Name == "releases" {
			return nil, nil, nil, e.New(e.CodeInvalidCatalog, "definition output %q collides with generated publication package", output.Name)
		}
	}
	publication := environment.Publication
	targets := map[string]Target{"release": {Kind: TargetOCI, Ref: publication.RepositoryPrefix + "/releases", CredentialRef: publication.CredentialRef, Anonymous: publication.Anonymous, PlainHTTP: publication.PlainHTTP}}
	routes := []Route{{Name: "release", Input: "package/release", Target: "release"}}
	packages = append(packages, packageSpec{Name: "release", Kind: PackageOCI, Inputs: closureInputs})
	return deriveOutputPublication(definition, environment, packages, targets, routes)
}

func packageProducerResults(inputs []string, nodes map[string]Node, artifacts map[string]Artifact) []string {
	var results []string
	pending := append([]string(nil), inputs...)
	seen := map[string]bool{}
	for len(pending) > 0 {
		inputID := pending[0]
		pending = pending[1:]
		if seen[inputID] {
			continue
		}
		seen[inputID] = true
		input := artifacts[inputID]
		switch input.Kind {
		case PackageBinaryBundle, PackageAPT, PackageHelm, PackageImage, PackageDiskImage:
		default:
			continue
		}
		producer := nodes[input.Producer]
		for _, memberID := range producer.Inputs {
			member := artifacts[memberID]
			if member.Kind == string(TierTerminalObservation) {
				continue
			}
			// A bound chart must reopen its selected image when independently
			// reproducing the chart. Transport that package and its producer
			// records; it remains a dependency, not another published output.
			if producer.HelmImage != nil && member.Kind == PackageImage {
				results = append(results, memberID)
				pending = append(pending, memberID)
			}
			if member.BuildTarget != "" {
				results = append(results, memberID)
			}
			if member.BuildTarget == "" || member.Component == "" || member.Kind == "producer-result" {
				continue
			}
			for resultID, candidate := range artifacts {
				if candidate.Kind == "producer-result" && candidate.BuildTarget == member.BuildTarget && candidate.Component == member.Component {
					results = append(results, resultID)
				}
			}
		}
	}
	return uniqueSorted(results)
}

// Publication carries the complete evidence set for each contributing build.
// Explicit edges make SBOM generation precede packaging and signing on every engine.
func publicationSBOMInputs(inputs []string, nodes map[string]Node, artifacts map[string]Artifact) []string {
	var result []string
	for _, id := range inputs {
		artifact := artifacts[id]
		if artifact.Kind != "producer-result" {
			continue
		}
		set := "sbom/" + artifact.BuildTarget
		result = append(result, set)
		for _, input := range nodes[artifacts[set].Producer].Inputs {
			if artifacts[input].Kind == "producer-result" {
				result = append(result, input)
			}
		}
	}
	return uniqueSorted(result)
}

// orderPackageSpecs derives execution order from package artifact inputs. Authored
// order and lexical names have no scheduling semantics; ready nodes are selected
// lexically only to make the topological result canonical.
func orderPackageSpecs(authored []packageSpec) ([]packageSpec, error) {
	byName := make(map[string]packageSpec, len(authored))
	dependencies := make(map[string]map[string]bool, len(authored))
	for _, spec := range authored {
		byName[spec.Name] = spec
		dependencies[spec.Name] = map[string]bool{}
	}
	for _, spec := range authored {
		for _, selector := range spec.Inputs {
			if !strings.HasPrefix(selector, "package/") || strings.HasSuffix(selector, "/*") {
				continue
			}
			dependency := strings.TrimPrefix(selector, "package/")
			if _, ok := byName[dependency]; !ok {
				return nil, e.New(e.CodeInvalidCatalog,
					"factory package %q references unknown package artifact %q", spec.Name, selector)
			}
			dependencies[spec.Name][dependency] = true
		}
	}
	ordered := make([]packageSpec, 0, len(authored))
	emitted := map[string]bool{}
	for len(ordered) < len(authored) {
		var ready []string
		for name := range byName {
			if emitted[name] {
				continue
			}
			allReady := true
			for dependency := range dependencies[name] {
				if !emitted[dependency] {
					allReady = false
					break
				}
			}
			if allReady {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			return nil, e.New(e.CodeInvalidCatalog, "factory package artifact graph contains a cycle")
		}
		sort.Strings(ready)
		for _, name := range ready {
			emitted[name] = true
			ordered = append(ordered, byName[name])
		}
	}
	return ordered, nil
}

func expandSelectors(selectors []string, artifacts map[string]Artifact) ([]string, error) {
	var expanded []string
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if strings.HasSuffix(selector, "/*") && strings.Count(selector, "*") == 1 {
			prefix := "artifact/" + strings.TrimSuffix(selector, "*")
			for id := range artifacts {
				if strings.HasPrefix(id, prefix) {
					expanded = append(expanded, id)
				}
			}
			continue
		}
		id := selector
		if !strings.HasPrefix(id, "artifact/") && !strings.HasPrefix(id, "package/") &&
			!strings.HasPrefix(id, "tool/") {
			id = "artifact/" + id
		}
		if _, ok := artifacts[id]; !ok {
			return nil, fmt.Errorf("selector %q matches no artifact", selector)
		}
		expanded = append(expanded, id)
	}
	expanded = uniqueSorted(expanded)
	if len(expanded) == 0 {
		return nil, fmt.Errorf("selectors match no artifacts")
	}
	return expanded, nil
}

func observationInputs(inputs []string, artifacts map[string]Artifact) []string {
	seen := map[string]bool{}
	for _, input := range inputs {
		target := artifacts[input].BuildTarget
		if target != "" {
			seen["observation/"+target] = true
		}
	}
	return sortedKeys(seen)
}

func validatePackageInputs(spec packageSpec, inputs []string, artifacts map[string]Artifact) error {
	if spec.HelmImage != nil {
		if spec.Kind != PackageHelm {
			return e.New(e.CodeInvalidCatalog, "image bindings require Helm packaging")
		}
		h := spec.HelmImage
		if err := h.ValidateBindings(); err != nil {
			return err
		}
		expected := map[string]bool{}
		for _, b := range h.Images {
			expected[b.From] = true
		}
		if len(inputs) != len(expected)+1 {
			return e.New(e.CodeInvalidCatalog, "Helm binding requires one source and the exact image packages")
		}
		source := false
		seen := map[string]bool{}
		for _, id := range inputs {
			if seen[id] {
				return e.New(e.CodeInvalidCatalog, "duplicate Helm input %q", id)
			}
			seen[id] = true
			a := artifacts[id]
			if expected[id] && a.Kind == PackageImage {
				delete(expected, id)
			} else if a.Kind == "tarball" && a.BuildTarget != "" && !source {
				source = true
			} else {
				return e.New(e.CodeInvalidCatalog, "Helm binding has unexpected input %q", id)
			}
		}
		if !source || len(expected) != 0 {
			return e.New(e.CodeInvalidCatalog, "Helm binding omits a source or image")
		}

		return nil
	}
	if spec.Kind == PackageHelm && len(inputs) != 1 {
		return e.New(e.CodeInvalidCatalog, "Helm package %q requires exactly one tree or source archive", spec.Name)
	}
	if spec.Kind == PackageImage && len(inputs) != 1 {
		return e.New(e.CodeInvalidCatalog, "container image package %q requires exactly one OCI export", spec.Name)
	}
	if spec.Kind == PackageDiskImage && len(inputs) != 1 {
		return e.New(e.CodeInvalidCatalog, "disk package %q requires exactly one raw disk subject", spec.Name)
	}
	for _, id := range inputs {
		artifact := artifacts[id]
		switch spec.Kind {
		case PackageDiskImage:
			if artifact.Kind != PackageDiskImage || artifact.BuildTarget != spec.Name || artifact.Component == "" {
				return e.New(e.CodeInvalidCatalog, "disk package %q requires its exact raw producer subject", spec.Name)
			}
		case PackageImage:
			if artifact.Kind != "oci-image" || artifact.BuildTarget == "" {
				return e.New(e.CodeInvalidCatalog, "container image package %q requires a build OCI image export", spec.Name)
			}
		case PackageAPT:
			if artifact.Kind != "host-package" {
				return e.New(e.CodeInvalidCatalog, "APT package %q input %s has kind %q, want host-package", spec.Name, id, artifact.Kind)
			}
		case PackageHelm:
			if (artifact.Kind != "tarball" && artifact.Kind != "raw") || artifact.BuildTarget == "" {
				return e.New(e.CodeInvalidCatalog, "Helm package %q input %s requires a source archive or acquired chart", spec.Name, id)
			}
		}
	}
	return nil
}

func validateRouteCompatibility(route Route, target Target, artifact Artifact) error {
	switch target.Kind {
	case TargetAPT:
		return e.New(e.CodeEngineCapability,
			"delivery route %q requires an APT server publication adapter; transport the apt-repository package or enclose it in an OCI root", route.Name)
	case TargetObject:
		return e.New(e.CodeEngineCapability,
			"delivery route %q requires an object-store publication adapter that is not implemented", route.Name)
	case TargetOCI:
		switch artifact.Kind {
		case PackageBinaryBundle, PackageOCI, PackageHelm, PackageImage, PackageDiskImage, "oci-image":
		default:
			return e.New(e.CodeInvalidCatalog, "delivery route %q sends unsupported %q to OCI target", route.Name, artifact.Kind)
		}
	}
	return nil
}

func packageMediaType(kind string) string {
	switch kind {
	case PackageImage:
		return "application/vnd.oci.image.manifest.v1+json"
	case PackageDiskImage:
		return delivery.MediaDiskImage
	case PackageBinaryBundle:
		return "application/vnd.nkd.binary-bundle.v1"
	case PackageAPT:
		return "application/vnd.nkd.apt-repository.v1"
	case PackageHelm:
		return "application/vnd.nkd.helm-bundle.v1"
	default:
		return delivery.MediaPublicationRoot
	}
}

func artifactMediaType(kind string) string {
	switch kind {
	case "host-package":
		return "application/vnd.debian.binary-package"
	case "oci-image":
		return "application/vnd.oci.image.manifest.v1+json"
	case "tarball":
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}

func (plan Plan) validatePublicationEvidence() error {
	evidenceNodes, evidenceArtifacts := planIndex(&plan)
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodePackage || node.PackageKind != PackageOCI {
			continue
		}
		for _, required := range publicationSBOMInputs(packageProducerResults(node.Inputs, evidenceNodes, evidenceArtifacts), evidenceNodes, evidenceArtifacts) {
			if !containsString(node.Inputs, required) {
				return e.New(e.CodeProvenanceMissing, "publication %q omits required SBOM evidence %q", node.ID, required)
			}
		}
	}
	return nil
}

// resolveHelmBinding joins Definition relationships with this output's resolved
// producer. Per-output catalog overrides are already frozen in BuildPlan.
func resolveHelmBinding(output authored.DefinitionOutput, builds []BuildPlan) (*HelmImageBinding, error) {
	var mapping *catalog.HelmPackaging
	for _, build := range builds {
		if build.Name != output.Name {
			continue
		}
		for _, c := range build.ResolvedComponents {
			if len(output.Components) == 1 && c.Name == output.Components[0] {
				mapping = c.Helm
			}
		}
	}
	if mapping == nil && output.Helm == nil {
		return nil, nil
	}
	if mapping == nil || output.Helm == nil || len(mapping.Images) != len(output.Helm.Images) {
		return nil, e.New(e.CodeInvalidCatalog, "Helm output %q must bind every catalog image slot exactly once", output.Name)
	}
	if err := mapping.Validate(); err != nil {
		return nil, err
	}
	binding := &HelmImageBinding{Chart: mapping.Chart, Images: map[string]HelmImage{}}
	for _, slot := range sortedKeys(output.Helm.Images) {
		image, ok := mapping.Images[slot]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "Helm output %q references unknown catalog image slot %q", output.Name, slot)
		}
		binding.Images[slot] = HelmImage{From: "package/" + output.Helm.Images[slot], Values: image.Values}
	}
	return binding, nil
}

func (h HelmImageBinding) ValidateBindings() error {
	mapping := catalog.HelmPackaging{Chart: h.Chart, Images: map[string]catalog.HelmImage{}}
	for _, slot := range sortedKeys(h.Images) {
		image := h.Images[slot]
		if image.From == "" {
			return e.New(e.CodeInvalidCatalog, "Helm image slot %q omits its dependency", slot)
		}
		mapping.Images[slot] = catalog.HelmImage{Values: image.Values}
	}
	return mapping.Validate()
}
