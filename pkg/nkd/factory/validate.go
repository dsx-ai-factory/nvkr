package factory

import (
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func (plan Plan) Validate() error {
	for _, pin := range plan.Actions {
		if err := pin.Validate(); err != nil {
			return err
		}
	}
	if plan.SchemaVersion != PlanSchemaVersion || !canonicalName.MatchString(plan.Name) {
		return e.New(e.CodeInvalidCatalog, "run plan schema/name is invalid")
	}
	if strings.TrimSpace(plan.GeneratorVersion) == "" {
		return e.New(e.CodeInvalidCatalog, "run plan has no generator version")
	}
	if (plan.DefinitionPath == "") != (plan.EnvironmentPath == "") ||
		(plan.DefinitionPath != "" && (!safeGeneratedPath(plan.DefinitionPath) || !safeGeneratedPath(plan.EnvironmentPath))) {
		return e.New(e.CodeInvalidCatalog, "run plan definition/environment source paths are incomplete or unsafe")
	}
	if plan.CompileSpecDigest != "" && !canonical.IsDigest(plan.CompileSpecDigest) {
		return e.New(e.CodeUnpinnedSource, "run plan compile-spec identity is not digest-pinned")
	}
	if plan.DefinitionDigest != "" && !canonical.IsDigest(plan.DefinitionDigest) {
		return e.New(e.CodeUnverifiedRef, "run plan has malformed definitionDigest %q", plan.DefinitionDigest)
	}
	if plan.EnvironmentDigest != "" && !canonical.IsDigest(plan.EnvironmentDigest) {
		return e.New(e.CodeUnverifiedRef, "run plan has malformed environmentDigest %q", plan.EnvironmentDigest)
	}
	if (plan.DefinitionDigest == "") != (plan.EnvironmentDigest == "") {
		return e.New(e.CodeUnverifiedRef, "run plan must bind definitionDigest and environmentDigest together")
	}
	if err := plan.Qualification.ValidatePlanTier(plan.ExecutionSettings.Signing); err != nil {
		return err
	}
	if !canonical.IsDigest(plan.CatalogDigest) || !canonical.IsDigest(plan.ExecutionSettingsDigest) {
		return e.New(e.CodeUnpinnedSource, "run plan catalog/execution-settings identity is not digest-pinned")
	}
	if err := plan.ExecutionSettings.Validate(); err != nil {
		return err
	}
	executionSettingsDigest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		return err
	}
	if executionSettingsDigest != plan.ExecutionSettingsDigest {
		return e.New(e.CodeUnverifiedRef, "run plan execution settings do not match executionSettingsDigest")
	}
	previous := ""
	for _, applied := range plan.AppliedOverlays {
		if !canonicalName.MatchString(applied.Name) || !canonical.IsDigest(applied.OverlayDigest) ||
			!canonical.IsDigest(applied.InputDigest) || !canonical.IsDigest(applied.OutputDigest) {
			return e.New(e.CodeUnpinnedSource, "factory plan has an invalid applied-overlay binding")
		}
		if previous != "" && applied.InputDigest != previous {
			return e.New(e.CodeUnverifiedRef, "factory plan overlay %q is not chained to its predecessor", applied.Name)
		}
		previous = applied.OutputDigest
	}
	if previous != "" && previous != plan.CatalogDigest {
		return e.New(e.CodeUnverifiedRef, "factory plan overlay chain does not end at catalogDigest")
	}
	if len(plan.Recipes) == 0 {
		return e.New(e.CodeInvalidCatalog, "factory plan has no recipes")
	}
	seen := map[string]bool{}
	resolvedSources := map[string]bool{}
	for _, recipePlan := range plan.Recipes {
		if !canonicalName.MatchString(recipePlan.Name) || seen[recipePlan.Name] || recipePlan.Graph == nil {
			return e.New(e.CodeInvalidCatalog, "factory plan recipe target %q is invalid or repeated", recipePlan.Name)
		}
		seen[recipePlan.Name] = true
		if !canonical.IsDigest(recipePlan.CatalogDigest) {
			return e.New(e.CodeUnpinnedSource, "factory recipe target %q has no exact catalog digest", recipePlan.Name)
		}
		targetCatalog := ""
		for _, applied := range recipePlan.AppliedOverlays {
			if !canonical.IsDigest(applied.InputDigest) || !canonical.IsDigest(applied.OutputDigest) ||
				!canonical.IsDigest(applied.OverlayDigest) || !canonicalName.MatchString(applied.Name) ||
				(targetCatalog != "" && applied.InputDigest != targetCatalog) {
				return e.New(e.CodeUnverifiedRef, "factory recipe target %q has an invalid catalog overlay chain", recipePlan.Name)
			}
			targetCatalog = applied.OutputDigest
		}
		if targetCatalog != "" && targetCatalog != recipePlan.CatalogDigest {
			return e.New(e.CodeUnverifiedRef, "factory recipe target %q catalog overlay chain ends at the wrong digest", recipePlan.Name)
		}
		if err := recipePlan.Graph.Validate(); err != nil {
			return err
		}
		digest, err := recipePlan.Graph.Digest()
		if err != nil {
			return err
		}
		if digest != recipePlan.RecipeDigest || recipePlan.RecipeID != recipePlan.Graph.RecipeID {
			return e.New(e.CodeUnverifiedRef, "factory recipe target %q graph identity does not match its binding", recipePlan.Name)
		}
		if len(recipePlan.Components) == 0 && recipePlan.Graph.AICR == nil {
			return e.New(e.CodeInvalidCatalog, "factory recipe target %q selects no components", recipePlan.Name)
		}
		if !sort.StringsAreSorted(recipePlan.Components) || !sort.StringsAreSorted(recipePlan.AllComponents) {
			return e.New(e.CodeInvalidCatalog, "factory recipe target %q components are not canonical", recipePlan.Name)
		}
		for _, node := range recipePlan.Graph.Nodes {
			if node.Kind == ir.KindFetch && node.Repo != "" {
				resolvedSources[node.Repo] = true
			}
		}
	}
	for source := range plan.ExecutionSettings.SourceAccess {
		if !resolvedSources[source] {
			return e.New(e.CodeInvalidCatalog,
				"execution settings sourceAccess %q does not match a resolved fetch source", source)
		}
	}
	previousComposition := ""
	for _, composition := range plan.Compositions {
		if !canonicalName.MatchString(composition.Name) || composition.Name <= previousComposition ||
			!canonical.IsDigest(composition.Digest) {
			return e.New(e.CodeInvalidCatalog, "factory composition %q is invalid or noncanonical", composition.Name)
		}
		if err := composition.Composition.Validate(); err != nil {
			return err
		}
		digest, err := composition.Composition.Digest()
		if err != nil || digest != composition.Digest {
			return e.New(e.CodeUnverifiedRef, "factory composition %q does not match its digest", composition.Name)
		}
		previousComposition = composition.Name
	}
	if err := plan.validateSigningGraph(); err != nil {
		return err
	}
	if err := plan.Graph.Validate(); err != nil {
		return err
	}
	if err := plan.validateSBOMGraph(); err != nil {
		return err
	}
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
	builds := map[string]ir.Node{}
	for _, recipe := range plan.Recipes {
		for _, node := range recipe.Graph.Nodes {
			if node.Kind == ir.KindBuild {
				builds[recipe.Name+"/"+node.ID] = node
			}
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.HelmImage != nil {
			if node.Kind != NodePackage || node.PackageKind != PackageHelm {
				return e.New(e.CodePipelineDrift, "Helm binding on a non-Helm node")
			}
			artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
			for _, artifact := range plan.Graph.Artifacts {
				artifacts[artifact.ID] = artifact
			}
			var inputs []string
			for _, id := range node.Inputs {
				if artifacts[id].Kind != string(TierTerminalObservation) && artifacts[id].Kind != "producer-result" {
					inputs = append(inputs, id)
				}
			}
			if err := validatePackageInputs(PackageSpec{Kind: PackageHelm, HelmImage: node.HelmImage}, inputs, artifacts); err != nil {
				return err
			}
		}
		if node.Kind != NodeBuild {
			continue
		}
		build, ok := builds[node.RecipeTarget+"/"+node.IRNode]
		if !ok || node.Network != build.Network || node.Container != build.Toolchain || node.Platform != build.Platform {
			return e.New(e.CodePipelineDrift, "build node %q changes its exact recipe execution settings", node.ID)
		}
		if build.BuilderType == ir.BuilderBuildKit && plan.ExecutionSettings.RuntimeBinary() != "docker" {
			return e.New(e.CodeEngineCapability, "rootless BuildKit node %q requires the Docker runtime in its Environment", node.ID)
		}
	}
	return plan.validateLifecyclePlans()
}

func (plan Plan) validateLifecyclePlans() error {
	if len(plan.LifecycleOutputs) == 0 && len(plan.LifecycleClusters) == 0 {
		return nil
	}
	if plan.DefinitionDigest == "" || plan.EnvironmentDigest == "" || len(plan.LifecycleOutputs) == 0 {
		return e.New(e.CodeUnverifiedRef, "lifecycle plan bindings require exact Definition and Environment identities")
	}
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	recipes := make(map[string]RecipePlan, len(plan.Recipes))
	for _, recipe := range plan.Recipes {
		recipes[recipe.RecipeDigest] = recipe
	}
	nodes := make(map[string]Node, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodes[node.ID] = node
	}
	previous := ""
	dummy := make([]LifecycleBoundOutput, 0, len(plan.LifecycleOutputs))
	for _, output := range plan.LifecycleOutputs {
		artifact, ok := artifacts[output.ArtifactID]
		recipe, recipeOK := recipes[output.RecipeDigest]
		producer := nodes[artifact.Producer]
		var actualComponents []string
		actualRecipe := ""
		for _, inputID := range producer.Inputs {
			input := artifacts[inputID]
			if input.Kind == "producer-result" || input.RecipeTarget == "" {
				continue
			}
			if input.Component != "" && !(output.Kind == "helm-oci" && input.Kind == "aicr-bundle") {
				actualComponents = append(actualComponents, input.Component)
			}
			if actualRecipe == "" {
				actualRecipe = input.RecipeTarget
			} else if actualRecipe != input.RecipeTarget {
				actualRecipe = "!mixed"
			}
		}
		actualComponents = uniqueSorted(actualComponents)
		if !canonicalName.MatchString(output.Name) || output.Name <= previous || !ok || artifact.Kind != lifecyclePackageKind(output.Kind) ||
			artifact.Name != output.Name || !recipeOK || (output.Kind != "helm-oci" && len(output.Components) == 0) || !sort.StringsAreSorted(output.Components) ||
			recipe.Name != actualRecipe || !equalFactoryStrings(actualComponents, output.Components) {
			return e.New(e.CodeUnverifiedRef, "lifecycle output %q is malformed or not bound to its graph recipe", output.Name)
		}
		dummy = append(dummy, LifecycleBoundOutput{Name: output.Name, Kind: output.Kind,
			Digest: "sha256:" + strings.Repeat("0", 64), RecipeDigest: output.RecipeDigest,
			Components: append([]string(nil), output.Components...)})
		previous = output.Name
	}
	previous = ""
	for _, cluster := range plan.LifecycleClusters {
		if !canonicalName.MatchString(cluster.Name) || cluster.Name <= previous || !canonical.IsDigest(cluster.CompositionDigest) || len(cluster.Members) == 0 {
			return e.New(e.CodeUnverifiedRef, "lifecycle cluster %q is malformed or noncanonical", cluster.Name)
		}
		memberPrevious := ""
		for _, member := range cluster.Members {
			key := member.Output + "\x00" + member.Role
			if !canonicalName.MatchString(member.Output) || key <= memberPrevious || (member.Role != "" && !canonicalName.MatchString(member.Role)) {
				return e.New(e.CodeUnverifiedRef, "lifecycle cluster %q has a malformed member", cluster.Name)
			}
			memberPrevious = key
		}
		previous = cluster.Name
	}
	return ValidateLifecycleRealizations(plan.Compositions, plan.LifecycleOutputs, plan.LifecycleClusters, dummy)
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
	default:
		return ""
	}
}

// validateSigningGraph binds the emitted signing lane to the delivery policy that
// authorizes it: a plan cannot carry a sign or verify-publication node without a
// signing profile, cannot sign a non-OCI target, and cannot publish to a signing
// delivery without also signing that route. Node presence is authority-bearing
// here — an executor dispatches on it — so it is checked at the plan boundary
// rather than trusted from whoever produced the plan file.
func (plan Plan) validateSigningGraph() error {
	signed := map[string]bool{}
	published := map[string]bool{}
	for _, node := range plan.Graph.Nodes {
		switch node.Kind {
		case NodePublish:
			published[node.Target] = true
			continue
		case NodeSign:
			signed[node.Target] = true
		case NodeVerifyPublication:
		default:
			continue
		}
		if plan.ExecutionSettings.Signing == nil {
			return e.New(e.CodeTrustPolicyMissing,
				"factory plan node %q has no delivery signing profile authorizing it", node.ID)
		}
		if plan.ExecutionSettings.Targets[node.Target].Kind != TargetOCI {
			return e.New(e.CodeEngineCapability,
				"factory plan node %q signs target %q, which is not an OCI registry", node.ID, node.Target)
		}
	}
	if plan.ExecutionSettings.Signing == nil {
		return nil
	}
	for target := range published {
		if !signed[target] {
			return e.New(e.CodeTrustPolicyMissing,
				"factory plan publishes to signing target %q without a sign node", target)
		}
	}
	return nil
}

func (graph ArtifactGraph) Validate() error {
	if graph.SchemaVersion != ArtifactGraphSchema || len(graph.Nodes) == 0 || len(graph.Artifacts) == 0 {
		return e.New(e.CodeInvalidCatalog, "artifact graph schema or contents are invalid")
	}
	nodes := make(map[string]Node, len(graph.Nodes))
	for index, node := range graph.Nodes {
		if !safeGraphID(node.ID) || nodes[node.ID].ID != "" {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q is invalid or repeated", node.ID)
		}
		if strings.TrimSpace(node.RunsOn) == "" || strings.ContainsAny(node.RunsOn, "\r\n\x00") {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q has no safe runner label", node.ID)
		}
		if node.Container != "" && !containerPin.MatchString(node.Container) {
			return e.New(e.CodeUnpinnedSource, "artifact graph node %q has unpinned container %q", node.ID, node.Container)
		}
		if node.Platform != "" && node.Platform != "linux/amd64" && node.Platform != "linux/arm64" {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q has unsupported platform %q", node.ID, node.Platform)
		}
		if index > 0 && graph.Nodes[index-1].ID >= node.ID {
			return e.New(e.CodeInvalidCatalog, "artifact graph nodes are not canonical")
		}
		switch node.Kind {
		case NodeFetch, NodeAcquire, NodePublish, NodeSign, NodeVerifyPublication:
			if node.Network != ir.NetworkDefault {
				return e.New(e.CodeInvalidCatalog, "artifact graph networked node %q does not declare default network", node.ID)
			}
		case NodeBuild:
			if node.Network != ir.NetworkNone && node.Network != ir.NetworkDefault {
				return e.New(e.CodeInvalidCatalog, "artifact graph build node %q has an unknown network mode", node.ID)
			}
		case NodeAICR, NodeVerify, NodeSBOM, NodePackage, NodeComposition, NodeInventory:
			if node.Network != ir.NetworkNone {
				return e.New(e.CodeInvalidCatalog, "artifact graph isolated node %q does not declare network none", node.ID)
			}
		default:
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q has unsupported kind %q", node.ID, node.Kind)
		}
		if node.Kind == NodeBuild {
			if node.IRNode == "" || node.Container == "" || node.Platform == "" {
				return e.New(e.CodeEngineCapability, "artifact graph build node %q lacks exact IR/container/platform execution inputs", node.ID)
			}
		}
		if node.Network == ir.NetworkNone && node.Kind != NodeBuild &&
			(node.Container == "" || node.Platform == "") {
			return e.New(e.CodeEngineCapability,
				"artifact graph isolated node %q lacks exact utility container/platform execution inputs", node.ID)
		}
		if node.Kind == NodeFetch && node.IRNode != "" && node.Container == "" {
			return e.New(e.CodeEngineCapability, "artifact graph fetch node %q has no pinned vendoring container", node.ID)
		}
		if node.Kind == NodeAcquire && node.IRNode == "" {
			if !strings.HasPrefix(node.URL, "https://") || !canonical.IsDigest(node.Digest) ||
				node.Executable == "" || !canonical.IsDigest(node.ExecutableDigest) ||
				(node.ToolFormat != ToolFormatRaw && node.ToolFormat != ToolFormatTarGzip) {
				return e.New(e.CodeUnpinnedSource, "artifact graph tool acquisition %q is not transport- and executable-pinned", node.ID)
			}
			if node.ToolFormat == ToolFormatRaw && node.Digest != node.ExecutableDigest {
				return e.New(e.CodeUnverifiedRef, "artifact graph raw tool acquisition %q has distinct transport and executable digests", node.ID)
			}
		}
		if len(node.Outputs) == 0 {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q has no named outputs", node.ID)
		}
		nodes[node.ID] = node
	}

	artifacts := make(map[string]Artifact, len(graph.Artifacts))
	for index, artifact := range graph.Artifacts {
		if !safeGraphID(artifact.ID) || artifacts[artifact.ID].ID != "" || artifact.Producer == "" {
			return e.New(e.CodeInvalidCatalog, "artifact graph artifact %q is invalid or repeated", artifact.ID)
		}
		if !safeGeneratedPath(artifact.Path) {
			return e.New(e.CodeInvalidCatalog, "artifact graph artifact %q has unsafe or empty path %q", artifact.ID, artifact.Path)
		}
		if index > 0 && graph.Artifacts[index-1].ID >= artifact.ID {
			return e.New(e.CodeInvalidCatalog, "artifact graph artifacts are not canonical")
		}
		if _, ok := nodes[artifact.Producer]; !ok {
			return e.New(e.CodeInvalidCatalog, "artifact %q names unknown producer %q", artifact.ID, artifact.Producer)
		}
		artifacts[artifact.ID] = artifact
	}

	produced := map[string]string{}
	indegree := map[string]int{}
	children := map[string][]string{}
	for _, node := range graph.Nodes {
		indegree[node.ID] = 0
		if !sort.StringsAreSorted(node.Inputs) || !sort.StringsAreSorted(node.Outputs) {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q inputs/outputs are not canonical", node.ID)
		}
		if hasAdjacentDuplicate(node.Inputs) || hasAdjacentDuplicate(node.Outputs) {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q repeats an input or output", node.ID)
		}
		for _, output := range node.Outputs {
			artifact, ok := artifacts[output]
			if !ok || artifact.Producer != node.ID || produced[output] != "" {
				return e.New(e.CodeInvalidCatalog, "artifact graph output %q has an invalid producer binding", output)
			}
			produced[output] = node.ID
		}
	}
	if len(produced) != len(artifacts) {
		return e.New(e.CodeInvalidCatalog, "artifact graph contains an artifact not declared by its producer")
	}
	for _, node := range graph.Nodes {
		parents := map[string]bool{}
		for _, input := range node.Inputs {
			artifact, ok := artifacts[input]
			if !ok {
				return e.New(e.CodeInvalidCatalog, "artifact graph node %q consumes unknown artifact %q", node.ID, input)
			}
			if artifact.Producer == node.ID {
				return e.New(e.CodeInvalidCatalog, "artifact graph node %q consumes its own output %q", node.ID, input)
			}
			if !parents[artifact.Producer] {
				parents[artifact.Producer] = true
				indegree[node.ID]++
				children[artifact.Producer] = append(children[artifact.Producer], node.ID)
			}
		}
	}
	ready := make([]string, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	visited := 0
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		visited++
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if visited != len(nodes) {
		return e.New(e.CodeInvalidCatalog, "artifact graph contains a dataflow cycle")
	}
	return nil
}

func hasAdjacentDuplicate(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return true
		}
	}
	return false
}

func safeGraphID(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !canonicalName.MatchString(part) {
			return false
		}
	}
	return true
}
