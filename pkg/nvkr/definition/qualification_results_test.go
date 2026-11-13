// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yaml "go.yaml.in/yaml/v3"
)

func validQualificationResults() *QualificationResults {
	return &QualificationResults{Mode: QualificationResultsModeTestGridGCS, GitHubEnvironment: "nvkr-results",
		BucketRef: "github:NVKR_RESULTS_BUCKET", WorkloadIdentityProviderRef: "github:NVKR_RESULTS_WIF_PROVIDER",
		ServiceAccountRef: "github:NVKR_RESULTS_SERVICE_ACCOUNT"}
}

func TestQualificationEnvironmentResults(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Environment)
		code e.Code
	}{
		{"declared", func(*Environment) {}, ""},
		{"absent", func(v *Environment) { v.QualificationResults = nil }, ""},
		{"development", func(v *Environment) { v.GitHubDevelopment = true }, e.CodeEngineCapability},
		{"another mode", func(v *Environment) { v.QualificationResults.Mode = "gcs" }, e.CodeInvalidCatalog},
		{"no Environment", func(v *Environment) { v.QualificationResults.GitHubEnvironment = "" }, e.CodeInvalidCatalog},
		{"upper-case Environment", func(v *Environment) { v.QualificationResults.GitHubEnvironment = "NVKR-results" }, e.CodeInvalidCatalog},
		{"the qualify job's Environment", func(v *Environment) { v.QualificationResults.GitHubEnvironment = "nvkr-release" }, e.CodeInvalidCatalog},
		{"the development qualify job's Environment", func(v *Environment) { v.QualificationResults.GitHubEnvironment = "nvkr-dev-release" }, e.CodeInvalidCatalog},
		{"the ARC Environment", func(v *Environment) { v.QualificationResults.GitHubEnvironment = "nvkr-arc" }, e.CodeInvalidCatalog},
		{"the development ARC Environment", func(v *Environment) { v.QualificationResults.GitHubEnvironment = "nvkr-dev-arc" }, e.CodeInvalidCatalog},
		{"the development cleanup Environment", func(v *Environment) { v.QualificationResults.GitHubEnvironment = "nvkr-dev-cleanup" }, e.CodeInvalidCatalog},
		{"empty reference", func(v *Environment) { v.QualificationResults.BucketRef = "" }, e.CodeInvalidCatalog},
		{"runner token", func(v *Environment) { v.QualificationResults.BucketRef = "github:GITHUB_TOKEN" }, e.CodeInvalidCatalog},
		{"lower-case reference", func(v *Environment) { v.QualificationResults.BucketRef = "github:nvkr_results_bucket" }, e.CodeInvalidCatalog},
		{"non-secret reference", func(v *Environment) { v.QualificationResults.BucketRef = "env:NVKR_RESULTS_BUCKET" }, e.CodeInvalidCatalog},
		{"duplicate references", func(v *Environment) {
			v.QualificationResults.ServiceAccountRef = v.QualificationResults.WorkloadIdentityProviderRef
		}, e.CodeInvalidCatalog},
		{"registry writer", func(v *Environment) { v.QualificationResults.BucketRef = ProductionRegistryWriteCredential }, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := Environment{QualificationResults: validQualificationResults()}
			test.edit(&value)
			err := value.validateQualificationResults()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v; want %s", err, test.code)
			}
		})
	}
}

func TestQualificationEnvironmentResultsClone(t *testing.T) {
	value := Environment{QualificationResults: validQualificationResults()}
	clone := value.Clone()
	clone.QualificationResults.BucketRef = "github:OTHER"
	if value.QualificationResults.BucketRef != "github:NVKR_RESULTS_BUCKET" {
		t.Fatal("clone shares the qualification results declaration")
	}
}

func TestQualificationEnvironmentResultsSecretRefs(t *testing.T) {
	bucket, provider, account := validQualificationResults().SecretRefs()
	if bucket != "NVKR_RESULTS_BUCKET" || provider != "NVKR_RESULTS_WIF_PROVIDER" || account != "NVKR_RESULTS_SERVICE_ACCOUNT" {
		t.Fatalf("got %q %q %q", bucket, provider, account)
	}
}

// Every protected Environment a committed workflow runs in, other than the
// results target itself, must be refused as a results target, so a newly
// generated Environment cannot be missing from reservedGitHubEnvironments.
func TestQualificationResultsReservesCommittedEnvironments(t *testing.T) {
	release, err := LoadEnvironment(t.Context(), "../../../inputs/environments/github-release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if release.QualificationResults == nil {
		t.Fatal("the release Environment declares no qualificationResults")
	}
	target := release.QualificationResults.GitHubEnvironment
	var workflows []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join("../../../.github/workflows", pattern))
		if err != nil {
			t.Fatal(err)
		}
		workflows = append(workflows, matches...)
	}
	if len(workflows) == 0 {
		t.Fatal("no committed workflows")
	}
	seen := map[string]bool{}
	for _, path := range workflows {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow struct {
			Jobs map[string]struct {
				Environment yaml.Node `yaml:"environment"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for job, definition := range workflow.Jobs {
			node := definition.Environment
			switch node.Kind {
			case 0:
				continue
			case yaml.ScalarNode:
				seen[node.Value] = true
			default:
				var named struct {
					Name string `yaml:"name"`
				}
				if err := node.Decode(&named); err != nil || named.Name == "" {
					t.Fatalf("%s job %s: unrecognized environment: %v", path, job, err)
				}
				seen[named.Name] = true
			}
		}
	}
	if !seen["nvkr-release"] || !seen[target] {
		t.Fatalf("committed workflows do not run the release and results Environments: %v", seen)
	}
	admitted := validQualificationResults()
	admitted.GitHubEnvironment = target
	if err := (Environment{QualificationResults: admitted}).validateQualificationResults(); err != nil {
		t.Fatalf("the results target itself is refused: %v", err)
	}
	for name := range seen {
		if name == target {
			continue
		}
		results := validQualificationResults()
		results.GitHubEnvironment = name
		environment := Environment{QualificationResults: results}
		if err := environment.validateQualificationResults(); err == nil {
			t.Errorf("committed workflow Environment %q is admitted as a results target", name)
		}
	}
}
