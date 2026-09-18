package pipeline

import (
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// GitHubActionRefs contains the catalog references admitted by the adapter.
type GitHubActionRefs struct{ Checkout, Upload, Download, SetupGo string }

// ResolveGitHubActions admits the action interfaces used by the GitHub renderer.
// Only supported repositories and major versions live in code; releases and
// commits belong to the catalog and are bound into each generated Run.
func ResolveGitHubActions(pins map[string]catalog.ActionPin) (GitHubActionRefs, error) {
	var refs GitHubActionRefs
	for _, requirement := range []struct {
		name, major string
		ref         *string
	}{
		{"checkout", "7", &refs.Checkout}, {"download-artifact", "8", &refs.Download},
		{"setup-go", "7", &refs.SetupGo}, {"upload-artifact", "7", &refs.Upload},
	} {
		pin, ok := pins[requirement.name]
		if !ok {
			return GitHubActionRefs{}, e.New(e.CodeUnpinnedSource, "catalog is missing GitHub action %q", requirement.name)
		}
		if err := pin.Validate(); err != nil {
			return GitHubActionRefs{}, err
		}
		if pin.Repo != "actions/"+requirement.name || !strings.HasPrefix(pin.Version, "v"+requirement.major+".") {
			return GitHubActionRefs{}, e.New(e.CodeEngineCapability, "GitHub adapter requires actions/%s major %s; got %s %s", requirement.name, requirement.major, pin.Repo, pin.Version)
		}
		*requirement.ref = pin.Uses()
	}
	return refs, nil
}
