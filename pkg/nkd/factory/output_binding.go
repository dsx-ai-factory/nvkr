package factory

import (
	"reflect"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func lifecycleArtifactID(output authored.DefinitionOutput, components []catalog.NamedComponent) (string, error) {
	if output.Format != authored.OutputDiskImage {
		return "package/" + output.Name, nil
	}
	root, err := buildgraph.DiskRootName(components)
	if err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err,
			"disk-image output %q cannot derive its root producer", output.Name)
	}
	return "artifact/" + output.Name + "/" + root, nil
}

func (plan Plan) validateLifecyclePlans() error {
	if len(plan.LifecycleOutputs) == 0 {
		return nil
	}
	if plan.DefinitionDigest == "" || plan.EnvironmentDigest == "" || len(plan.LifecycleOutputs) == 0 {
		return e.New(e.CodeUnverifiedRef, "lifecycle plan bindings require exact Definition and Environment identities")
	}
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	builds := make(map[string]BuildPlan, len(plan.Builds))
	for _, build := range plan.Builds {
		builds[build.Name] = build
	}
	nodes := make(map[string]Node, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodes[node.ID] = node
	}
	previous := ""
	for i, output := range plan.LifecycleOutputs {
		artifact, ok := artifacts[output.ArtifactID]
		build, buildOK := builds[output.Name]
		producer := nodes[artifact.Producer]
		var actualComponents []string
		actualBuild := ""
		if output.Kind == authored.OutputDiskImage {
			// Plan.Validate has already re-resolved and recompiled this BuildPlan
			// from the held Definition and catalog snapshot. Use that authenticated
			// selected closure rather than reconstructing only the root's direct
			// inputs from the artifact graph; material graphs are transitive.
			actualComponents = make([]string, 0, len(build.ResolvedComponents))
			for _, component := range build.ResolvedComponents {
				actualComponents = append(actualComponents, component.Name)
			}
			actualBuild = artifact.BuildTarget
		} else {
			for _, inputID := range producer.Inputs {
				input := artifacts[inputID]
				if input.Kind == "producer-result" || input.BuildTarget == "" {
					continue
				}
				if input.Component != "" && !(output.Kind == "helm-oci" && input.Kind == "aicr-bundle") {
					actualComponents = append(actualComponents, input.Component)
				}
				if actualBuild == "" {
					actualBuild = input.BuildTarget
				} else if actualBuild != input.BuildTarget {
					actualBuild = "!mixed"
				}
			}
		}
		actualComponents = uniqueSorted(actualComponents)
		nameMatches := artifact.Name == output.Name ||
			(output.Kind == authored.OutputDiskImage && artifact.Name == artifact.Component)
		if !canonicalName.MatchString(output.Name) || output.Name <= previous || !ok || artifact.Kind != lifecyclePackageKind(output.Kind) ||
			!nameMatches || !buildOK || (output.Kind != "helm-oci" && len(output.Components) == 0) || !sort.StringsAreSorted(output.Components) ||
			build.BuildDigest != output.BuildDigest || build.Name != actualBuild || !equalFactoryStrings(actualComponents, output.Components) {
			return e.New(e.CodeUnverifiedRef, "lifecycle output %q is malformed or not bound to its graph build", output.Name)
		}
		authored := plan.Definition.Outputs[i]
		if build.Platform != authored.Platform || !equalFactoryStrings(build.Components, authored.Components) || build.AICRProfileName != aicrProfile(authored) {
			return e.New(e.CodeUnverifiedRef, "output %q producer selection differs from its definition", output.Name)
		}
		if authored.AICR != nil {
			bundle, err := aicr.NormalizeBundleInputs(authored.AICR.Bundle)
			if err != nil {
				return err
			}
			if build.Graph.AICR == nil || !reflect.DeepEqual(build.Graph.AICR.Bundle, bundle) || build.Graph.AICR.GeneratorDigest != toolExecutableDigest(&plan, authored.AICR.Generator) {
				return e.New(e.CodeUnverifiedRef, "output %q rendering settings differ from its definition", output.Name)
			}
		}
		previous = output.Name
	}
	return nil
}

func lifecyclePackageKind(kind string) string {
	switch kind {
	case "executable":
		return PackageBinaryBundle
	case "apt-repository":
		return PackageAPT
	case "helm-oci":
		return PackageHelm
	case "oci":
		return PackageImage
	case authored.OutputDiskImage:
		return authored.OutputDiskImage
	default:
		return ""
	}
}
