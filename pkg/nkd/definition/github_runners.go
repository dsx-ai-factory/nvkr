package definition

import (
	"maps"
	"slices"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// GitHubRunnerPlatform identifies the supported generic hosted Linux routes.
// A label declares the requested platform; execution must still observe it.
func GitHubRunnerPlatform(runner string) (string, error) {
	switch runner {
	case "ubuntu-latest", "ubuntu-22.04", "ubuntu-24.04":
		return "linux/amd64", nil
	case "ubuntu-22.04-arm", "ubuntu-24.04-arm":
		return "linux/arm64", nil
	default:
		return "", e.New(e.CodeEngineCapability, "GitHub runner %q is not a supported generic hosted Linux route", runner)
	}
}

func validateGitHubRunners(runners map[string]string, runsOn string) error {
	if runners == nil {
		return nil
	}
	if len(runners) == 0 || len(runners) > 2 || runsOn != "" {
		return e.New(e.CodeInvalidCatalog, "githubRunners requires one or two platform routes and cannot be combined with runsOn")
	}
	for _, platform := range slices.Sorted(maps.Keys(runners)) {
		runner := runners[platform]
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return e.New(e.CodeEngineCapability, "GitHub execution platform %q is unsupported", platform)
		}
		actual, err := GitHubRunnerPlatform(runner)
		if err != nil {
			return err
		}
		if actual != platform {
			return e.New(e.CodeEngineCapability, "GitHub runner %q provides %s, not requested %s", runner, actual, platform)
		}
	}
	return nil
}
