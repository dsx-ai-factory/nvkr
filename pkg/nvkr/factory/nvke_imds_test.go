// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestNVKEIMDSNamedOutputEvidence(t *testing.T) {
	t.Chdir("../../..")
	const agent = "dsx-imds-node-attestor-agent"
	for _, family := range []string{"disks", "disks-ubuntu24"} {
		t.Run(family, func(t *testing.T) {
			definitionPath := "inputs/definitions/nvke/" + family + ".yaml"
			environmentPath := "inputs/environments/github-nvke-" + family + ".yaml"
			doc, err := definition.LoadDefinition(t.Context(), definitionPath)
			if err != nil {
				t.Fatal(err)
			}
			env, err := definition.LoadEnvironment(t.Context(), environmentPath)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := CompileDocuments(t.Context(), definitionPath, doc, environmentPath, env, "dev (unknown)")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.LifecycleOutputs) != 4 || len(plan.Builds) != 4 {
				t.Fatal("internal WLI producer changed Release membership")
			}
			for _, output := range plan.LifecycleOutputs {
				if output.Kind != definition.OutputDiskImage || !slices.Contains(output.Components, agent) {
					t.Fatalf("unexpected output or incomplete producer closure: %+v", output)
				}
			}
			for _, build := range plan.Builds {
				hashes := map[string]string{}
				for _, component := range build.ResolvedComponents {
					hashes[component.Name] = pipeline.FileDigest([]byte(build.Name + "/" + component.Name))
				}
				documents, err := diskBuildDocuments(build, hashes)
				if err != nil {
					t.Fatal(err)
				}
				bom := documents[agent]
				if bom == nil || len(bom.Metadata.Component.Hashes) != 1 || bom.Metadata.Component.Hashes[0].Content != strings.TrimPrefix(hashes[agent], "sha256:") {
					t.Fatal("disk producer evidence omits the observed IMDS executable")
				}
				props := map[string]string{}
				for _, property := range bom.Metadata.Component.Properties {
					props[property.Name] = property.Value
				}
				component := build.CatalogSnapshot.Components[agent]
				arch := strings.TrimPrefix(build.Platform, "linux/")
				if props["nvkr:commit"] != component.Commit || props["nvkr:treeHash"] != component.TreeHash || props["nvkr:toolchain"] != component.ToolchainImages[arch] {
					t.Fatalf("IMDS producer evidence lost source/toolchain binding: %v", props)
				}
				if err := validateDiskBuildDocument(plan, build.Name, agent, hashes[agent], bom); err != nil {
					t.Fatal(err)
				}
				if err := validateDiskBuildDocument(plan, build.Name, agent, pipeline.FileDigest([]byte("substitution")), bom); factoryErrorCode(err) != e.CodeVerifyFailed {
					t.Fatalf("substituted IMDS subject: %v", err)
				}
				for _, sibling := range plan.Builds {
					if sibling.Name != build.Name {
						if err := validateDiskBuildDocument(plan, sibling.Name, agent, hashes[agent], bom); factoryErrorCode(err) != e.CodeVerifyFailed {
							t.Fatalf("IMDS evidence reused across named outputs: %v", err)
						}
					}
				}
			}
		})
	}
}
