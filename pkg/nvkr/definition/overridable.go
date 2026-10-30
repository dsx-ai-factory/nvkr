// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Overridable declares the from-source repositories that one local or
// development Run may build at an operator-selected version constraint or full
// commit instead of the reviewed pins. Undeclared repositories keep their pins.
type Overridable struct {
	// Sources maps each override name to the repository it overrides. The name
	// is the development dispatch input and the local --source-override name.
	Sources map[string]OverridableSource `json:"sources"`
}

// OverridableSource names one canonical from-source repository.
type OverridableSource struct {
	Repository string `json:"repository"`
}

// GitHubDispatchInputLimit is GitHub's maximum workflow_dispatch input count.
const GitHubDispatchInputLimit = 25

// overrideName is GitHub's dispatch input grammar, bounded for readability, so
// one name works as a dispatch input and as a command-line flag value.
var overrideName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// UsesGitHubRuntimePlan reports whether generated GitHub execution compiles its
// Plan at dispatch. Named production bundles select it with githubRuntimePlan;
// development implies it whenever sources are overridable.
func (environment Environment) UsesGitHubRuntimePlan() bool {
	return environment.GitHubRuntimePlan || (environment.GitHubDevelopment && environment.Overridable != nil)
}

func (environment Environment) validateOverridable() error {
	overridable := environment.Overridable
	if overridable == nil {
		return nil
	}
	if len(overridable.Sources) == 0 {
		return e.New(e.CodeInvalidCatalog, "overridable requires at least one source")
	}
	if environment.GitHubBundle != "" || environment.GitHubRuntimePlan || environment.GitHubRuntimeSource != nil {
		return e.New(e.CodeEngineCapability, "overridable sources are for local and development Runs; development implies githubRuntimePlan")
	}
	if environment.GitHubDevelopment && len(overridable.Sources) > GitHubDispatchInputLimit {
		return e.New(e.CodeEngineCapability, "overridable declares %d sources; GitHub admits at most %d dispatch inputs", len(overridable.Sources), GitHubDispatchInputLimit)
	}
	owners := make(map[string]string, len(overridable.Sources))
	for name, source := range overridable.Sources {
		if !overrideName.MatchString(name) {
			return e.New(e.CodeInvalidCatalog, "overridable source %q must start with a letter or _ and contain only letters, digits, - or _", name)
		}
		if !canonicalSourceRepository(source.Repository) {
			return e.New(e.CodeInvalidCatalog, "overridable source %q requires a canonical HTTPS repository without credentials, query or a .git suffix", name)
		}
		if owner, exists := owners[source.Repository]; exists {
			return e.New(e.CodeInvalidCatalog, "overridable sources %q and %q select the same repository", min(owner, name), max(owner, name))
		}
		owners[source.Repository] = name
	}
	return nil
}

func canonicalSourceRepository(repository string) bool {
	parsed, err := url.Parse(repository)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery &&
		parsed.Fragment == "" && parsed.RawPath == "" && strings.Trim(parsed.Path, "/") != "" && parsed.Path == path.Clean(parsed.Path) &&
		!strings.HasSuffix(parsed.Path, ".git") && !strings.ContainsAny(parsed.Path, " \t\r\n\\") && parsed.String() == repository
}
