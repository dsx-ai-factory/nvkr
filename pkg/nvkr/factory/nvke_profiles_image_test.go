// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// These tests compile the actual catalog and apply the authenticated patches.
// Executing the pinned runtime and the chart materializer remains a separate
// external check; inspecting a Dockerfile alone does not establish runtime proof.
func TestControllerProfilesImageProducerContracts(t *testing.T) {
	for _, tc := range []struct {
		name, catalogPath, definitionPath, component, dockerfile string
		platforms                                                []string
	}{
		{"source fixtures", "nvke-controller/catalog", "nvke-controller/definition-profiles.yaml", "nvke-controller-profiles-image", "nvkr-controller-profiles/Dockerfile", []string{"linux/amd64", "linux/arm64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("../../../definitions", tc.catalogPath)))
			if err != nil {
				t.Fatal(err)
			}
			stack, err := catalog.ApplyStack(cat)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := definition.LoadDefinition(t.Context(), filepath.Join("../../../definitions", tc.definitionPath))
			if err != nil {
				t.Fatal(err)
			}
			env, err := definition.LoadEnvironment(t.Context(), "../../../definitions/nvke-deploy/environment-github.yaml")
			if err != nil {
				t.Fatal(err)
			}
			// Pure compilation has no source access or publication. The real
			// catalog pins, patches and each native child remain in the Plan.
			env.SourceAccess, env.Publication, env.Signing = nil, nil, nil
			plan, err := Compile(t.Context(), doc, env, stack, "profiles-contract")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "controller-profiles-image" || plan.LifecycleOutputs[0].Kind != "oci" || len(plan.Builds) != len(tc.platforms) {
				t.Fatalf("wrong image membership: %+v", plan.LifecycleOutputs)
			}
			if len(tc.platforms) == 2 && (plan.LifecycleOutputs[0].ImageIndex == nil || len(plan.LifecycleOutputs[0].ImageIndex.Children) != 2) {
				t.Fatal("both native images must belong to one exact OCI index")
			}
			for i, build := range plan.Builds {
				producers := 0
				for _, node := range build.Graph.Nodes {
					if node.Kind != ir.KindBuild {
						continue
					}
					producers++
					if node.BuilderType != ir.BuilderBuildKit || node.OutputType != "oci-image" || node.Network != ir.NetworkDefault || len(node.Command) != 3 || !strings.Contains(node.Command[2], "--opt platform="+tc.platforms[i]) {
						t.Fatalf("wrong native wrapped producer: %+v", node)
					}
					if strings.Contains(node.Command[2], "{{.Arch}}") || !strings.Contains(node.Command[2], "--local dockerfile=/src/"+filepath.Dir(tc.dockerfile)) {
						t.Fatalf("wrong image producer command: %v", node.Command)
					}
				}
				if producers != 1 {
					t.Fatalf("native build has %d image producers", producers)
				}
			}
			root := t.TempDir()
			for _, patch := range cat.Components[tc.component].Patches {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				cmd := exec.CommandContext(ctx, "git", "apply", "--no-index", "--whitespace=nowarn", "-")
				cmd.Dir, cmd.WaitDelay = root, time.Second
				cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))
				cmd.Stdin = strings.NewReader(patch.Content)
				output, err := cmd.CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("apply authenticated patch: %v\n%s", err, output)
				}
			}
			raw, err := os.ReadFile(filepath.Join(root, tc.dockerfile))
			if err != nil {
				t.Fatal(err)
			}
			value := string(raw)
			if !strings.Contains(value, "FROM docker.io/library/alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6\n") || !strings.Contains(value, "COPY controller/test/fixtures/runbooks/ /profiles/runbooks/\n") || strings.Contains(value, "\nRUN ") {
				t.Fatalf("image must use a pinned runnable base and source runbooks without target execution:\n%s", value)
			}

		})
	}
}
