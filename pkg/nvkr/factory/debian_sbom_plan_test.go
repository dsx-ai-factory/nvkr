// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

func TestDebianInspectionPlanBindsScannerProfile(t *testing.T) {
	for _, mode := range []string{"complete", "wrong scanner", "wrong host", "missing payload"} {
		t.Run(mode, func(t *testing.T) {
			d, env, stack := debianFactoryInputs(t)
			sourceOnly, err := Compile(t.Context(), d, env, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			d.SBOMTool = "syft"
			pin := "sha256:" + strings.Repeat("d", 64)
			env.DiskInspectionProfiles = map[string]definition.DiskInspectionProfile{"linux/amd64": {Image: "example.test/inspection@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin,
				Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 9000, EphemeralStorageMiB: 4096}, TimeoutSeconds: 30, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume"}}
			stack.Catalog.Tools = map[string]catalog.ToolPin{"syft": {Format: "raw", URL: "https://example.test/syft", Platform: "linux/amd64", ExecutablePath: "syft", ArchiveDigest: pin, ExecutableDigest: pin}}
			if mode == "wrong scanner" {
				tool := stack.Catalog.Tools["syft"]
				tool.ArchiveDigest, tool.ExecutableDigest = "sha256:"+strings.Repeat("e", 64), "sha256:"+strings.Repeat("e", 64)
				stack.Catalog.Tools["syft"] = tool
			}
			if mode == "wrong host" {
				d.Outputs[0].Platform = "linux/arm64"
			}
			stack, err = catalog.ApplyStack(stack.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), d, env, stack, "test")
			if mode == "wrong scanner" {
				if plan != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
					t.Fatal(err)
				}
				return
			}
			if mode == "wrong host" {
				if plan != nil || factoryErrorCode(err) != e.CodeEngineCapability {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Builds[0].BuildDigest != sourceOnly.Builds[0].BuildDigest {
				t.Fatal("inspection changed Build")
			}
			if mode == "missing payload" {
				for i := range plan.Graph.Nodes {
					node := &plan.Graph.Nodes[i]
					if node.Kind == NodeSBOM {
						var inputs []string
						for _, id := range node.Inputs {
							if id != plan.LifecycleOutputs[0].ArtifactID {
								inputs = append(inputs, id)
							}
						}
						node.Inputs = inputs
					}
				}
				if factoryErrorCode(plan.validateSBOMGraph()) != e.CodeProvenanceMissing {
					t.Fatal("missing payload admitted")
				}
				return
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			order := topologicalFactoryNodes(plan.Graph)
			inspection, terminal := -1, -1
			for i, node := range order {
				if node.Kind == NodeSBOM {
					inspection = i
					if containsString(node.Inputs, "observation/"+node.BuildTarget) {
						t.Fatal("inspection requires its own downstream terminal receipt")
					}
				}
				if node.Kind == NodeVerify {
					terminal = i
				}
			}
			if inspection < 0 || terminal <= inspection {
				t.Fatal("terminal observation does not follow retained-content inspection")
			}
			raw, err := canonical.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip Plan
			if err := canonical.UnmarshalExact(raw, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if err := roundtrip.Validate(); err != nil {
				t.Fatal("compiled scanner Plan roundtrip", err)
			}
			for _, mutation := range []string{"image", "terminal", "scanner"} {
				t.Run(mutation, func(t *testing.T) {
					var changed Plan
					if err := canonical.UnmarshalExact(raw, &changed); err != nil {
						t.Fatal(err)
					}
					for i := range changed.Graph.Nodes {
						node := &changed.Graph.Nodes[i]
						if mutation == "terminal" {
							if node.Kind == NodeVerify {
								var inputs []string
								for _, id := range node.Inputs {
									if id != "sbom/"+node.BuildTarget {
										inputs = append(inputs, id)
									}
								}
								node.Inputs = inputs
							}
							continue
						}
						if node.Kind != NodeSBOM {
							continue
						}
						if mutation == "image" {
							node.Container = "example.test/foreign@sha256:" + strings.Repeat("f", 64)
						} else {
							var inputs []string
							for _, id := range node.Inputs {
								if mutation == "scanner" && id == "tool/syft" {
									continue
								}
								inputs = append(inputs, id)
							}
							node.Inputs = inputs
						}
					}
					if err := changed.Validate(); err == nil || factoryErrorCode(err) == "" {
						t.Fatal("changed complete compiled evidence Plan admitted", err)
					}
				})
			}
			if err := AdmitLocal(t.Context(), plan, LocalRunOptions{}); factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatal("compiler opened lifecycle execution", err)
			}
		})
	}
}

func TestDebianInspectionRejectsTargetBeforeCatalog(t *testing.T) {
	for _, request := range []string{"", "linux/arm64"} {
		t.Run(request, func(t *testing.T) {
			d, env, _ := debianFactoryInputs(t)
			d.SBOMTool = "syft"
			d.Catalog.Base = "/missing-debian-scanner-catalog"
			d.Outputs[0].Platform = request
			env.UtilityPlatform = "linux/arm64"
			if request != "" {
				env.UtilityPlatform = "linux/amd64"
			}
			plan, err := CompileDocuments(t.Context(), "definition.yaml", d, "environment.yaml", env, "test")
			if plan != nil || factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatal("target conflict reached catalog work", err)
			}
		})
	}
}
