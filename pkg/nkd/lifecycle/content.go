package lifecycle

import (
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

type releaseContent struct {
	Artifacts []Artifact `json:"artifacts"`
}

func bindReleaseContent(plan *factory.Plan, run *Run) (*Run, error) {
	if run == nil || run.Status != RunComplete {
		return nil, e.New(e.CodeGateUnmet, "content binding requires a complete Run")
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
	boundOutputs := make([]factory.LifecycleBoundOutput, 0, len(run.Observed))
	for _, artifact := range run.Observed {
		boundOutputs = append(boundOutputs, factory.LifecycleBoundOutput{Name: artifact.Name, Kind: artifact.Kind,
			Digest: artifact.Digest, BuildDigest: artifact.BuildDigest, Components: append([]string(nil), artifact.Components...)})
	}
	if err := factory.ValidateLifecycleOutputs(plan.LifecycleOutputs, boundOutputs); err != nil {
		return nil, err
	}
	return &copyRun, nil
}
