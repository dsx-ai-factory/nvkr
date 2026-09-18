package factory

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// CompileDocuments compiles lifecycle-owned handoff values while resolving catalog
// paths against the public Definition that selected them.
func CompileDocuments(ctx context.Context, definitionPath string, definition CompileSpec,
	environmentPath string, delivery ExecutionSettings, generatorVersion string,
) (*Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "definition compilation requires a context")
	}
	stack, err := loadFactoryCatalogStack(ctx, definitionPath, definition.Catalog)
	if err != nil {
		return nil, err
	}
	targetStacks := make(map[string]*catalog.Stack)
	for _, target := range definition.Builds {
		if target.Catalog == nil {
			continue
		}
		targetStack, err := loadFactoryCatalogStack(ctx, definitionPath, *target.Catalog)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "loading catalog stack for build %q", target.Name)
		}
		targetStacks[target.Name] = targetStack
	}
	plan, err := compileWithTargetStacks(ctx, definition, delivery, stack, targetStacks, generatorVersion)
	if err != nil {
		return nil, err
	}
	plan.DefinitionPath = filepath.ToSlash(filepath.Clean(definitionPath))
	plan.EnvironmentPath = filepath.ToSlash(filepath.Clean(environmentPath))
	return plan, nil
}

func loadFactoryCatalogStack(ctx context.Context, definitionPath string, catalogSpec CatalogStack) (*catalog.Stack, error) {
	basePath := catalogSpec.Base
	if !filepath.IsAbs(basePath) {
		basePath = filepath.Join(filepath.Dir(definitionPath), basePath)
	}
	provider := catalog.Dir(basePath)
	base, err := catalog.LoadContext(ctx, provider)
	if err != nil {
		return nil, err
	}
	overlays := make([]catalog.Overlay, 0, len(catalogSpec.Overlays))
	for _, overlayPath := range catalogSpec.Overlays {
		if !filepath.IsAbs(overlayPath) {
			overlayPath = filepath.Join(filepath.Dir(definitionPath), overlayPath)
		}
		overlay, err := catalog.LoadOverlayContext(ctx, overlayPath)
		if err != nil {
			return nil, err
		}
		overlays = append(overlays, overlay)
	}
	stack, err := catalog.ApplyStack(base, overlays...)
	if err != nil {
		return nil, err
	}
	return stack, nil
}

// Compile is the deep pure-ish compiler once external files have been admitted.
// toolchainMaterializedPrefix names operator-materialized toolchain images. The catalog pins
// them registry-agnostically (bare `nkd-toolchain-*@sha256:…`) so the default catalog carries no
// operator's registry; the delivery's toolchainRegistry supplies WHERE the operator published
// them. A public toolchain (e.g. golang@sha256:…) never carries this prefix and is left as-is.
const toolchainMaterializedPrefix = "nkd-toolchain-"

// relocateToolchainRegistry rewrites each build node's operator-materialized toolchain ref into
// the delivery-supplied registry, preserving the @sha256 digest (mirroring airgap.Relocate, so
// buildDigest — which strips the prefix — is unchanged). When no registry is configured the
// operator toolchains are left registry-agnostic (they will not pull at runtime until an
// operator names their registry); public toolchains are always left untouched.
func relocateToolchainRegistry(graph *ir.Graph, registry string) {
	registry = strings.TrimSuffix(strings.TrimSpace(registry), "/")
	if registry == "" {
		return
	}
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		if node.Kind != ir.KindBuild || node.Toolchain == "" {
			continue
		}
		name := node.Toolchain
		if at := strings.Index(name, "@"); at >= 0 {
			name = name[:at]
		}
		// A bare name (no registry host / no path separator) carrying our prefix is an
		// operator toolchain awaiting relocation; everything else is already resolvable.
		if strings.Contains(name, "/") || !strings.HasPrefix(name, toolchainMaterializedPrefix) {
			continue
		}
		node.Toolchain = registry + "/" + node.Toolchain
	}
}

func Compile(ctx context.Context, definition CompileSpec, delivery ExecutionSettings, stack *catalog.Stack, generatorVersion string) (*Plan, error) {
	for _, target := range definition.Builds {
		if target.Catalog != nil {
			return nil, e.New(e.CodeUsage,
				"factory build target %q has a catalog override; compile it through the lifecycle owner", target.Name)
		}
	}
	return compileWithTargetStacks(ctx, definition, delivery, stack, nil, generatorVersion)
}

func compileWithTargetStacks(ctx context.Context, definition CompileSpec, delivery ExecutionSettings, stack *catalog.Stack,
	targetStacks map[string]*catalog.Stack, generatorVersion string) (*Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "factory compilation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "factory compilation canceled")
	}
	if stack == nil || stack.Catalog == nil {
		return nil, e.New(e.CodeInvalidCatalog, "factory compilation requires a resolved catalog stack")
	}
	if len(definition.SelectedTools) > 0 {
		if definition.Tools == nil {
			definition.Tools = map[string]Tool{}
		}
		for _, name := range definition.SelectedTools {
			pin, ok := stack.Catalog.Tools[name]
			if !ok {
				return nil, e.New(e.CodeUnpinnedSource, "selected catalog tool %q has no pin", name)
			}
			if _, duplicate := definition.Tools[name]; duplicate {
				return nil, e.New(e.CodeInvalidCatalog, "duplicate tool %q", name)
			}
			if pin.Platform != definition.UtilityPlatform {
				return nil, e.New(e.CodeEngineCapability, "tool %q platform %s does not match utility platform %s", name, pin.Platform, definition.UtilityPlatform)
			}
			definition.Tools[name] = Tool{Format: pin.Format, URL: pin.URL, ArchiveDigest: pin.ArchiveDigest, ExecutablePath: pin.ExecutablePath, ExecutableDigest: pin.ExecutableDigest}
		}
	}
	// The AUTHORED definition's identity, taken before the runner defaulting below
	// mutates the document. Production signing binds it into the attestation, so it
	// is computed here (where the definition object exists) rather than rederived
	// from a file the executing job may not even have. It is recorded only when the
	// delivery signs: a plan that mints no attestation must keep its exact bytes.
	compileSpecDigest := ""
	if delivery.Signing != nil {
		digest, err := definition.Digest()
		if err != nil {
			return nil, err
		}
		compileSpecDigest = digest
	}
	// ADR-18 §8: the runner is environment config. A delivery may override the definition's default
	// runner for its environment (e.g. a GitLab tag); otherwise an unset runsOn falls back to the
	// generic hosted runner. The effective default propagates to every node's RunsOn below.
	if r := strings.TrimSpace(delivery.RunsOn); r != "" {
		definition.RunsOn = r
	} else if strings.TrimSpace(definition.RunsOn) == "" {
		definition.RunsOn = DefaultRunsOn
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	if err := delivery.Validate(); err != nil {
		return nil, err
	}
	// The Plan owns its class inventory. ExecutionSettings is passed by value, but
	// both this slice and each class's capability slice would otherwise alias the
	// caller and permit post-compilation mutation of executable Plan identity.
	delivery.ExecutionClasses = execution.CloneClasses(delivery.ExecutionClasses)
	if stack == nil || stack.Catalog == nil {
		return nil, e.New(e.CodeInvalidCatalog, "factory compilation requires a resolved catalog stack")
	}
	executionSettingsDigest, err := delivery.Digest()
	if err != nil {
		return nil, err
	}
	if delivery.Signing != nil && delivery.Signing.Mode == SigningModeCosign {
		if _, ok := definition.Tools[delivery.Signing.CosignTool]; !ok {
			return nil, e.New(e.CodeTrustPolicyMissing,
				"environment signing cosignTool %q is not a pinned tool in compile spec %q",
				delivery.Signing.CosignTool, definition.Metadata.Name)
		}
	}
	plan := &Plan{Actions: cloneActionPins(stack.Catalog.Actions), SchemaVersion: PlanSchemaVersion, Name: definition.Metadata.Name,
		GeneratorVersion: generatorVersion, Qualification: planTier(delivery.Signing),
		CompileSpecDigest: compileSpecDigest,
		CatalogDigest:     stack.Digest, AppliedOverlays: append([]catalog.AppliedOverlay(nil), stack.Applied...),
		ExecutionSettingsDigest: executionSettingsDigest, ExecutionSettings: delivery,
		Graph: ArtifactGraph{SchemaVersion: ArtifactGraphSchema}}

	targets := append([]BuildTarget(nil), definition.Builds...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	artifacts := map[string]Artifact{}
	nodes := map[string]Node{}
	for _, name := range sortedKeys(definition.Tools) {
		tool := definition.Tools[name]
		nodeID := "tool/" + name
		artifactID := "tool/" + name
		nodes[nodeID] = Node{ID: nodeID, Kind: NodeAcquire, RunsOn: definition.RunsOn,
			Network: ir.NetworkDefault, URL: tool.URL, Digest: tool.ArchiveDigest,
			Executable: tool.ExecutablePath, ExecutableDigest: tool.ExecutableDigest, ToolFormat: tool.Format,
			Outputs: []string{artifactID}}
		artifacts[artifactID] = Artifact{ID: artifactID, Name: name, Kind: "pipeline-tool",
			MediaType: "application/vnd.nkd.pipeline-tool.v1", Producer: nodeID,
			Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "tools", name, tool.ExecutablePath))}
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "factory compilation canceled")
		}
		if target.RunsOn == "" {
			target.RunsOn = definition.RunsOn
		}
		targetStack := stack
		if selected := targetStacks[target.Name]; selected != nil {
			targetStack = selected
		}
		resolved, err := buildgraph.Resolve(targetStack.Catalog, buildgraph.Selection{Components: target.Components, Platform: target.Platform, AICRProfile: target.AICRProfile, Bundle: target.Bundle})
		if err != nil {
			return nil, e.Wrap(e.CodeNoMatch, err, "factory build target %q", target.Name)
		}
		if resolved.AICRProfile != nil {
			if target.AICRGenerator == "" {
				return nil, e.New(e.CodeInvalidCatalog,
					"factory AICR target %q requires an exact pipeline tool in aicrGenerator", target.Name)
			}
			tool := definition.Tools[target.AICRGenerator]
			profile := *resolved.AICRProfile
			// Generator identity is execution-platform specific. The catalog selects
			// the semantic build, while the factory target pins the exact executable
			// that will reproduce it on this runner. buildgraph.Compile then binds that
			// executable digest into buildDigest.
			if profile.GeneratorDigest != tool.ExecutableDigest {
				return nil, e.New(e.CodeUnpinnedSource, "output %q generator does not match selected profile pin", target.Name)
			}
			resolved.AICRProfile = &profile
		} else if target.AICRGenerator != "" {
			return nil, e.New(e.CodeInvalidCatalog,
				"factory build target %q names aicrGenerator but resolves no AICR profile", target.Name)
		}
		graph, err := buildgraph.Compile(resolved)
		if err != nil {
			return nil, err
		}
		buildDigest, err := graph.Digest()
		if err != nil {
			return nil, err
		}
		// The catalog pins operator-materialized toolchains registry-agnostically
		// (`nkd-toolchain-*@sha256:…`); resolve them into THIS environment's registry now. Only
		// the registry prefix changes — the digest is preserved — so buildDigest (computed
		// above, and which strips the prefix) is unaffected. Public toolchains are untouched.
		relocateToolchainRegistry(graph, delivery.Runtime.ToolchainRegistry)
		selected := resolved
		selectedNames := make([]string, 0, len(selected.Components))
		selectedSet := map[string]bool{}
		for _, component := range selected.Components {
			selectedNames = append(selectedNames, component.Name)
			selectedSet[component.Name] = true
		}
		sort.Strings(selectedNames)
		if err := bindBuildExecution(graph, selectedSet, delivery.ExecutionClasses); err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err,
				"factory build target %q cannot satisfy execution requirements", target.Name)
		}
		aicrIdentity := ""
		if resolved.AICRProfile != nil {
			aicrIdentity = resolved.AICRProfile.Identity
		}
		snapshot, err := catalog.ApplyStack(targetStack.Catalog)
		if err != nil {
			return nil, err
		}
		plan.Builds = append(plan.Builds, BuildPlan{Name: target.Name,
			CatalogDigest: targetStack.Digest, AppliedOverlays: append([]catalog.AppliedOverlay(nil), targetStack.Applied...),
			BuildDigest: buildDigest, Platform: target.Platform, CatalogSnapshot: snapshot.Catalog, RunsOn: target.RunsOn,
			Components: selectedNames,
			// The SBOM node bills the SELECTED (built) components: those are exactly the
			// ones with a producer-result carrying an observed digest (ADR-13).
			ResolvedComponents: append([]catalog.NamedComponent(nil), selected.Components...),
			AICRProfileName:    target.AICRProfile, AICRProfileIdentity: aicrIdentity, Graph: graph})
		if err := addBuildDataflow(target, graph, selectedSet, definition.UtilityContainer, definition.UtilityPlatform, nodes, artifacts); err != nil {
			return nil, err
		}
	}
	if delivery.SBOMTool != "" {
		if _, ok := definition.Tools[delivery.SBOMTool]; !ok {
			return nil, e.New(e.CodeProvenanceMissing, "SBOM scanner is not a pinned tool")
		}
		for id, node := range nodes {
			if node.Kind != NodeSBOM {
				continue
			}
			components := map[string]bool{}
			for _, input := range node.Inputs {
				if artifacts[input].Kind == "producer-result" {
					components[artifacts[input].Component] = true
				}
			}
			for _, artifact := range artifacts {
				if artifact.BuildTarget == node.BuildTarget && components[artifact.Component] && strings.HasPrefix(artifact.ID, "artifact/") {
					node.Inputs = append(node.Inputs, artifact.ID)
				}
			}
			node.Inputs = uniqueSorted(append(node.Inputs, "tool/"+delivery.SBOMTool))
			nodes[id] = node
		}
	}

	packages, err := orderPackageSpecs(definition.Packages)
	if err != nil {
		return nil, err
	}
	for _, spec := range packages {
		inputs, err := expandSelectors(spec.Inputs, artifacts)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "factory package %q", spec.Name)
		}
		if err := validatePackageInputs(spec, inputs, artifacts); err != nil {
			return nil, err
		}
		if spec.Kind == PackageImage && nodes[artifacts[inputs[0]].Producer].Kind != NodeBuild {
			return nil, e.New(e.CodeEngineCapability, "image package %q currently requires a built OCI export; acquired layouts need a separate adapter", spec.Name)
		}
		// Packaging is explicitly gated by each target's terminal observation as a
		// separate input, rather than hiding the policy edge in scheduler needs.
		inputs = append(inputs, observationInputs(inputs, artifacts)...)
		if spec.Kind == PackageOCI {
			inputs = append(inputs, packageProducerResults(inputs, nodes, artifacts)...)
			inputs = append(inputs, publicationSBOMInputs(inputs, nodes, artifacts)...)
		}
		inputs = uniqueSorted(inputs)
		nodeID := "package/" + spec.Name
		artifactID := "package/" + spec.Name
		nodes[nodeID] = Node{ID: nodeID, Kind: NodePackage, PackageKind: spec.Kind,
			HelmImage:    spec.HelmImage,
			Distribution: spec.Distribution, PackageComponent: spec.Component,
			Inputs: inputs, Outputs: []string{artifactID}, Network: ir.NetworkNone, RunsOn: definition.RunsOn,
			Container: definition.UtilityContainer, Platform: definition.UtilityPlatform}
		artifacts[artifactID] = Artifact{ID: artifactID, Name: spec.Name, Kind: spec.Kind,
			MediaType: packageMediaType(spec.Kind), Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "packages", spec.Name)), Producer: nodeID}
	}

	routes := append([]Route(nil), delivery.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].Name < routes[j].Name })
	for _, route := range routes {
		inputs, err := expandSelectors([]string{route.Input}, artifacts)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "delivery route %q", route.Name)
		}
		if len(inputs) != 1 {
			return nil, e.New(e.CodeInvalidCatalog, "delivery route %q must select exactly one artifact, got %d", route.Name, len(inputs))
		}
		target := delivery.Targets[route.Target]
		if err := validateRouteCompatibility(route, target, artifacts[inputs[0]]); err != nil {
			return nil, err
		}
		nodeID := "publish/" + route.Name
		outputID := "publication/" + route.Name
		nodes[nodeID] = Node{ID: nodeID, Kind: NodePublish, Target: route.Target,
			Inputs: inputs, Outputs: []string{outputID}, Network: ir.NetworkDefault, RunsOn: definition.RunsOn}
		artifacts[outputID] = Artifact{ID: outputID, Name: route.Name, Kind: "published-reference",
			Producer: nodeID, Target: route.Target,
			Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "publications", route.Name+".txt"))}
		if err := addSigningDataflow(definition, delivery, route, inputs[0], outputID, nodes, artifacts); err != nil {
			return nil, err
		}
	}

	plan.Graph.Nodes = mapNodes(nodes)
	plan.Graph.Artifacts = mapArtifacts(artifacts)
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
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
func orderPackageSpecs(authored []PackageSpec) ([]PackageSpec, error) {
	byName := make(map[string]PackageSpec, len(authored))
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
	ordered := make([]PackageSpec, 0, len(authored))
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

// Artifact kinds produced by the signing lane. They are records ABOUT a
// publication, never the publication itself, so they are deliberately not
// packageable inputs.
const (
	ArtifactSigningRecord       = "signing-record"
	ArtifactProvenancePredicate = "provenance-predicate"
	ArtifactVerificationRecord  = "verification-record"
)

// addSigningDataflow appends the two post-publication nodes of one OCI route when
// the delivery configures signing. The edges are pure dataflow, not ordering
// hints: sign consumes the PUBLISHED reference (so it cannot run before publish)
// together with the routed layout (so it can re-derive the digest it is about to
// bind instead of trusting the reference file), and verify-publication consumes
// the reference and the signing record (so it cannot run before signing).
func addSigningDataflow(definition CompileSpec, delivery ExecutionSettings, route Route,
	packageInput, referenceID string, nodes map[string]Node, artifacts map[string]Artifact) error {
	signing := delivery.Signing
	if signing == nil {
		return nil
	}
	if delivery.Targets[route.Target].Kind != TargetOCI {
		return e.New(e.CodeEngineCapability,
			"delivery route %q cannot be signed: only OCI registry targets have a signing adapter", route.Name)
	}
	signInputs := []string{packageInput, referenceID}
	verifyInputs := []string{referenceID, "signature/" + route.Name}
	if signing.Mode == SigningModeCosign {
		// The pinned cosign executable is DATA this node consumes; declaring it as an
		// input is what makes a generated pipeline transport it to the signing job
		// instead of assuming an ambient binary on the runner.
		tool := "tool/" + signing.CosignTool
		if _, ok := artifacts[tool]; !ok {
			return e.New(e.CodeTrustPolicyMissing,
				"delivery signing cosignTool %q has no acquired tool artifact", signing.CosignTool)
		}
		signInputs = append(signInputs, tool)
		verifyInputs = append(verifyInputs, tool)
	}
	signID := NodeSign + "/" + route.Name
	nodes[signID] = Node{ID: signID, Kind: NodeSign, Target: route.Target,
		Inputs: uniqueSorted(signInputs), Network: ir.NetworkDefault, RunsOn: definition.RunsOn,
		Outputs: uniqueSorted([]string{"signature/" + route.Name, "predicate/" + route.Name})}
	artifacts["signature/"+route.Name] = Artifact{ID: "signature/" + route.Name, Name: route.Name,
		Kind: ArtifactSigningRecord, MediaType: "application/vnd.nkd.signing-record.v1+json",
		Producer: signID, Target: route.Target,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "signatures", route.Name+".json"))}
	artifacts["predicate/"+route.Name] = Artifact{ID: "predicate/" + route.Name, Name: route.Name,
		Kind: ArtifactProvenancePredicate, MediaType: "application/vnd.in-toto.provenance.v1+json",
		Producer: signID, Target: route.Target,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "predicates", route.Name+".json"))}
	verifyID := NodeVerifyPublication + "/" + route.Name
	nodes[verifyID] = Node{ID: verifyID, Kind: NodeVerifyPublication, Target: route.Target,
		Inputs: uniqueSorted(verifyInputs), Network: ir.NetworkDefault, RunsOn: definition.RunsOn,
		Outputs: []string{"verification/" + route.Name}}
	artifacts["verification/"+route.Name] = Artifact{ID: "verification/" + route.Name, Name: route.Name,
		Kind: ArtifactVerificationRecord, MediaType: "application/vnd.nkd.verification-record.v1+json",
		Producer: verifyID, Target: route.Target,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "verifications", route.Name+".json"))}
	return nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func addBuildDataflow(targetSpec BuildTarget, graph *ir.Graph, selected map[string]bool,
	utilityContainer, utilityPlatform string, nodes map[string]Node, artifacts map[string]Artifact) error {
	target := targetSpec.Name
	byID := map[string]ir.Node{}
	selectedFetch := map[string]bool{}
	for _, node := range graph.Nodes {
		byID[node.ID] = node
		if node.Kind == ir.KindBuild && selected[node.Component] {
			for _, dependency := range node.Needs {
				selectedFetch[dependency] = true
			}
		}
	}
	producerOutputs := make([]string, 0, len(selected))
	for _, irNode := range graph.Nodes {
		nodeID := target + "/" + irNode.ID
		switch irNode.Kind {
		case ir.KindFetch:
			if !selectedFetch[irNode.ID] {
				continue
			}
			output := "source/" + target + "/" + irNode.ID
			receipt := "source-receipt/" + target + "/" + irNode.ID
			receiptPath, err := pipeline.SourceReceiptPath(filepath.Join(".nkd", "factory", target, "src"), irNode.Dest)
			if err != nil {
				return err
			}
			nodes[nodeID] = Node{ID: nodeID, Kind: NodeFetch, BuildTarget: target, IRNode: irNode.ID,
				Network: ir.NetworkDefault, Container: irNode.VendorImage, Platform: targetSpec.Platform,
				RunsOn: targetSpec.RunsOn, Outputs: []string{output, receipt}}
			artifacts[output] = Artifact{ID: output, Name: irNode.ID, Kind: "source-closure",
				Producer: nodeID, BuildTarget: target,
				Path: filepath.ToSlash(filepath.Join(".nkd", "factory", target, "src", irNode.Dest))}
			artifacts[receipt] = Artifact{ID: receipt, Name: irNode.ID + "-receipt", Kind: "source-receipt",
				Producer: nodeID, BuildTarget: target,
				Path: filepath.ToSlash(receiptPath)}
		case ir.KindBuild:
			if !selected[irNode.Component] {
				continue
			}
			inputs := make([]string, 0, len(irNode.Needs))
			for _, dependency := range irNode.Needs {
				if byID[dependency].Kind != ir.KindFetch {
					return e.New(e.CodeInvalidCatalog, "build %q has unsupported non-fetch input %q", irNode.ID, dependency)
				}
				inputs = append(inputs, "source/"+target+"/"+dependency, "source-receipt/"+target+"/"+dependency)
			}
			output := "artifact/" + target + "/" + irNode.Component
			result := "result/" + target + "/" + irNode.Component
			runsOn := targetSpec.RunsOn
			if irNode.ExecutionBinding != nil {
				runsOn = irNode.ExecutionBinding.Class.RunsOn
			}
			nodes[nodeID] = Node{ID: nodeID, Kind: NodeBuild, BuildTarget: target, IRNode: irNode.ID,
				Requirements: execution.Clone(irNode.Requirements), ExecutionBinding: execution.CloneBinding(irNode.ExecutionBinding),
				Network: irNode.Network, Container: irNode.Toolchain, Platform: irNode.Platform,
				RunsOn: runsOn, Inputs: inputs, Outputs: []string{output, result}}
			artifacts[output] = Artifact{ID: output, Name: irNode.Component, Kind: irNode.OutputType,
				MediaType: artifactMediaType(irNode.OutputType), Producer: nodeID, BuildTarget: target,
				Component: irNode.Component, Path: factoryBuildArtifactPath(target, irNode)}
			artifacts[result] = Artifact{ID: result, Name: irNode.Component + "-result", Kind: "producer-result",
				Producer: nodeID, BuildTarget: target, Component: irNode.Component,
				Path: filepath.ToSlash(filepath.Join(".nkd", "factory", target, "results", irNode.ID+".json"))}
			producerOutputs = append(producerOutputs, output, result)
		case ir.KindPull:
			if !selected[irNode.Component] {
				continue
			}
			output := "artifact/" + target + "/" + irNode.Component
			result := "result/" + target + "/" + irNode.Component
			nodes[nodeID] = Node{ID: nodeID, Kind: NodeAcquire, BuildTarget: target, IRNode: irNode.ID,
				Network: ir.NetworkDefault, Platform: targetSpec.Platform,
				RunsOn: targetSpec.RunsOn, Outputs: []string{output, result}}
			artifacts[output] = Artifact{ID: output, Name: irNode.Component, Kind: irNode.OutputType,
				MediaType: artifactMediaType(irNode.OutputType), Producer: nodeID, BuildTarget: target,
				Component: irNode.Component, Path: factoryPullArtifactPath(target, irNode)}
			artifacts[result] = Artifact{ID: result, Name: irNode.Component + "-result", Kind: "producer-result",
				Producer: nodeID, BuildTarget: target, Component: irNode.Component,
				Path: filepath.ToSlash(filepath.Join(".nkd", "factory", target, "results", irNode.ID+".json"))}
			producerOutputs = append(producerOutputs, output, result)
		}
	}
	if graph.AICR != nil {
		generator := targetSpec.AICRGenerator
		generatorArtifact := "tool/" + generator
		if _, ok := artifacts[generatorArtifact]; !ok {
			return e.New(e.CodeInvalidCatalog,
				"factory AICR target %q requires exact generator tool %q so the pinned build can be rendered without an ambient executable", target, generator)
		}
		nodeID := target + "/aicr-render"
		output := "artifact/" + target + "/aicr-bundle"
		nodes[nodeID] = Node{ID: nodeID, Kind: NodeAICR, BuildTarget: target,
			Network: ir.NetworkNone, RunsOn: targetSpec.RunsOn, Container: utilityContainer,
			Platform: utilityPlatform, Inputs: []string{generatorArtifact}, Outputs: []string{output}}
		artifacts[output] = Artifact{ID: output, Name: "aicr-bundle", Kind: "aicr-bundle",
			MediaType: "application/vnd.nkd.aicr-bundle.v1", Producer: nodeID,
			BuildTarget: target, Component: "aicr-bundle",
			Path: filepath.ToSlash(filepath.Join(".nkd", "factory", target, "aicr-bundle"))}
		producerOutputs = append(producerOutputs, output)
	}
	sort.Strings(producerOutputs)
	if len(producerOutputs) == 0 {
		return e.New(e.CodeInvalidCatalog, "factory build target %q has no selected artifact producers", target)
	}
	verifyID := target + "/terminal-observe"
	observationID := "observation/" + target
	nodes[verifyID] = Node{ID: verifyID, Kind: NodeVerify, BuildTarget: target,
		Network: ir.NetworkNone, RunsOn: targetSpec.RunsOn, Container: utilityContainer,
		Platform: utilityPlatform, Inputs: producerOutputs, Outputs: []string{observationID}}
	artifacts[observationID] = Artifact{ID: observationID, Name: target + "-terminal-observation",
		Kind: string(TierTerminalObservation), Producer: verifyID, BuildTarget: target,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", target, "terminal-observation.json"))}

	// SBOM node (ADR-13 §1 / ADR-13): one per build target, gated on the
	// terminal observation, binding a CycloneDX SBOM per built component to that
	// component's producer-result digest. It bills only what was built, so a
	// producer-less target (e.g. AICR-only) mints no SBOM.
	resultInputs := make([]string, 0, len(producerOutputs))
	for _, id := range producerOutputs {
		if strings.HasPrefix(id, "result/") {
			resultInputs = append(resultInputs, id)
		}
	}
	if len(resultInputs) > 0 {
		sbomNodeID := target + "/sbom"
		sbomArtifactID := "sbom/" + target
		inputs := uniqueSorted(append(resultInputs, observationID))
		nodes[sbomNodeID] = Node{ID: sbomNodeID, Kind: NodeSBOM, BuildTarget: target,
			Network: ir.NetworkNone, RunsOn: targetSpec.RunsOn, Container: utilityContainer,
			Platform: utilityPlatform, Inputs: inputs, Outputs: []string{sbomArtifactID}}
		artifacts[sbomArtifactID] = Artifact{ID: sbomArtifactID, Name: target + "-sbom",
			Kind: "sbom-set", MediaType: "application/vnd.nkd.sbom-set.v1+json",
			Producer: sbomNodeID, BuildTarget: target,
			Path: filepath.ToSlash(filepath.Join(".nkd", "factory", target, "sbom"))}
	}
	return nil
}

func factoryBuildArtifactPath(target string, node ir.Node) string {
	rel := filepath.Clean(node.ArtifactPath)
	if node.WriteDir != "" {
		if candidate, err := filepath.Rel(filepath.Clean(node.WriteDir), rel); err == nil &&
			candidate != ".." && !strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
			rel = candidate
		}
	}
	return filepath.ToSlash(filepath.Join(".nkd", "factory", target, "out", node.Component, rel))
}

func factoryPullArtifactPath(target string, node ir.Node) string {
	if node.OutputType == "oci-image" {
		return filepath.ToSlash(filepath.Join(".nkd", "factory", target, "fetch", node.Component, node.Component+".oci"))
	}
	name := node.ArtifactPath
	if name == "" {
		name = node.Component
	}
	return filepath.ToSlash(filepath.Join(".nkd", "factory", target, "fetch", node.Component, name))
}

func expandSelectors(selectors []string, artifacts map[string]Artifact) ([]string, error) {
	var expanded []string
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if strings.HasSuffix(selector, "/*") && strings.Count(selector, "*") == 1 {
			prefix := "artifact/" + strings.TrimSuffix(selector, "*")
			for id, artifact := range artifacts {
				if strings.HasPrefix(id, prefix) && artifact.Component != "aicr-bundle" {
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

func validatePackageInputs(spec PackageSpec, inputs []string, artifacts map[string]Artifact) error {
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
			if artifact.Kind != "aicr-bundle" && (artifact.Kind != "tarball" || artifact.BuildTarget == "") {
				return e.New(e.CodeInvalidCatalog, "Helm package %q input %s requires an AICR tree or build source archive", spec.Name, id)
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
		case PackageBinaryBundle, PackageOCI, PackageHelm, PackageImage, "oci-image", "aicr-bundle":
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
		return "application/vnd.nkd.aicr-helm-bundle.v1"
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

func mapNodes(values map[string]Node) []Node {
	keys := sortedKeys(values)
	out := make([]Node, 0, len(keys))
	for _, key := range keys {
		node := values[key]
		node.Inputs = uniqueSorted(node.Inputs)
		node.Outputs = uniqueSorted(node.Outputs)
		out = append(out, node)
	}
	return out
}

func mapArtifacts(values map[string]Artifact) []Artifact {
	keys := sortedKeys(values)
	out := make([]Artifact, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

func uniqueSorted(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return sortedKeys(set)
}
