package factory

import (
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func admitInputs(definition authored.Definition, environment authored.Environment) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if err := environment.Validate(); err != nil {
		return err
	}
	for _, output := range definition.Outputs {
		if output.Format == authored.OutputDiskImage {
			return e.New(e.CodeEngineCapability, "disk-image output %q is declared but execution is not implemented", output.Name)
		}
	}
	if environment.Signing != nil && environment.Signing.Mode == SigningModeCosign && definition.CosignTool == "" {
		return e.New(e.CodeTrustPolicyMissing, "cosign signing requires Definition.cosignTool")
	}
	return nil
}

func aicrProfile(output authored.DefinitionOutput) string {
	if output.AICR != nil {
		return output.AICR.Profile
	}
	return ""
}
func aicrGenerator(output authored.DefinitionOutput) string {
	if output.AICR != nil {
		return output.AICR.Generator
	}
	return ""
}
func aicrBundle(output authored.DefinitionOutput) *aicr.BundleInputs {
	if output.AICR != nil {
		return output.AICR.Bundle
	}
	return nil
}

// Run-wide tools use the Definition catalog; each generator uses its output's
// catalog. One tool name identifies one acquisition, so all selected pins must agree.
func selectedTools(definition authored.Definition, environment authored.Environment, source *catalog.Catalog,
	outputCatalogs map[string]*catalog.Catalog) (map[string]catalog.ToolPin, error) {
	if source == nil {
		return nil, e.New(e.CodeInvalidCatalog, "tool selection requires a resolved catalog")
	}
	names := []string{}
	if definition.SBOMTool != "" {
		names = append(names, definition.SBOMTool)
	}
	if environment.Signing != nil && environment.Signing.Mode == SigningModeCosign {
		names = append(names, definition.CosignTool)
	}
	tools := make(map[string]catalog.ToolPin, len(names))
	selectTool := func(name string, selected *catalog.Catalog) error {
		pin, ok := selected.Tools[name]
		if !ok {
			return e.New(e.CodeUnpinnedSource, "selected catalog tool %q has no pin", name)
		}
		if pin.Platform != environment.UtilityPlatform {
			return e.New(e.CodeEngineCapability, "tool %q platform %s does not match utility platform %s", name, pin.Platform, environment.UtilityPlatform)
		}
		if previous, exists := tools[name]; exists && previous != pin {
			return e.New(e.CodeUnpinnedSource, "selected catalog tool %q has conflicting pins", name)
		}
		tools[name] = pin
		return nil
	}
	for _, name := range uniqueSorted(names) {
		if err := selectTool(name, source); err != nil {
			return nil, err
		}
	}
	for _, output := range definition.Outputs {
		if output.AICR == nil {
			continue
		}
		selected := source
		if output.Catalog != nil {
			selected = outputCatalogs[output.Name]
			if selected == nil {
				return nil, e.New(e.CodeInvalidCatalog, "output %q has no resolved catalog override", output.Name)
			}
		}
		if err := selectTool(output.AICR.Generator, selected); err != nil {
			return nil, err
		}
	}
	return tools, nil
}

func compileToolOperation(name string, pin catalog.ToolPin, runsOn string) (Node, Artifact) {
	id := "tool/" + name
	node := Node{ID: id, Kind: NodeAcquire, RunsOn: runsOn,
		Network: ir.NetworkDefault, URL: pin.URL, Digest: pin.ArchiveDigest,
		Executable: pin.ExecutablePath, ExecutableDigest: pin.ExecutableDigest, ToolFormat: pin.Format,
		Outputs: []string{id}}
	artifact := Artifact{ID: id, Name: name, Kind: "pipeline-tool",
		MediaType: "application/vnd.nkd.pipeline-tool.v1", Producer: id,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "tools", name, pin.ExecutablePath))}
	return node, artifact
}

// derivePublication creates package operations and one publication route from named
// outputs. These are executable dataflow, never another authored document model.
func derivePublication(definition authored.Definition, environment authored.Environment) ([]packageSpec, map[string]Target, []Route, error) {
	packages := make([]packageSpec, 0, len(definition.Outputs)+1)
	closureInputs := make([]string, 0, len(definition.Outputs))
	for _, output := range definition.Outputs {
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
			if output.AICR != nil {
				spec.Inputs = []string{output.Name + "/aicr-bundle"}
			}
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
