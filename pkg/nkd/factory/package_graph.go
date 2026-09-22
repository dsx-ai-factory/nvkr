package factory

import (
	"fmt"
	"sort"
	"strings"

	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// derivePublication creates package operations and one publication route from named
// outputs. These are executable dataflow, never another authored document model.
func derivePublication(definition authored.Definition, environment authored.Environment) ([]packageSpec, map[string]Target, []Route, error) {
	if environment.Publication != nil {
		for _, output := range definition.Outputs {
			if output.Format == authored.OutputDiskImage {
				return nil, nil, nil, e.New(e.CodeEngineCapability,
					"disk-image output %q has no admitted publication adapter", output.Name)
			}
		}
	}
	packages := make([]packageSpec, 0, len(definition.Outputs))
	closureInputs := make([]string, 0, len(definition.Outputs))
	for _, output := range definition.Outputs {
		if output.Format == authored.OutputDiskImage {
			// The produced QCOW2 is the lifecycle payload. It is not repackaged as
			// an ordinary binary bundle; disk observation/publication remain gated.
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
		if output.Helm != nil {
			image := "package/" + output.Helm.ImageOutput
			spec.HelmImage = &HelmImageBinding{Image: image, Chart: output.Helm.Chart}
			spec.Inputs = append(spec.Inputs, image)
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
	return packages, targets, routes, nil
}

func packageProducerResults(inputs []string, nodes map[string]Node, artifacts map[string]Artifact) []string {
	var results []string
	for _, inputID := range inputs {
		input := artifacts[inputID]
		switch input.Kind {
		case PackageBinaryBundle, PackageAPT, PackageHelm, PackageImage:
		default:
			continue
		}
		producer := nodes[input.Producer]
		for _, memberID := range producer.Inputs {
			member := artifacts[memberID]
			if member.Kind == string(TierTerminalObservation) {
				continue
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
		if spec.Kind != PackageHelm || len(inputs) != 2 || !canonicalName.MatchString(spec.HelmImage.Chart) {
			return e.New(e.CodeInvalidCatalog, "Helm image binding requires a source archive and one image package")
		}
		image, source := false, false
		for _, id := range inputs {
			artifact := artifacts[id]
			if id == spec.HelmImage.Image && artifact.Kind == PackageImage {
				image = true
			} else if artifact.Kind == "tarball" && artifact.BuildTarget != "" {
				source = true
			}
		}
		if !image || !source {
			return e.New(e.CodeInvalidCatalog, "Helm image binding does not select exact typed inputs")
		}
		return nil
	}
	if spec.Kind == PackageHelm && len(inputs) != 1 {
		return e.New(e.CodeInvalidCatalog, "Helm package %q requires exactly one tree or source archive", spec.Name)
	}
	if spec.Kind == PackageImage && len(inputs) != 1 {
		return e.New(e.CodeInvalidCatalog, "container image package %q requires exactly one OCI export", spec.Name)
	}
	for _, id := range inputs {
		artifact := artifacts[id]
		switch spec.Kind {
		case PackageImage:
			if artifact.Kind != "oci-image" || artifact.BuildTarget == "" {
				return e.New(e.CodeInvalidCatalog, "container image package %q requires a build OCI image export", spec.Name)
			}
		case PackageAPT:
			if artifact.Kind != "host-package" {
				return e.New(e.CodeInvalidCatalog, "APT package %q input %s has kind %q, want host-package", spec.Name, id, artifact.Kind)
			}
		case PackageHelm:
			if artifact.Kind != "tarball" || artifact.BuildTarget == "" {
				return e.New(e.CodeInvalidCatalog, "Helm package %q input %s requires a build source archive", spec.Name, id)
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
		case PackageBinaryBundle, PackageOCI, PackageHelm, PackageImage, "oci-image":
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
	case PackageBinaryBundle:
		return "application/vnd.nkd.binary-bundle.v1"
	case PackageAPT:
		return "application/vnd.nkd.apt-repository.v1"
	case PackageHelm:
		return "application/vnd.nkd.helm-bundle.v1"
	default:
		return "application/vnd.nkd.publication-root.v3"
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
