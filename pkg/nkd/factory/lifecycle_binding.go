package factory

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

const (
	lifecycleBindingName  = "nkd-lifecycle"
	lifecycleBindingKind  = "lifecycle-binding"
	lifecycleBindingMedia = "application/vnd.nkd.lifecycle-binding.v1+json"
	clusterBindingSchema  = "nkd.dev/cluster-realization/v1"
)

type lifecycleContent struct {
	Artifacts []lifecycleContentArtifact `json:"artifacts"`
}

type lifecycleContentArtifact struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Digest       string   `json:"digest"`
	RecipeDigest string   `json:"recipeDigest"`
	Components   []string `json:"components,omitempty"`
}

// LifecyclePayloadObservation is an independently reopened and reproduced
// lifecycle package. It is descriptive only; callers still bind it to a Run
// or signed publication through the owning lifecycle module.
type LifecyclePayloadObservation struct {
	Digest     string
	Components []string
}

// ObserveLifecyclePayload verifies one compiled lifecycle output against its
// exact package inputs and producer results. Artifact paths must name the bytes
// in the execution workspace being observed.
func ObserveLifecyclePayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifacts map[string]Artifact) (LifecyclePayloadObservation, error) {
	artifact, ok := artifacts[expected.ArtifactID]
	if !ok || artifact.Kind != lifecyclePackageKind(expected.Kind) {
		return LifecyclePayloadObservation{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no typed payload", expected.Name)
	}
	node := planNode(plan, artifact.Producer)
	if node.ID == "" {
		return LifecyclePayloadObservation{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no package producer", expected.Name)
	}
	components := append([]string(nil), expected.Components...)
	recipeName := ""
	for _, recipe := range plan.Recipes {
		if recipe.RecipeDigest == expected.RecipeDigest {
			if recipeName != "" {
				return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle output %q has an ambiguous recipe identity", expected.Name)
			}
			recipeName = recipe.Name
		}
	}
	if recipeName == "" {
		return LifecyclePayloadObservation{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no exact recipe", expected.Name)
	}
	verifyRawResults := func() error {
		seen := map[string]bool{}
		for _, inputID := range node.Inputs {
			input := artifacts[inputID]
			if input.RecipeTarget == "" || input.Component == "" || input.Kind == "producer-result" {
				continue
			}
			if seen[input.Component] {
				return e.New(e.CodeVerifyFailed, "lifecycle output %q has ambiguous producer bytes for %q", expected.Name, input.Component)
			}
			seen[input.Component] = true
			digest, err := digestFactoryPath(ctx, input.Path)
			info, statErr := os.Stat(input.Path)
			if err != nil || statErr != nil || !info.Mode().IsRegular() {
				return e.New(e.CodeVerifyFailed, "lifecycle output %q has no exact producer bytes for %q", expected.Name, input.Component)
			}
			matches := 0
			for _, candidate := range artifacts {
				if candidate.Kind != "producer-result" || candidate.RecipeTarget != input.RecipeTarget || candidate.Component != input.Component {
					continue
				}
				result, readErr := readProducerResult(ctx, candidate.Path)
				if readErr != nil {
					return readErr
				}
				if result.Component != input.Component || result.SHA256 != digest || result.Size != info.Size() {
					return e.New(e.CodeVerifyFailed, "lifecycle output %q component %q differs from its producer result", expected.Name, input.Component)
				}
				matches++
			}
			if matches != 1 {
				return e.New(e.CodeReleaseIncomplete, "lifecycle output %q component %q has no unique producer result", expected.Name, input.Component)
			}
		}
		if len(seen) != len(expected.Components) {
			return e.New(e.CodeVerifyFailed, "lifecycle output %q producer membership differs from its plan binding", expected.Name)
		}
		for _, component := range expected.Components {
			if !seen[component] {
				return e.New(e.CodeVerifyFailed, "lifecycle output %q omits producer bytes for %q", expected.Name, component)
			}
		}
		return nil
	}
	switch artifact.Kind {
	case PackageBinaryBundle:
		bundle, result, err := delivery.InspectBinaryBundle(ctx, artifact.Path)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		components = components[:0]
		for _, entry := range bundle.Entries {
			components = append(components, entry.Name)
			matches := 0
			for _, candidate := range artifacts {
				if candidate.Kind != "producer-result" || candidate.RecipeTarget != recipeName || candidate.Component != entry.Name {
					continue
				}
				resultRecord, readErr := readProducerResult(ctx, candidate.Path)
				if readErr != nil {
					return LifecyclePayloadObservation{}, readErr
				}
				if resultRecord.Component == entry.Name && resultRecord.SHA256 == entry.SHA256 && resultRecord.Size == entry.Size {
					matches++
				}
			}
			if matches != 1 {
				return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle output %q component %q has no exact producer result", expected.Name, entry.Name)
			}
		}
		if !equalFactoryStrings(components, expected.Components) {
			return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle output %q members differ from its plan binding", expected.Name)
		}
		return LifecyclePayloadObservation{Digest: result.RootDigest, Components: components}, nil
	case PackageAPT:
		repository, err := delivery.InspectAPTRepository(ctx, artifact.Path)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		if err := verifyRawResults(); err != nil {
			return LifecyclePayloadObservation{}, err
		}
		expectedDigest, err := reproduceLifecyclePackage(ctx, plan, artifact, artifacts)
		if err != nil || expectedDigest != repository.Digest {
			return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle APT output %q differs from independently reproduced package bytes", expected.Name)
		}
		return LifecyclePayloadObservation{Digest: repository.Digest, Components: components}, nil
	case PackageHelm:
		inspection, err := delivery.Inspect(ctx, artifact.Path)
		if err != nil {
			return LifecyclePayloadObservation{}, err
		}
		expectedDigest, err := reproduceLifecyclePackage(ctx, plan, artifact, artifacts)
		if err != nil || expectedDigest != inspection.RootDigest {
			return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "lifecycle Helm output %q differs from independently reproduced AICR package (%s != %s)", expected.Name, inspection.RootDigest, expectedDigest)
		}
		return LifecyclePayloadObservation{Digest: inspection.RootDigest}, nil
	default:
		return LifecyclePayloadObservation{}, e.New(e.CodeEngineCapability, "package kind %q cannot be observed for lifecycle output", artifact.Kind)
	}
}

func lifecyclePublicationObject(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) (delivery.ObjectInput, error) {
	declared := make(map[string]bool, len(node.Inputs))
	for _, input := range node.Inputs {
		declared[input] = true
	}
	binding := LifecycleBinding{SchemaVersion: LifecycleBindingSchema, DefinitionDigest: plan.DefinitionDigest,
		EnvironmentDigest: plan.EnvironmentDigest}
	for _, expected := range plan.LifecycleOutputs {
		artifact, ok := artifacts[expected.ArtifactID]
		if !ok || artifact.Kind != lifecyclePackageKind(expected.Kind) {
			return delivery.ObjectInput{}, e.New(e.CodeReleaseIncomplete, "lifecycle output %q has no typed payload", expected.Name)
		}
		visible := make(map[string]Artifact, len(declared)+1)
		visible[artifact.ID] = artifact
		for id := range declared {
			visible[id] = artifacts[id]
		}
		observation, err := ObserveLifecyclePayload(ctx, plan, expected, visible)
		if err != nil {
			return delivery.ObjectInput{}, err
		}
		components := observation.Components
		payloadDigest := observation.Digest
		if !equalFactoryStrings(components, expected.Components) || payloadDigest == "" || expected.RecipeDigest == "" {
			return delivery.ObjectInput{}, e.New(e.CodeVerifyFailed, "lifecycle output %q payload differs from its exact plan binding", expected.Name)
		}
		bound := LifecycleBoundOutput{Name: expected.Name, Kind: expected.Kind,
			Digest: payloadDigest, RecipeDigest: expected.RecipeDigest, Components: append([]string(nil), components...)}
		if artifact.Kind == PackageAPT {
			bound.TransportPath = artifact.Path
		}
		binding.Outputs = append(binding.Outputs, bound)
	}
	sort.Slice(binding.Outputs, func(i, j int) bool { return binding.Outputs[i].Name < binding.Outputs[j].Name })
	if err := ValidateLifecycleRealizations(plan.Compositions, plan.LifecycleOutputs, plan.LifecycleClusters, binding.Outputs); err != nil {
		return delivery.ObjectInput{}, err
	}
	content := lifecycleContent{}
	for _, output := range binding.Outputs {
		content.Artifacts = append(content.Artifacts, lifecycleContentArtifact{Name: output.Name,
			Kind: output.Kind, Digest: output.Digest, RecipeDigest: output.RecipeDigest, Components: append([]string(nil), output.Components...)})
	}
	contentSubject, err := canonical.Digest(canonical.KindReleaseContent, content)
	if err != nil {
		return delivery.ObjectInput{}, err
	}
	binding.ContentSubject = contentSubject
	outputs := make(map[string]LifecycleBoundOutput, len(binding.Outputs))
	for _, output := range binding.Outputs {
		outputs[output.Name] = output
	}
	for _, planned := range plan.LifecycleClusters {
		var composition *CompositionPlan
		for index := range plan.Compositions {
			if plan.Compositions[index].Name == planned.Name {
				composition = &plan.Compositions[index]
				break
			}
		}
		if composition == nil || composition.Digest != planned.CompositionDigest {
			return delivery.ObjectInput{}, e.New(e.CodeVerifyFailed, "cluster %q differs from its exact composition plan", planned.Name)
		}
		digest, err := composition.Composition.Digest()
		if err != nil || digest != planned.CompositionDigest {
			return delivery.ObjectInput{}, e.New(e.CodeVerifyFailed, "cluster %q composition bytes differ from its identity", planned.Name)
		}
		cluster := LifecycleBoundCluster{SchemaVersion: clusterBindingSchema, Name: planned.Name,
			CompositionDigest: planned.CompositionDigest, ContentSubject: contentSubject}
		for _, member := range planned.Members {
			output, ok := outputs[member.Output]
			if !ok {
				return delivery.ObjectInput{}, e.New(e.CodeReleaseIncomplete, "cluster %q references missing output %q", planned.Name, member.Output)
			}
			cluster.Members = append(cluster.Members, LifecycleBoundClusterMember{Output: output.Name, ArtifactDigest: output.Digest,
				RecipeDigest: output.RecipeDigest, Components: append([]string(nil), output.Components...), Role: member.Role})
		}
		sort.Slice(cluster.Members, func(i, j int) bool {
			if cluster.Members[i].Output != cluster.Members[j].Output {
				return cluster.Members[i].Output < cluster.Members[j].Output
			}
			return cluster.Members[i].Role < cluster.Members[j].Role
		})
		if _, err := canonical.Digest(canonical.KindClusterRealization, cluster); err != nil {
			return delivery.ObjectInput{}, err
		}
		binding.Clusters = append(binding.Clusters, cluster)
	}
	sort.Slice(binding.Clusters, func(i, j int) bool { return binding.Clusters[i].Name < binding.Clusters[j].Name })
	raw, err := canonical.Marshal(binding)
	if err != nil {
		return delivery.ObjectInput{}, err
	}
	semantic, err := canonical.Digest(canonical.KindLifecycleBinding, binding)
	if err != nil {
		return delivery.ObjectInput{}, err
	}
	return delivery.ObjectInput{Name: lifecycleBindingName, Kind: lifecycleBindingKind, ArtifactType: lifecycleBindingMedia,
		LayerMediaType: lifecycleBindingMedia, SemanticDigest: semantic, Bytes: raw}, nil
}

func reproduceLifecyclePackage(ctx context.Context, plan *Plan, artifact Artifact, artifacts map[string]Artifact) (string, error) {
	stage, err := os.MkdirTemp(filepath.Dir(artifact.Path), ".lifecycle-reproduce-*")
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "creating lifecycle package reproduction stage")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	clone := make(map[string]Artifact, len(artifacts))
	for id, value := range artifacts {
		clone[id] = value
	}
	reproduced := artifact
	reproduced.Path = filepath.Join(stage, "output")
	clone[artifact.ID] = reproduced
	node := planNode(plan, artifact.Producer)
	if err := executePackage(ctx, plan, node, clone); err != nil {
		return "", err
	}
	switch artifact.Kind {
	case PackageAPT:
		result, err := delivery.InspectAPTRepository(ctx, reproduced.Path)
		if err != nil {
			return "", err
		}
		return result.Digest, nil
	case PackageHelm:
		result, err := delivery.Inspect(ctx, reproduced.Path)
		if err != nil {
			return "", err
		}
		return result.RootDigest, nil
	default:
		return "", e.New(e.CodeEngineCapability, "package kind %q cannot be reproduced for lifecycle binding", artifact.Kind)
	}
}

func planNode(plan *Plan, id string) Node {
	for _, node := range plan.Graph.Nodes {
		if node.ID == id {
			return node
		}
	}
	return Node{}
}

// ValidateLifecycleRealizations owns the exact join from resolved recipes and
// compiled cluster compositions to observed public output payloads.
func ValidateLifecycleRealizations(compositions []CompositionPlan, expected []LifecycleOutputPlan,
	clusters []LifecycleClusterPlan, observed []LifecycleBoundOutput,
) error {
	outputs := make(map[string]LifecycleBoundOutput, len(observed))
	for _, output := range observed {
		if _, exists := outputs[output.Name]; exists || lifecyclePackageKind(output.Kind) == "" || !canonical.IsDigest(output.Digest) ||
			!canonical.IsDigest(output.RecipeDigest) || (output.Kind != "helm-oci" && len(output.Components) == 0) {
			return e.New(e.CodeVerifyFailed, "lifecycle outputs are not unique exact payload bindings")
		}
		outputs[output.Name] = output
	}
	if len(outputs) != len(expected) {
		return e.New(e.CodeReleaseIncomplete, "lifecycle output observation count differs from its plan")
	}
	for _, want := range expected {
		got, ok := outputs[want.Name]
		if !ok || got.RecipeDigest != want.RecipeDigest || !equalFactoryStrings(got.Components, want.Components) {
			return e.New(e.CodeVerifyFailed, "lifecycle output %q changes its recipe or component binding", want.Name)
		}
	}
	compositionByName := make(map[string]CompositionPlan, len(compositions))
	for _, composition := range compositions {
		compositionByName[composition.Name] = composition
	}
	for _, planned := range clusters {
		composition, ok := compositionByName[planned.Name]
		if !ok || composition.Digest != planned.CompositionDigest {
			return e.New(e.CodeVerifyFailed, "cluster %q differs from its exact composition plan", planned.Name)
		}
		digest, err := composition.Composition.Digest()
		if err != nil || digest != planned.CompositionDigest {
			return e.New(e.CodeVerifyFailed, "cluster %q composition bytes differ from its identity", planned.Name)
		}
		for _, member := range planned.Members {
			output, ok := outputs[member.Output]
			if !ok {
				return e.New(e.CodeReleaseIncomplete, "cluster %q references missing output %q", planned.Name, member.Output)
			}
			var category string
			for _, recipe := range composition.Composition.Recipes {
				if recipe.RecipeDigest == output.RecipeDigest && containsFactoryStrings(recipe.Components, output.Components) {
					category = recipe.Category
					break
				}
			}
			if category == "" {
				return e.New(e.CodeVerifyFailed, "cluster %q output %q is not bound to an exact composition recipe", planned.Name, member.Output)
			}
			if member.Role == "" {
				if !containsFactoryString(composition.Composition.ClusterCategories, category) {
					return e.New(e.CodeVerifyFailed, "cluster %q output %q is not bound at cluster level", planned.Name, member.Output)
				}
				continue
			}
			matched := false
			for _, role := range composition.Composition.RoleBindings {
				if role.Role == member.Role && containsFactoryString(role.NodeLevelCategories, category) && containsFactoryStrings(role.Subset, output.Components) {
					matched = true
					break
				}
			}
			if !matched {
				return e.New(e.CodeVerifyFailed, "cluster %q output %q is not bound to role %q", planned.Name, member.Output, member.Role)
			}
		}
	}
	return nil
}

func containsFactoryString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsFactoryStrings(values, wanted []string) bool {
	for _, item := range wanted {
		if !containsFactoryString(values, item) {
			return false
		}
	}
	return true
}

func equalFactoryStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
