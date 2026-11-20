// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/Masterminds/semver/v3"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// SourceRecipe derives command argv and its artifact path from the selected
// source pin. The declaration remains in resolved snapshots for source substitution.
// CommitVersion is an explicitly authored packaging stream for commit-only pins;
// it is never inferred from a hash or claimed to be an upstream release tag.
type SourceRecipe struct {
	BuildCommand  []string `json:"buildCommand"`
	ArtifactPath  string   `json:"artifactPath"`
	CommitVersion string   `json:"commitVersion,omitempty"`
}

var recipeTag = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+~-]*$`)

type sourceRecipeData struct {
	Tag, Arch      string
	component      Component
	releaseVersion *string
}

func (data sourceRecipeData) ReleaseVersion() (string, error) {
	version, err := data.component.RecipeVersion()
	if err == nil && data.releaseVersion != nil {
		*data.releaseVersion = version
	}
	return version, err
}

func cloneSourceRecipe(recipe *SourceRecipe) *SourceRecipe {
	if recipe == nil {
		return nil
	}
	clone := *recipe
	clone.BuildCommand = slices.Clone(recipe.BuildCommand)
	return &clone
}

// RecipeVersion reports the exact normalized tag or explicitly authored commit
// stream used by an opted-in producer. Commit-only pins without that declaration
// fail closed; ReleaseVersion elsewhere retains its strict tag-only behavior.
func (c Component) RecipeVersion() (string, error) {
	if c.SourceRecipe == nil {
		return "", nil
	}
	if c.Tag == c.Commit || fullSHA.MatchString(c.Tag) {
		version := c.SourceRecipe.CommitVersion
		if version == "" {
			return "", e.New(e.CodeInvalidCatalog, "commit-only source recipe requires an explicit commitVersion")
		}
		if _, err := semver.StrictNewVersion(version); err != nil {
			return "", e.Wrap(e.CodeInvalidCatalog, err, "invalid explicit commit packaging version")
		}
		return version, nil
	}
	version, err := (sourceMetadataData{stampContext(Pin{Tag: c.Tag, Commit: c.Commit})}).ReleaseVersion()
	if err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "source recipe requires a semantic release tag")
	}
	return version, nil
}

// RenderedRecipeVersion reports the packaging version actually used by a recipe.
// Tag-only recipes return an empty version; their exact source tag remains separate.
func (c Component) RenderedRecipeVersion() (string, error) {
	var version string
	_, _, err := c.renderSourceRecipe(&version)
	if err != nil {
		return "", err
	}
	return version, nil
}

func (c Component) renderedSourceRecipe() ([]string, string, error) {
	return c.renderSourceRecipe(nil)
}

func (c Component) renderSourceRecipe(releaseVersion *string) ([]string, string, error) {
	recipe := c.SourceRecipe
	if recipe == nil {
		return c.BuildCommand, c.ArtifactPath, nil
	}
	if !c.FromSource || c.Prebuilt != nil || (c.BuilderType != "" && c.BuilderType != BuilderOCIHermetic && c.BuilderType != BuilderBuildKit && c.BuilderType != BuilderNativeGo) || len(c.BuildCommandByPlatform) != 0 {
		return nil, "", e.New(e.CodeInvalidCatalog, "sourceRecipe requires one source-command argv contract")
	}
	if len(recipe.BuildCommand) == 0 || len(recipe.BuildCommand) > 128 || recipe.ArtifactPath == "" || !recipeTag.MatchString(c.Tag) {
		return nil, "", e.New(e.CodeInvalidCatalog, "sourceRecipe requires bounded argv, artifact path and a safe source tag")
	}
	if recipe.CommitVersion != "" {
		if _, err := semver.StrictNewVersion(recipe.CommitVersion); err != nil {
			return nil, "", e.Wrap(e.CodeInvalidCatalog, err, "invalid commit packaging stream")
		}
	}
	// Arch stays an existing runtime token. No shell expansion is introduced.
	// Resolve a release version only when the declaration uses it: Tag alone can
	// describe an exact commit without inventing a semantic packaging version.
	data := sourceRecipeData{Tag: c.Tag, Arch: "{{.Arch}}", component: c, releaseVersion: releaseVersion}
	render := func(raw string) (string, error) {
		if len(raw) > 4096 {
			return "", e.New(e.CodeInvalidCatalog, "source recipe value exceeds byte limit")
		}
		t, err := template.New("source-recipe").Option("missingkey=error").Parse(raw)
		if err != nil {
			return "", e.Wrap(e.CodeInvalidCatalog, err, "invalid source recipe template")
		}
		var out metadataBuffer
		if err := t.Execute(&out, data); err != nil {
			return "", e.Wrap(e.CodeInvalidCatalog, err, "rendering source recipe")
		}
		if strings.ContainsRune(out.String(), '\x00') {
			return "", e.New(e.CodeInvalidCatalog, "source recipe contains NUL")
		}
		return out.String(), nil
	}
	command := make([]string, len(recipe.BuildCommand))
	var err error
	for i, arg := range recipe.BuildCommand {
		command[i], err = render(arg)
		if err != nil {
			return nil, "", err
		}
	}
	artifact, err := render(recipe.ArtifactPath)
	return command, artifact, err
}

func (c *Component) resolveSourceRecipe() error {
	command, artifact, err := c.renderedSourceRecipe()
	if err != nil {
		return err
	}
	c.BuildCommand, c.ArtifactPath = command, artifact
	return nil
}

func (c Component) validateSourceRecipe() error {
	if c.SourceRecipe == nil {
		return nil
	}
	command, artifact, err := c.renderedSourceRecipe()
	if err != nil {
		return err
	}
	if !slices.Equal(command, c.BuildCommand) || artifact != c.ArtifactPath {
		return e.New(e.CodeInvalidCatalog, "source recipe values differ from the selected pin")
	}
	return nil
}
