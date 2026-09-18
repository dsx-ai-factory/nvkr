package factory

import (
	"bytes"
	"maps"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
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
	if !canonical.IsDigest(plan.CatalogDigest) {
		return e.New(e.CodeUnpinnedSource, "run plan catalog identity is not digest-pinned")
	}
	if plan.CatalogSnapshot == nil {
		return e.New(e.CodeUnverifiedRef, "run plan has no base catalog snapshot")
	}
	catalogDigest, err := plan.CatalogSnapshot.SnapshotDigest()
	if err != nil {
		return err
	}
	if catalogDigest != plan.CatalogDigest {
		return e.New(e.CodeUnverifiedRef, "run plan base catalog snapshot differs from catalogDigest")
	}
	if !maps.Equal(plan.Actions, plan.CatalogSnapshot.Actions) {
		return e.New(e.CodePipelineDrift, "run plan action pins differ from its base catalog snapshot")
	}
	if err := admitInputs(plan.Definition, plan.Environment); err != nil {
		return err
	}
	definitionDigest, err := plan.Definition.Digest()
	if err != nil {
		return err
	}
	environmentDigest, err := plan.Environment.Digest()
	if err != nil {
		return err
	}
	if definitionDigest != plan.DefinitionDigest || plan.Name != plan.Definition.Metadata.Name {
		return e.New(e.CodeUnverifiedRef, "run plan definition does not match definitionDigest")
	}
	if environmentDigest != plan.EnvironmentDigest {
		return e.New(e.CodeUnverifiedRef, "run plan environment does not match environmentDigest")
	}
	if err := plan.Qualification.ValidatePlanTier(plan.Environment.Signing); err != nil {
		return err
	}
	packages, targets, routes, err := derivePublication(plan.Definition, plan.Environment)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(plan.Targets, targets) || !reflect.DeepEqual(plan.Routes, routes) {
		return e.New(e.CodeUnverifiedRef, "run plan publication routing differs from its authored environment")
	}
	if len(plan.LifecycleOutputs) != len(plan.Definition.Outputs) || len(plan.Builds) != len(plan.Definition.Outputs) {
		return e.New(e.CodeUnverifiedRef, "run plan omits or adds authored output slots")
	}
	for i, output := range plan.Definition.Outputs {
		bound := plan.LifecycleOutputs[i]
		if bound.Name != output.Name || bound.Kind != output.Format || bound.ArtifactID != "package/"+output.Name || !equalFactoryStrings(bound.Components, output.Components) {
			return e.New(e.CodeUnverifiedRef, "run plan output %q differs from its definition", output.Name)
		}
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
	if len(plan.Builds) == 0 {
		return e.New(e.CodeInvalidCatalog, "factory plan has no builds")
	}
	seen := map[string]bool{}
	resolvedSources := map[string]bool{}
	for _, buildPlan := range plan.Builds {
		if !canonicalName.MatchString(buildPlan.Name) || seen[buildPlan.Name] || buildPlan.Graph == nil {
			return e.New(e.CodeInvalidCatalog, "factory plan build target %q is invalid or repeated", buildPlan.Name)
		}
		seen[buildPlan.Name] = true
		if !canonical.IsDigest(buildPlan.CatalogDigest) {
			return e.New(e.CodeUnpinnedSource, "factory build target %q has no exact catalog digest", buildPlan.Name)
		}
		targetCatalog := ""
		for _, applied := range buildPlan.AppliedOverlays {
			if !canonical.IsDigest(applied.InputDigest) || !canonical.IsDigest(applied.OutputDigest) ||
				!canonical.IsDigest(applied.OverlayDigest) || !canonicalName.MatchString(applied.Name) ||
				(targetCatalog != "" && applied.InputDigest != targetCatalog) {
				return e.New(e.CodeUnverifiedRef, "factory build target %q has an invalid catalog overlay chain", buildPlan.Name)
			}
			targetCatalog = applied.OutputDigest
		}
		if targetCatalog != "" && targetCatalog != buildPlan.CatalogDigest {
			return e.New(e.CodeUnverifiedRef, "factory build target %q catalog overlay chain ends at the wrong digest", buildPlan.Name)
		}
		if err := buildPlan.Graph.Validate(); err != nil {
			return err
		}
		digest, err := buildPlan.Graph.Digest()
		if err != nil {
			return err
		}
		if digest != buildPlan.BuildDigest || buildPlan.Platform != buildPlan.Graph.Platform {
			return e.New(e.CodeUnverifiedRef, "factory build target %q graph identity does not match its binding", buildPlan.Name)
		}
		if len(buildPlan.Components) == 0 && buildPlan.Graph.AICR == nil {
			return e.New(e.CodeInvalidCatalog, "factory build target %q selects no components", buildPlan.Name)
		}
		if !sort.StringsAreSorted(buildPlan.Components) || hasAdjacentDuplicate(buildPlan.Components) {
			return e.New(e.CodeInvalidCatalog, "factory build target %q components are not canonical", buildPlan.Name)
		}
		if err := validateBuildCatalog(buildPlan, plan.Environment); err != nil {
			return err
		}
		for _, node := range buildPlan.Graph.Nodes {
			if node.Kind == ir.KindFetch && node.Repo != "" {
				resolvedSources[node.Repo] = true
			}
		}
	}
	for source := range plan.Environment.SourceAccess {
		if !resolvedSources[source] {
			return e.New(e.CodeInvalidCatalog,
				"execution settings sourceAccess %q does not match a resolved fetch source", source)
		}
	}
	if err := plan.validateEvidenceAttestationGraph(); err != nil {
		return err
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
	for _, build := range plan.Builds {
		selected := make(map[string]struct{}, len(build.Components))
		for _, name := range build.Components {
			selected[name] = struct{}{}
		}
		components := make(map[string]*execution.Requirements, len(build.ResolvedComponents))
		for _, component := range build.ResolvedComponents {
			if _, selectedComponent := selected[component.Name]; !selectedComponent {
				return e.New(e.CodePipelineDrift,
					"build %q retains unselected resolved component %q", build.Name, component.Name)
			}
			if _, duplicate := components[component.Name]; duplicate {
				return e.New(e.CodePipelineDrift,
					"build %q repeats resolved component %q", build.Name, component.Name)
			}
			components[component.Name] = component.BuildRequirements
		}
		for name := range selected {
			if _, ok := components[name]; !ok {
				return e.New(e.CodePipelineDrift,
					"build %q has no resolved component %q", build.Name, name)
			}
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind == ir.KindBuild {
				if _, selectedComponent := selected[node.Component]; !selectedComponent {
					if node.ExecutionBinding != nil {
						return e.New(e.CodePipelineDrift,
							"build %q unselected build node %q has an execution-class binding", build.Name, node.ID)
					}
					continue
				}
				requirements := components[node.Component]
				if !execution.Equal(node.Requirements, requirements) {
					return e.New(e.CodePipelineDrift,
						"build %q build node %q changes its catalog execution requirements", build.Name, node.ID)
				}
				expectedBinding, matchErr := execution.Match(
					node.Requirements, plan.Environment.ExecutionClasses, matchOptionsForBuildNode(node))
				if matchErr != nil {
					return e.Wrap(e.CodeEngineCapability, matchErr,
						"build %q build node %q has no admitted execution class", build.Name, node.ID)
				}
				if !execution.EqualBinding(node.ExecutionBinding, expectedBinding) {
					return e.New(e.CodePipelineDrift,
						"build %q build node %q changes its deterministic execution-class binding", build.Name, node.ID)
				}
				builds[build.Name+"/"+node.ID] = node
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
			if err := validatePackageInputs(packageSpec{Kind: PackageHelm, HelmImage: node.HelmImage}, inputs, artifacts); err != nil {
				return err
			}
		}
		if node.IRNode != "" {
			if _, err := plan.ExecutionFor(node); err != nil {
				return err
			}
		}
		if node.Kind != NodeBuild {
			continue
		}
		build, ok := builds[node.BuildTarget+"/"+node.IRNode]
		if !ok {
			return e.New(e.CodePipelineDrift, "operation %q has no selected producer instruction", node.ID)
		}
		if build.BuilderType == ir.BuilderBuildKit && plan.Environment.RuntimeBinary() != "docker" {
			return e.New(e.CodeEngineCapability, "rootless BuildKit node %q requires the Docker runtime in its Environment", node.ID)
		}
	}
	if err := plan.validateLifecyclePlans(); err != nil {
		return err
	}
	return plan.validateCompiledGraph(packages)
}

// validateCompiledGraph checks the complete compiler-owned operation set after
// authenticating the authored inputs, catalog snapshots and producer selections.
func (plan Plan) validateCompiledGraph(packages []packageSpec) error {
	outputCatalogs := make(map[string]*catalog.Catalog, len(plan.Builds))
	for _, build := range plan.Builds {
		outputCatalogs[build.Name] = build.CatalogSnapshot
		for _, output := range plan.Definition.Outputs {
			if output.Name == build.Name && output.Catalog == nil && build.CatalogDigest != plan.CatalogDigest {
				return e.New(e.CodeUnverifiedRef, "output %q substitutes the Definition catalog", output.Name)
			}
		}
	}
	tools, err := selectedTools(plan.Definition, plan.Environment, plan.CatalogSnapshot, outputCatalogs)
	if err != nil {
		return err
	}
	expected, err := compileArtifactGraph(plan, tools, packages)
	if err != nil {
		return err
	}
	if len(plan.Graph.Nodes) != len(expected.Nodes) || len(plan.Graph.Artifacts) != len(expected.Artifacts) {
		return e.New(e.CodePipelineDrift, "run plan operation or artifact set differs from its compiler-derived graph")
	}
	for i, want := range expected.Nodes {
		actual := plan.Graph.Nodes[i]
		// Omitted empty inputs can be nil after decoding. All nonempty edges and
		// their canonical ordering remain exact; graph validation rejects duplicates.
		if len(actual.Inputs) == 0 {
			actual.Inputs = nil
		}
		if len(want.Inputs) == 0 {
			want.Inputs = nil
		}
		if !reflect.DeepEqual(actual, want) {
			return e.New(e.CodePipelineDrift, "operation %q differs from compiler-derived operation %q", actual.ID, want.ID)
		}
	}
	for i, want := range expected.Artifacts {
		if plan.workspace != "" {
			path, err := relocatedFactoryPath(want.Path, plan.workspace)
			if err != nil {
				return err
			}
			want.Path = filepath.ToSlash(path)
		}
		if plan.Graph.Artifacts[i] != want {
			return e.New(e.CodePipelineDrift, "artifact %q differs from compiler-derived artifact %q", plan.Graph.Artifacts[i].ID, want.ID)
		}
	}
	return nil
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
		actualComponents = uniqueSorted(actualComponents)
		if !canonicalName.MatchString(output.Name) || output.Name <= previous || !ok || artifact.Kind != lifecyclePackageKind(output.Kind) ||
			artifact.Name != output.Name || !buildOK || (output.Kind != "helm-oci" && len(output.Components) == 0) || !sort.StringsAreSorted(output.Components) ||
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
		case NodeVerifyPublication, NodeAttestEvidence:
		default:
			continue
		}
		if plan.Environment.Signing == nil {
			return e.New(e.CodeTrustPolicyMissing,
				"factory plan node %q has no delivery signing profile authorizing it", node.ID)
		}
		if plan.Targets[node.Target].Kind != TargetOCI {
			return e.New(e.CodeEngineCapability,
				"factory plan node %q signs target %q, which is not an OCI registry", node.ID, node.Target)
		}
	}
	if plan.Environment.Signing == nil {
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
		producer := node.IRNode != ""
		if producer {
			if node.BuildTarget == "" || (node.Kind != NodeFetch && node.Kind != NodeBuild && node.Kind != NodeAcquire) {
				return e.New(e.CodePipelineDrift, "operation %q has an invalid producer reference", node.ID)
			}
			if node.RunsOn != "" || node.Network != "" || node.Container != "" || node.Platform != "" || node.Requirements != nil || node.ExecutionBinding != nil {
				return e.New(e.CodePipelineDrift, "operation %q duplicates producer execution settings", node.ID)
			}
		}
		if !producer {
			if node.Kind == NodeBuild || node.Kind == NodeFetch {
				return e.New(e.CodeEngineCapability, "operation %q lacks an exact producer reference", node.ID)
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
			if node.Kind != NodeBuild && (node.Requirements != nil || node.ExecutionBinding != nil) {
				return e.New(e.CodeInvalidCatalog,
					"artifact graph execution requirements and bindings are supported only on build node %q", node.ID)
			}
			if err := node.Requirements.Validate(); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err,
					"artifact graph node %q has invalid execution requirements", node.ID)
			}
			if node.ExecutionBinding != nil {
				if err := node.ExecutionBinding.ValidateFor(node.Requirements); err != nil {
					return e.Wrap(e.CodeInvalidCatalog, err,
						"artifact graph node %q has invalid execution binding", node.ID)
				}
			}
			switch node.Kind {
			case NodeFetch, NodeAcquire, NodePublish, NodeSign, NodeVerifyPublication, NodeAttestEvidence:
				if node.Network != ir.NetworkDefault {
					return e.New(e.CodeInvalidCatalog, "artifact graph networked node %q does not declare default network", node.ID)
				}
			case NodeBuild:
				if node.Network != ir.NetworkNone && node.Network != ir.NetworkDefault {
					return e.New(e.CodeInvalidCatalog, "artifact graph build node %q has an unknown network mode", node.ID)
				}
			case NodeAICR, NodeVerify, NodeSBOM, NodePackage, NodeInventory:
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
		}
		if index > 0 && graph.Nodes[index-1].ID >= node.ID {
			return e.New(e.CodeInvalidCatalog, "artifact graph nodes are not canonical")
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
		if artifact.BuildTarget != nodes[artifact.Producer].BuildTarget {
			return e.New(e.CodeUnverifiedRef, "artifact %q changes its producing output target", artifact.ID)
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

// validateBuildCatalog binds the complete executable graph to its catalog and
// Environment. Logical identity alone excludes transport and execution metadata.
func validateBuildCatalog(build BuildPlan, environment authored.Environment) error {
	if build.CatalogSnapshot == nil {
		return e.New(e.CodeUnverifiedRef, "output %q has no catalog snapshot", build.Name)
	}
	digest, err := build.CatalogSnapshot.SnapshotDigest()
	if err != nil {
		return err
	}
	if digest != build.CatalogDigest {
		return e.New(e.CodeUnverifiedRef, "output %q catalog snapshot differs", build.Name)
	}
	selection := buildgraph.Selection{Components: build.Components, Platform: build.Platform, AICRProfile: build.AICRProfileName}
	if build.Graph.AICR != nil {
		selection.Bundle = &build.Graph.AICR.Bundle
	}
	selected, err := buildgraph.Resolve(build.CatalogSnapshot, selection)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(selected.Components, build.ResolvedComponents) {
		return e.New(e.CodeUnverifiedRef, "output %q resolved producers differ from catalog", build.Name)
	}
	profileIdentity := ""
	if selected.AICRProfile != nil {
		profileIdentity = selected.AICRProfile.Identity
	}
	if build.AICRProfileIdentity != profileIdentity {
		return e.New(e.CodeUnverifiedRef, "output %q AICR profile identity differs from catalog", build.Name)
	}
	graph, err := compileBuildGraph(selected, environment)
	if err != nil {
		return err
	}
	graphDigest, err := graph.Digest()
	if err != nil {
		return err
	}
	if graphDigest != build.BuildDigest {
		return e.New(e.CodeUnverifiedRef, "output %q graph differs from catalog producers", build.Name)
	}
	expected, err := graph.CanonicalJSON()
	if err != nil {
		return err
	}
	actual, err := build.Graph.CanonicalJSON()
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return e.New(e.CodePipelineDrift, "output %q executable graph differs from its catalog and environment", build.Name)
	}
	return nil
}
