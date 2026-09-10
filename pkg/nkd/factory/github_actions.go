package factory

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type githubActionRefs pipeline.GitHubActionRefs

func resolveGitHubActions(pins map[string]catalog.ActionPin) (githubActionRefs, error) {
	refs, err := pipeline.ResolveGitHubActions(pins)
	return githubActionRefs(refs), err
}

func cloneActionPins(pins map[string]catalog.ActionPin) map[string]catalog.ActionPin {
	if len(pins) == 0 {
		return nil
	}
	out := make(map[string]catalog.ActionPin, len(pins))
	for name, pin := range pins {
		out[name] = pin
	}
	return out
}
