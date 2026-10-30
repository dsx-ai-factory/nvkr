// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"fmt"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestOverridablePolicy(t *testing.T) {
	const nvke = "https://github.com/dsx-ai-factory/nvke"
	sources := func(values map[string]string) *Overridable {
		declared := map[string]OverridableSource{}
		for name, repository := range values {
			declared[name] = OverridableSource{Repository: repository}
		}
		return &Overridable{Sources: declared}
	}
	tooMany := map[string]string{}
	for i := 0; i <= GitHubDispatchInputLimit; i++ {
		tooMany[fmt.Sprintf("source%d", i)] = fmt.Sprintf("https://github.com/org/repo%d", i)
	}
	for _, test := range []struct {
		name string
		edit func(*Environment)
		code e.Code
	}{
		{"development", func(*Environment) {}, ""},
		{"local", func(v *Environment) { v.GitHubDevelopment = false }, ""},
		{"undeclared", func(v *Environment) { v.Overridable = nil }, ""},
		{"empty declaration", func(v *Environment) { v.Overridable = &Overridable{} }, e.CodeInvalidCatalog},
		{"explicit runtime Plan", func(v *Environment) { v.GitHubRuntimePlan = true }, e.CodeEngineCapability},
		{"named bundle", func(v *Environment) { v.GitHubDevelopment, v.GitHubBundle = false, "project" }, e.CodeEngineCapability},
		{"runtime source", func(v *Environment) {
			v.GitHubRuntimeSource = &GitHubRuntimeSource{Repository: nvke}
		}, e.CodeEngineCapability},
		{"too many dispatch inputs", func(v *Environment) { v.Overridable = sources(tooMany) }, e.CodeEngineCapability},
		{"many local sources", func(v *Environment) { v.GitHubDevelopment, v.Overridable = false, sources(tooMany) }, ""},
		{"URL as name", func(v *Environment) { v.Overridable = sources(map[string]string{nvke: nvke}) }, e.CodeInvalidCatalog},
		{"leading digit", func(v *Environment) { v.Overridable = sources(map[string]string{"1nvke": nvke}) }, e.CodeInvalidCatalog},
		{"dotted name", func(v *Environment) { v.Overridable = sources(map[string]string{"nvke.cli": nvke}) }, e.CodeInvalidCatalog},
		{"hyphen and underscore", func(v *Environment) { v.Overridable = sources(map[string]string{"_nvke-cli_2": nvke}) }, ""},
		{"git suffix", func(v *Environment) { v.Overridable = sources(map[string]string{"nvke": nvke + ".git"}) }, e.CodeInvalidCatalog},
		{"cleartext", func(v *Environment) {
			v.Overridable = sources(map[string]string{"nvke": "http://github.com/dsx-ai-factory/nvke"})
		}, e.CodeInvalidCatalog},
		{"credential", func(v *Environment) {
			v.Overridable = sources(map[string]string{"nvke": "https://user@github.com/dsx-ai-factory/nvke"})
		}, e.CodeInvalidCatalog},
		{"trailing slash", func(v *Environment) { v.Overridable = sources(map[string]string{"nvke": nvke + "/"}) }, e.CodeInvalidCatalog},
		{"no path", func(v *Environment) { v.Overridable = sources(map[string]string{"nvke": "https://github.com"}) }, e.CodeInvalidCatalog},
		{"missing repository", func(v *Environment) { v.Overridable = sources(map[string]string{"nvke": ""}) }, e.CodeInvalidCatalog},
		{"duplicate repository", func(v *Environment) {
			v.Overridable = sources(map[string]string{"nvke": nvke, "other": nvke})
		}, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := Environment{GitHubDevelopment: true,
				Overridable: sources(map[string]string{"nvke": nvke, "ovn": "https://github.com/ovn-kubernetes/ovn-kubernetes"})}
			test.edit(&value)
			err := value.validateOverridable()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v, want %s", err, test.code)
			}
		})
	}
	value := Environment{Overridable: sources(map[string]string{"nvke": nvke})}
	clone := value.Clone()
	clone.Overridable.Sources["nvke"] = OverridableSource{Repository: "https://github.com/other/repo"}
	if value.Overridable.Sources["nvke"].Repository != nvke {
		t.Fatal("clone shares overridable source declarations")
	}
}

func TestUsesGitHubRuntimePlan(t *testing.T) {
	overridable := &Overridable{Sources: map[string]OverridableSource{"nvke": {Repository: "https://github.com/dsx-ai-factory/nvke"}}}
	for _, test := range []struct {
		name        string
		environment Environment
		want        bool
	}{
		{"default", Environment{}, false},
		{"named production", Environment{GitHubBundle: "project", GitHubRuntimePlan: true}, true},
		{"development", Environment{GitHubDevelopment: true}, false},
		{"development with overridable sources", Environment{GitHubDevelopment: true, Overridable: overridable}, true},
		{"local with overridable sources", Environment{Overridable: overridable}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.environment.UsesGitHubRuntimePlan(); got != test.want {
				t.Fatalf("UsesGitHubRuntimePlan() = %v, want %v", got, test.want)
			}
		})
	}
}
