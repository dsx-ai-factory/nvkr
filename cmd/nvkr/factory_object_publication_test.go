// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestGeneratedRunNodeDispatchesRequiredObjectPublication(t *testing.T) {
	fixture, _, _ := githubActionDataCommandFixture(t)
	var base factory.Plan
	if err := canonical.UnmarshalExact(fixture, &base); err != nil {
		t.Fatal(err)
	}
	base.Definition.CosignTool = "cosign"
	base.CatalogSnapshot.Tools = map[string]catalog.ToolPin{"cosign": {Format: "tar-gzip", URL: "https://example.invalid/cosign.tar.gz", Platform: "linux/amd64", ArchiveDigest: "sha256:" + strings.Repeat("1", 64), ExecutablePath: "cosign", ExecutableDigest: "sha256:" + strings.Repeat("2", 64)}}
	environment := base.Environment.Clone()
	environment.APIVersion = definition.EnvironmentObjectStoresAPIVersion
	environment.Signing = &definition.Signing{Mode: "cosign", Identity: "^https://github.com/example/nvkr/.*$", Issuer: "https://token.actions.githubusercontent.com"}
	environment.Publication = &definition.Publication{Mode: "per-output-v1", RepositoryPrefix: "ghcr.io/example/nvkr", CredentialRef: "github:REGISTRY"}
	environment.Publication.ObjectStores = []definition.ObjectStoreDestination{{Name: "customer", Provider: "s3", S3: &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-artifacts", Prefix: "products/nvke", ExpectedBucketOwner: "123456789012", RoleRef: "github:S3_PUBLISH_ROLE"}}}
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), base.Definition, environment, stack, base.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			original := executeGitHubObjectPublication
			t.Cleanup(func() { executeGitHubObjectPublication = original })
			called := false
			executeGitHubObjectPublication = func(_ context.Context, got *factory.Plan, node, version string, _ factory.GitHubStepOptions) error {
				called = true
				if got == nil || node != "publish-object/customer" || version != factoryGeneratorVersion() {
					t.Fatal("CLI changed frozen publication dispatch")
				}
				if failure {
					return e.New(e.CodeUnavailable, "synthetic publication failure")
				}
				return nil
			}
			_, err := invokeLifecycleCLI(t, "factory", "github", "run-node", "--plan", path, "--node", "publish-object/customer")
			if !called {
				t.Fatal("normal generated command did not delegate object publication")
			}
			if failure {
				assertLifecycleCommandCode(t, err, e.CodeUnavailable)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
