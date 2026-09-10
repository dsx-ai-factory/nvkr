package lifecycle

import (
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

const clusterRealizationSchema = "nkd.dev/cluster-realization/v1"

type releaseContent struct {
	Artifacts []Artifact `json:"artifacts"`
}

type clusterRealization struct {
	SchemaVersion     string                  `json:"schemaVersion"`
	Name              string                  `json:"name"`
	CompositionDigest string                  `json:"compositionDigest"`
	ContentSubject    string                  `json:"contentSubject"`
	Members           []clusterRealizedMember `json:"members"`
}

type clusterRealizedMember struct {
	Output         string   `json:"output"`
	ArtifactDigest string   `json:"artifactDigest"`
	RecipeDigest   string   `json:"recipeDigest"`
	Components     []string `json:"components"`
	Role           string   `json:"role,omitempty"`
}

func bindClusterRealizations(definition Definition, plan *factory.Plan, run *Run) (*Run, error) {
	if run == nil || run.Status != RunComplete {
		return nil, e.New(e.CodeGateUnmet, "cluster realization requires a complete Run")
	}
	content := releaseContent{Artifacts: cloneArtifacts(run.Observed)}
	sort.Slice(content.Artifacts, func(i, j int) bool { return content.Artifacts[i].Name < content.Artifacts[j].Name })
	contentSubject, err := canonical.Digest(canonical.KindReleaseContent, content)
	if err != nil {
		return nil, err
	}
	copyRun := *run
	copyRun.Expected = cloneRequired(run.Expected)
	copyRun.Observed = cloneArtifacts(run.Observed)
	copyRun.ContentSubject = contentSubject
	copyRun.Clusters = nil
	boundOutputs := make([]factory.LifecycleBoundOutput, 0, len(run.Observed))
	for _, artifact := range run.Observed {
		if artifact.Role != "" {
			return nil, e.New(e.CodeVerifyFailed, "output %q carries a global role outside its cluster view", artifact.Name)
		}
		boundOutputs = append(boundOutputs, factory.LifecycleBoundOutput{Name: artifact.Name, Kind: artifact.Kind,
			Digest: artifact.Digest, RecipeDigest: artifact.RecipeDigest, Components: append([]string(nil), artifact.Components...)})
	}
	if err := factory.ValidateLifecycleRealizations(plan.Compositions, plan.LifecycleOutputs, plan.LifecycleClusters, boundOutputs); err != nil {
		return nil, err
	}
	compositions := map[string]factory.CompositionPlan{}
	for _, composition := range plan.Compositions {
		compositions[composition.Name] = composition
	}
	outputs := map[string]DefinitionOutput{}
	for _, output := range definition.Outputs {
		outputs[output.Name] = output
	}
	observed := map[string]Artifact{}
	for _, artifact := range run.Observed {
		observed[artifact.Name] = artifact
	}
	for _, view := range definition.Clusters {
		composition, ok := compositions[view.Name]
		if !ok {
			return nil, e.New(e.CodeReleaseIncomplete, "cluster %q has no exact compiled composition", view.Name)
		}
		recomputed, err := composition.Composition.Digest()
		if err != nil || recomputed != composition.Digest {
			return nil, e.New(e.CodeUnverifiedRef, "cluster %q compiled composition identity is not exact", view.Name)
		}
		realization := clusterRealization{SchemaVersion: clusterRealizationSchema, Name: view.Name,
			CompositionDigest: composition.Digest, ContentSubject: contentSubject}
		for _, member := range view.Members {
			output := outputs[member.Output]
			artifact, found := observed[member.Output]
			if !found {
				return nil, e.New(e.CodeReleaseIncomplete, "cluster %q output %q was not observed", view.Name, member.Output)
			}
			if !equalStrings(artifact.Components, output.Components) {
				return nil, e.New(e.CodeVerifyFailed, "cluster %q output %q changes its role or component mapping", view.Name, member.Output)
			}
			realization.Members = append(realization.Members, clusterRealizedMember{Output: member.Output,
				ArtifactDigest: artifact.Digest, RecipeDigest: artifact.RecipeDigest,
				Components: append([]string(nil), artifact.Components...), Role: member.Role})
		}
		if !canonical.IsDigest(realization.CompositionDigest) || !canonical.IsDigest(realization.ContentSubject) || len(realization.Members) == 0 {
			return nil, e.New(e.CodeVerifyFailed, "cluster %q realization does not bind exact composition and content identities", view.Name)
		}
		if _, err := canonical.Digest(canonical.KindClusterRealization, realization); err != nil {
			return nil, err
		}
		copyRun.Clusters = append(copyRun.Clusters, realization)
	}
	return &copyRun, nil
}
