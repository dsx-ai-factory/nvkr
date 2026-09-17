package lifecycle

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/cluster"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

// CompileFiles authenticates the public authored documents, derives routine
// packages and routes, and delegates graph construction to the factory compiler.
func CompileFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion string) (*factory.Plan, error) {
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	return Compile(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
}

// Compile lowers the v2 public model into the existing internal compiler model.
// The lowering owns package and route derivation; callers never assemble a plan.
func Compile(ctx context.Context, definitionPath string, definition Definition,
	environmentPath string, environment Environment, generatorVersion string,
) (*factory.Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "definition compilation requires a context")
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	if err := environment.Validate(); err != nil {
		return nil, err
	}
	for _, output := range definition.Outputs {
		if output.Format == OutputDiskImage {
			return nil, e.New(e.CodeEngineCapability,
				"disk-image output %q is declared but execution is not implemented", output.Name)
		}
	}
	compileSpec, delivery, err := lower(definition, environment)
	if err != nil {
		return nil, err
	}
	plan, err := factory.CompileDocuments(ctx, definitionPath, compileSpec, environmentPath, delivery, generatorVersion)
	if err != nil {
		return nil, err
	}
	definitionDigest, err := definition.Digest()
	if err != nil {
		return nil, err
	}
	environmentDigest, err := environment.Digest()
	if err != nil {
		return nil, err
	}
	plan.DefinitionDigest = definitionDigest
	plan.EnvironmentDigest = environmentDigest
	for _, output := range definition.Outputs {
		var recipeDigest string
		for _, recipe := range plan.Recipes {
			if recipe.Name == output.Recipe {
				recipeDigest = recipe.RecipeDigest
				break
			}
		}
		plan.LifecycleOutputs = append(plan.LifecycleOutputs, factory.LifecycleOutputPlan{Name: output.Name, Kind: output.Format,
			ArtifactID: "package/" + output.Name, RecipeDigest: recipeDigest,
			Components: append([]string(nil), output.Components...)})
	}
	for _, view := range definition.Clusters {
		var compositionDigest string
		for _, composition := range plan.Compositions {
			if composition.Name == view.Name {
				compositionDigest = composition.Digest
				break
			}
		}
		bound := factory.LifecycleClusterPlan{Name: view.Name, CompositionDigest: compositionDigest}
		for _, member := range view.Members {
			bound.Members = append(bound.Members, factory.LifecycleClusterMember{Output: member.Output, Role: member.Role})
		}
		plan.LifecycleClusters = append(plan.LifecycleClusters, bound)
	}
	plan.DefinitionPath = filepath.ToSlash(filepath.Clean(definitionPath))
	plan.EnvironmentPath = filepath.ToSlash(filepath.Clean(environmentPath))
	if err := factory.BindEvidenceAttestation(plan); err != nil {
		return nil, err
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

func lower(definition Definition, environment Environment) (factory.CompileSpec, factory.ExecutionSettings, error) {
	compileSpec := factory.CompileSpec{
		APIVersion: factory.CompileSpecAPIVersion, Kind: factory.CompileSpecKind,
		Metadata:         factory.Metadata{Name: definition.Metadata.Name, Description: definition.Metadata.Description},
		UtilityContainer: environment.UtilityContainer, UtilityPlatform: environment.UtilityPlatform,
		Catalog: factory.CatalogStack{Base: definition.Catalog.Base, Overlays: append([]string(nil), definition.Catalog.Overlays...)},
	}
	if environment.Signing != nil && environment.Signing.Mode == "cosign" && definition.CosignTool == "" {
		return factory.CompileSpec{}, factory.ExecutionSettings{}, e.New(e.CodeTrustPolicyMissing, "cosign signing requires Definition.cosignTool")
	}
	tools := []string{}
	if definition.SBOMTool != "" {
		tools = append(tools, definition.SBOMTool)
	}
	if environment.Signing != nil && environment.Signing.Mode == "cosign" {
		tools = append(tools, definition.CosignTool)
	}
	for _, selection := range definition.Recipes {
		if selection.AICRGenerator != "" {
			tools = append(tools, selection.AICRGenerator)
		}
	}
	compileSpec.SelectedTools = uniqueSorted(tools)
	outputComponents := make(map[string][]string, len(definition.Recipes))
	for _, output := range definition.Outputs {
		outputComponents[output.Recipe] = append(outputComponents[output.Recipe], output.Components...)
	}
	for _, recipe := range definition.Recipes {
		target := factory.RecipeTarget{Name: recipe.Name, Criteria: recipe.Criteria,
			Reserved: cloneMap(recipe.Parameters), AICRGenerator: recipe.AICRGenerator,
			LifecycleComponents: uniqueSorted(outputComponents[recipe.Name])}
		if recipe.Catalog != nil {
			target.Catalog = &factory.CatalogStack{Base: recipe.Catalog.Base, Overlays: append([]string(nil), recipe.Catalog.Overlays...)}
		}
		compileSpec.Recipes = append(compileSpec.Recipes, target)
	}
	for _, view := range definition.Clusters {
		recipeTargets := map[string]bool{}
		clusterCategories := map[string]bool{}
		roleBindings := map[string]*cluster.RoleBinding{}
		for _, member := range view.Members {
			var output DefinitionOutput
			for _, candidate := range definition.Outputs {
				if candidate.Name == member.Output {
					output = candidate
					break
				}
			}
			var selection RecipeSelection
			for _, candidate := range definition.Recipes {
				if candidate.Name == output.Recipe {
					selection = candidate
					break
				}
			}
			recipeTargets[output.Recipe] = true
			if member.Role == "" {
				clusterCategories[selection.Criteria.Category] = true
				continue
			}
			binding := roleBindings[member.Role]
			if binding == nil {
				binding = &cluster.RoleBinding{Role: member.Role}
				roleBindings[member.Role] = binding
			}
			binding.NodeLevelCategories = append(binding.NodeLevelCategories, selection.Criteria.Category)
			binding.Subset = append(binding.Subset, output.Components...)
		}
		roles := make([]cluster.RoleBinding, 0, len(roleBindings))
		for _, binding := range roleBindings {
			binding.NodeLevelCategories = uniqueSorted(binding.NodeLevelCategories)
			binding.Subset = uniqueSorted(binding.Subset)
			roles = append(roles, *binding)
		}
		sort.Slice(roles, func(i, j int) bool { return roles[i].Role < roles[j].Role })
		compileSpec.Clusters = append(compileSpec.Clusters, factory.ClusterSpec{Name: view.Name, Type: view.Type, CNI: view.CNI,
			RecipeTargets: sortedMapKeys(recipeTargets), RoleBindings: roles,
			ClusterCategories: sortedMapKeys(clusterCategories), AICRBundle: view.AICRBundle,
			ValidationRequirements: append([]string(nil), view.ValidationRequirements...)})
	}

	delivery := factory.ExecutionSettings{
		SBOMTool:   definition.SBOMTool,
		APIVersion: factory.ExecutionSettingsAPIVersion, Kind: factory.ExecutionSettingsKind,
		Metadata: factory.Metadata{Name: environment.Metadata.Name, Description: environment.Metadata.Description},
		RunsOn:   environment.RunsOn, ExecutionClasses: execution.CloneClasses(environment.ExecutionClasses),
		Targets:      make(map[string]factory.Target, len(definition.Outputs)),
		SourceAccess: make(map[string]factory.SourceAccess, len(environment.SourceAccess)),
		Runtime: factory.RuntimeProfile{Runtime: environment.Runtime.Runtime, RuntimeImage: environment.Runtime.RuntimeImage,
			HelperImage: environment.Runtime.HelperImage, BuilderImage: environment.Runtime.BuilderImage,
			Services: append([]string(nil), environment.Runtime.Services...), Variables: cloneMap(environment.Runtime.Variables),
			Setup: append([]string(nil), environment.Runtime.Setup...), ToolchainRegistry: environment.Runtime.ToolchainRegistry},
	}
	for source, access := range environment.SourceAccess {
		delivery.SourceAccess[source] = factory.SourceAccess{Mirror: access.Mirror, CredentialRef: access.CredentialRef}
	}
	if environment.Signing != nil {
		delivery.Signing = &factory.SigningProfile{Mode: environment.Signing.Mode, Identity: environment.Signing.Identity,
			Issuer: environment.Signing.Issuer, CosignTool: definition.CosignTool}
	}

	outputs := append([]DefinitionOutput(nil), definition.Outputs...)
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Name < outputs[j].Name })
	authoredNames := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		authoredNames[output.Name] = true
	}
	packageNames := make(map[string]bool, len(outputs)*2)
	closureInputs := make([]string, 0, len(outputs)+len(definition.Clusters))
	for _, output := range outputs {
		closureInputs = append(closureInputs, "package/"+output.Name)
	}
	for _, view := range definition.Clusters {
		closureInputs = append(closureInputs, "composition/"+view.Name)
	}
	for _, output := range outputs {
		selectors := make([]string, 0, len(output.Components))
		for _, component := range output.Components {
			selectors = append(selectors, output.Recipe+"/"+component)
		}
		packageKind := factory.PackageBinaryBundle
		switch output.Format {
		case OutputOCI:
			packageKind = factory.PackageImage
		case OutputAPT:
			packageKind = factory.PackageAPT
		case OutputHelm:
			packageKind = factory.PackageHelm
			if len(output.Components) == 0 {
				selectors = []string{output.Recipe + "/aicr-bundle"}
			}
		}
		bundleName := output.Name
		if packageNames[bundleName] {
			return factory.CompileSpec{}, factory.ExecutionSettings{}, e.New(e.CodeInvalidCatalog,
				"definition output %q derives package name %q that collides with another output", output.Name, bundleName)
		}
		packageNames[bundleName] = true
		packageSpec := factory.PackageSpec{Name: bundleName, Kind: packageKind, Inputs: selectors}
		if output.Helm != nil {
			image := "package/" + output.Helm.ImageOutput
			packageSpec.HelmImage = &factory.HelmImageBinding{Image: image, Chart: output.Helm.Chart}
			packageSpec.Inputs = append(packageSpec.Inputs, image)
		}
		if output.APT != nil {
			packageSpec.Distribution, packageSpec.Component = output.APT.Distribution, output.APT.Component
		}
		compileSpec.Packages = append(compileSpec.Packages, packageSpec)

	}
	if environment.Publication != nil {
		publication := environment.Publication
		delivery.Targets[publicationPackageName] = factory.Target{Kind: factory.TargetOCI, Ref: publication.RepositoryPrefix + "/releases",
			CredentialRef: publication.CredentialRef, Anonymous: publication.Anonymous, PlainHTTP: publication.PlainHTTP}
		delivery.Routes = []factory.Route{{Name: publicationPackageName, Input: "package/" + publicationPackageName, Target: publicationPackageName}}
		if authoredNames[publicationPackageName] || authoredNames["releases"] {
			return factory.CompileSpec{}, factory.ExecutionSettings{}, e.New(e.CodeInvalidCatalog,
				"definition output %q collides with the generated publication package", publicationPackageName)
		}
		compileSpec.Packages = append(compileSpec.Packages, factory.PackageSpec{Name: publicationPackageName, Kind: factory.PackageOCI,
			Inputs: append([]string(nil), closureInputs...)})
	}
	return compileSpec, delivery, nil
}

// One closure is built and transported once; repository routes reuse its identity.
const publicationPackageName = "release"

func sortedMapKeys(source map[string]bool) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueSorted(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return sortedMapKeys(set)
}

func cloneMap[V ~string](source map[string]V) map[string]V {
	if source == nil {
		return nil
	}
	clone := make(map[string]V, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
