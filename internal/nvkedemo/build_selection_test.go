// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSelectedBuildRequiresCompleteLocalSelection(t *testing.T) {
	for _, row := range []struct {
		name, definition, environment, nvkr string
		qualification                       bool
		code                                e.Code
	}{
		{name: "default inputs"},
		{name: "explicit native inputs", definition: "definition.json", environment: "environment.json", nvkr: "/native/nvkr"},
		{name: "missing executable", definition: "definition.json", environment: "environment.json", code: e.CodeUsage},
		{name: "missing environment", definition: "definition.json", nvkr: "/native/nvkr", code: e.CodeUsage},
		{name: "missing definition", environment: "environment.json", nvkr: "/native/nvkr", code: e.CodeUsage},
		{name: "control character", definition: "definition\n.json", environment: "environment.json", nvkr: "/native/nvkr", code: e.CodeUsage},
		{name: "qualification stays separate", definition: "definition.json", environment: "environment.json", nvkr: "/native/nvkr", qualification: true, code: e.CodeEngineCapability},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := validateSelectedBuild(row.definition, row.environment, row.nvkr, row.qualification)
			var coded *e.Error
			if row.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &coded) || coded.Code != row.code {
				t.Fatalf("error=%v, want %s", err, row.code)
			}
		})
	}
}

func TestSelectedBuildUsesPlanOwnerAndExactInvocation(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	plan, _ := nightlyCompiledFixture(t, profile, build)
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "command failure", "invalid plan"} {
		t.Run(mode, func(t *testing.T) {
			repo := privateTestDir(t)
			nvkr := filepath.Join(repo, "nvkr")
			body := "#!/bin/sh\nset -eu\n[ \"$1 $2\" = 'run plan' ]\ncat \"$PLAN_FIXTURE\"\n"
			data := raw
			if mode == "command failure" {
				body = "#!/bin/sh\nexit 23\n"
			}
			if mode == "invalid plan" {
				data = []byte("{}")
			}
			fixture := filepath.Join(repo, "fixture.json")
			if err := os.WriteFile(fixture, data, 0600); err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, nvkr, []byte(body), 0700)
			cfg := BuildConfig{Repository: repo, Definition: filepath.Join(repo, "definition.json"), Environment: filepath.Join(repo, "environment.json"), NVKR: nvkr, AttemptPrefix: "fresh", Jobs: 2}
			runner := Runner{Env: []string{"PATH=/usr/bin:/bin", "PLAN_FIXTURE=" + fixture}}
			err := prepareSelectedBuild(t.Context(), runner, cfg)
			root := filepath.Join(repo, ".nvkr/laptop-bootstrap/fresh")
			invocation := filepath.Join(root, "build-invocation.json")
			if mode != "success" {
				if err == nil {
					t.Fatal("invalid preparation passed")
				}
				if _, err := os.Stat(invocation); !os.IsNotExist(err) {
					t.Fatalf("failed preparation wrote an invocation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			args, err := preparedBuildInvocation(cfg)
			if err != nil || !slices.Equal(args, selectedBuildInvocation(cfg)) {
				t.Fatalf("args=%v, error=%v", args, err)
			}
			args[4] = "foreign-definition.json"
			altered, _ := json.Marshal(args)
			if err := os.WriteFile(invocation, altered, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := preparedBuildInvocation(cfg); err == nil {
				t.Fatal("changed Definition was accepted")
			}
		})
	}
}

func TestSelectedBuildConfigurationKeepsInputPaths(t *testing.T) {
	file := DemoFile{Version: 1, Build: BuildFile{Repo: "/repo", Attempt: "fresh", Jobs: 2, Definition: "inputs/definition.json", Environment: "inputs/environment.json", NVKR: "/native/nvkr"}}
	cfg, err := file.BuildConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Definition != file.Build.Definition || cfg.Environment != file.Build.Environment || cfg.NVKR != file.Build.NVKR {
		t.Fatalf("selection lost: %+v", cfg)
	}
	file.Build.Environment = ""
	if _, err := file.BuildConfig(nil); err == nil {
		t.Fatal("partial selection accepted")
	}
}

func TestSelectedBuildResolvesInputsWithinRepository(t *testing.T) {
	definitionBytes, err := os.ReadFile("../../inputs/definitions/demo/arm64-upstream-local-kvm.json")
	if err != nil {
		t.Fatal(err)
	}
	environmentBytes, err := os.ReadFile("../../inputs/environments/local-native-mac.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"relative", "absolute", "outside", "symlink outside", "publication"} {
		t.Run(mode, func(t *testing.T) {
			repo := privateTestDir(t)
			definition := filepath.Join(repo, "definition.json")
			environment := filepath.Join(repo, "environment.json")
			tool := filepath.Join(repo, "nvkr")
			if err := os.WriteFile(definition, definitionBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "publication" {
				var doc map[string]any
				if err := json.Unmarshal(environmentBytes, &doc); err != nil {
					t.Fatal(err)
				}
				doc["publication"] = map[string]any{"repositoryPrefix": "registry.example/team", "anonymous": true}
				selected, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(environment, selected, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(environment, environmentBytes, 0600); err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, tool, []byte("#!/bin/sh\nexit 99\n"), 0700)
			cfg := BuildConfig{Definition: "definition.json", Environment: "environment.json", NVKR: "nvkr"}
			if mode == "absolute" {
				cfg.Definition, cfg.Environment, cfg.NVKR = definition, environment, tool
			}
			if mode == "outside" || mode == "symlink outside" {
				outside := filepath.Join(privateTestDir(t), "definition.json")
				if err := os.WriteFile(outside, definitionBytes, 0600); err != nil {
					t.Fatal(err)
				}
				cfg.Definition = outside
				if mode == "symlink outside" {
					link := filepath.Join(repo, "linked-definition.json")
					if err := os.Symlink(outside, link); err != nil {
						t.Fatal(err)
					}
					cfg.Definition = link
				}
			}
			err := resolveSelectedBuild(t.Context(), repo, &cfg)
			if mode == "outside" || mode == "symlink outside" || mode == "publication" {
				var coded *e.Error
				want := e.CodeUsage
				if mode == "publication" {
					want = e.CodeEngineCapability
				}
				if !errors.As(err, &coded) || coded.Code != want {
					t.Fatalf("error=%v, want %s", err, want)
				}
			} else if err != nil || cfg.Definition != "definition.json" || cfg.Environment != "environment.json" || cfg.NVKR != tool {
				t.Fatalf("selection=%+v, error=%v", cfg, err)
			}
			if _, err := os.Stat(filepath.Join(repo, ".nvkr")); !os.IsNotExist(err) {
				t.Fatal("input admission created build state")
			}
		})
	}
}
