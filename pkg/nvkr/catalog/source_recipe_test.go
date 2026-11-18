// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSourceRecipeVersions(t *testing.T) {
	for _, tc := range []struct{ tag, commitVersion, want string }{
		{"v0.23.0", "", "0.23.0"}, {"0.23.0", "", "0.23.0"},
		{"v0.24.0-rc.2", "", "0.24.0-rc.2"}, {"cli/v1.2.3", "", "1.2.3"},
		{strings.Repeat("a", 40), "", ""}, {strings.Repeat("a", 40), "0.23.0", "0.23.0"},
		{"main", "", ""}, {"v0.23.0;echo", "", ""}, {"v0.23.0", "invalid", ""},
	} {
		t.Run(tc.tag+tc.commitVersion, func(t *testing.T) {
			c := Component{FromSource: true, Tag: tc.tag, Commit: strings.Repeat("a", 40), SourceRecipe: &SourceRecipe{
				BuildCommand: []string{"package", "--version={{.ReleaseVersion}}", "--app-version={{.Tag}}", "{{.Arch}}"},
				ArtifactPath: "charts/controller-{{.ReleaseVersion}}.tgz", CommitVersion: tc.commitVersion,
			}}
			err := c.resolveSourceRecipe()
			if tc.want == "" {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"package", "--version=" + tc.want, "--app-version=" + tc.tag, "{{.Arch}}"}
			if !slices.Equal(c.BuildCommand, want) || c.ArtifactPath != "charts/controller-"+tc.want+".tgz" {
				t.Fatalf("wrong recipe: %+v", c)
			}
			if err := c.validateSourceRecipe(); err != nil {
				t.Fatal(err)
			}
			c.BuildCommand[1] = "--version=stale"
			assertCatalogContractCode(t, c.validateSourceRecipe(), e.CodeInvalidCatalog)
		})
	}
}

func TestSourceRecipeRejectsInvalidTemplates(t *testing.T) {
	for _, raw := range []string{"{{.Unknown}}", "{{", "arg\x00", strings.Repeat("x", 4097)} {
		t.Run(raw[:min(len(raw), 20)], func(t *testing.T) {
			c := Component{FromSource: true, Tag: "v1.2.3", SourceRecipe: &SourceRecipe{BuildCommand: []string{raw}, ArtifactPath: "result"}}
			assertCatalogContractCode(t, c.resolveSourceRecipe(), e.CodeInvalidCatalog)
		})
	}
}

func TestSourceRecipeTagOnlyCommitDoesNotInferVersion(t *testing.T) {
	commit := strings.Repeat("a", 40)
	c := Component{FromSource: true, Tag: commit, Commit: commit, SourceRecipe: &SourceRecipe{BuildCommand: []string{"make", "VERSION={{.Tag}}"}, ArtifactPath: "agent.deb"}}
	if err := c.resolveSourceRecipe(); err != nil {
		t.Fatal(err)
	}
	if c.BuildCommand[1] != "VERSION="+commit {
		t.Fatal("lost exact commit tag")
	}
	_, err := c.RecipeVersion()
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
}
