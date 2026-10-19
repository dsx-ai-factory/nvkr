// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubProductionBootstrapDispatchAdmission(t *testing.T) {
	for _, fixture := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"build only", func(t *testing.T) *Plan { return buildOnlyPlan(t, "ubuntu-24.04") }},
		{"publication", githubTestPlan},
		{"named runtime", func(t *testing.T) *Plan {
			return runtimeTransportPlan(t, nativeGitHubTestPlan(t, map[string]string{
				"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm",
			}))
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plan := fixture.plan(t)
			if plan.Environment.GitHubDevelopment || len(githubCatalogSourceEnvironment(plan)) != 0 {
				t.Fatal("fixture must exercise production without catalog credentials")
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			bootstraps := 0
			for _, id := range sortedKeys(document.Jobs) {
				if !strings.HasPrefix(id, "build-runner-") {
					continue
				}
				bootstraps++
				t.Run(id, func(t *testing.T) {
					want := "${{ github.event_name == 'workflow_dispatch' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch) }}"
					if document.Jobs[id].If != want {
						t.Fatalf("helper can run outside production dispatch admission: %q", document.Jobs[id].If)
					}
					for _, mutation := range []struct{ name, condition string }{
						{"missing", ""},
						{"any branch", "${{ github.event_name == 'workflow_dispatch' && startsWith(github.ref, 'refs/heads/') }}"},
						{"unadmitted feature", "${{ github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/feature/unadmitted' }}"},
						{"tag", "${{ github.event_name == 'workflow_dispatch' && github.ref == 'refs/tags/main' }}"},
						{"push", "${{ github.event_name == 'push' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch) }}"},
					} {
						t.Run(mutation.name, func(t *testing.T) {
							changed := mutateGitHubBundleYAML(t, bundle, bundle.WorkflowPath, func(root *yamlv3.Node) {
								job := githubTestYAMLField(t, root, "jobs", id)
								if mutation.condition == "" {
									githubYAMLRemove(job, "if")
								} else {
									githubTestYAMLField(t, job, "if").Value = mutation.condition
								}
							})
							assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeInvalidCatalog)
						})
					}
				})
			}
			if bootstraps == 0 || (plan.Environment.GitHubRuntimePlan && bootstraps < 2) {
				t.Fatalf("fixture lost required helper coverage: %d", bootstraps)
			}
		})
	}
}
