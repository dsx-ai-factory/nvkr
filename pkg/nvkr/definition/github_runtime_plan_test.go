// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubRuntimePlanAdmission(t *testing.T) {
	for _, test := range []struct {
		name, bundle                    string
		runtime, development, overrides bool
		code                            e.Code
	}{
		{name: "existing default"},
		{name: "existing named", bundle: "project"},
		{name: "runtime named", bundle: "project", runtime: true},
		{name: "runtime default", runtime: true, code: e.CodeEngineCapability},
		{name: "runtime development", bundle: "project", runtime: true, development: true, code: e.CodeEngineCapability},
		{name: "explicit runtime development", runtime: true, development: true, code: e.CodeEngineCapability},
		{name: "development overridable sources", development: true, overrides: true},
		{name: "explicit runtime with overridable sources", runtime: true, development: true, overrides: true, code: e.CodeEngineCapability},
		{name: "local overridable sources", overrides: true},
		{name: "production overridable sources", bundle: "project", runtime: true, overrides: true, code: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			var overridable *Overridable
			if test.overrides {
				overridable = &Overridable{Sources: map[string]OverridableSource{"nvke": {Repository: "https://github.com/dsx-ai-factory/nvke"}}}
			}
			env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
				Metadata: Metadata{Name: "test"}, UtilityPlatform: "linux/amd64",
				UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64),
				GitHubBundle:     test.bundle, GitHubRuntimePlan: test.runtime, GitHubDevelopment: test.development,
				Overridable: overridable}
			err := env.Validate()
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
}

func TestGitHubRuntimePlanBindsEnvironmentIdentity(t *testing.T) {
	env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
		Metadata: Metadata{Name: "test"}, UtilityPlatform: "linux/amd64",
		UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), GitHubBundle: "project"}
	before, err := env.Digest()
	if err != nil {
		t.Fatal(err)
	}
	clone := env.Clone()
	clone.GitHubRuntimePlan = true
	after, err := clone.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after || env.GitHubRuntimePlan {
		t.Fatal("runtime planning did not bind identity and preserve clone isolation")
	}
}
