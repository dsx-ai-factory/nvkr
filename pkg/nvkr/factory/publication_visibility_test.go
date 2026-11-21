// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationVisibilityBindsPlanAndBundle(t *testing.T) {
	base := referenceTestPlan(t)
	environment := base.Environment.Clone()
	environment.Publication.AllowPublicPackages = true
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), base.Definition, environment, stack, base.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	before, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	after, err := plan.Digest()
	if err != nil || before == after || base.EnvironmentDigest == plan.EnvironmentDigest {
		t.Fatalf("visibility selection did not change Environment and Plan identities: %v", err)
	}
	if base.DefinitionDigest != plan.DefinitionDigest || len(base.Builds) != len(plan.Builds) {
		t.Fatal("visibility changed Definition or build membership")
	}
	for i := range base.Builds {
		if base.Builds[i].BuildDigest != plan.Builds[i].BuildDigest {
			t.Fatal("transport visibility changed logical Build identity")
		}
	}
	for _, candidate := range []*Plan{base, plan} {
		bundle, err := RenderGitHub(t.Context(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateGitHubBundle(t.Context(), candidate, bundle); err != nil {
			t.Fatal(err)
		}
		other := plan
		if candidate == plan {
			other = base
		}
		assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), other, bundle), e.CodePipelineDrift)
	}
}

func TestPublicationVisibilityFromPlanPrecedesPayloadAndOutput(t *testing.T) {
	t.Setenv("NVKR_REGISTRY_USER", "fixture-user")
	t.Setenv("NVKR_CREDENTIAL", "fixture-token")
	t.Setenv(githubRepositoryTokenEnv, "fixture-token")
	for _, public := range []bool{false, true} {
		t.Run(fmt.Sprint(public), func(t *testing.T) {
			base := referenceTestPlan(t)
			environment := base.Environment.Clone()
			environment.Publication.AllowPublicPackages = public
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := Compile(t.Context(), base.Definition, environment, stack, base.GeneratorVersion)
			if err != nil {
				t.Fatal(err)
			}
			compiled.DefinitionPath, compiled.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
			plan, artifacts, _ := referenceMetadataFixtureForPlan(t, compiled)
			node := planNode(plan, NodePublish+"/"+outputPublicationRoute(plan.LifecycleOutputs[0].Name))
			for _, id := range node.Outputs {
				artifact := artifacts[id]
				artifact.Path = filepath.Join(t.TempDir(), "must-not-be-written")
				artifacts[id] = artifact
			}
			calls := 0
			err = executePublishWithAdmission(t.Context(), plan, node, artifacts, io.Discard,
				func(_ context.Context, _ string, _ []string, opts cistore.PublicationOptions) error {
					calls++
					if !opts.PlatformToken || opts.AllowPublicPackages != public {
						t.Fatal("execution did not derive admission policy from Plan and target")
					}
					return e.New(e.CodeTrustPolicyMissing, "fixture policy refused")
				})
			assertFactoryErrorCode(t, err, e.CodeTrustPolicyMissing)
			if calls != 1 {
				t.Fatalf("admission calls = %d", calls)
			}
			for _, id := range node.Outputs {
				if _, err := os.Lstat(artifacts[id].Path); !os.IsNotExist(err) {
					t.Fatalf("refused publication emitted output: %v", err)
				}
			}
		})
	}
}
