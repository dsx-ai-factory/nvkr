// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func derivedTestInputs(t *testing.T) (authored.Definition, authored.Environment, *catalog.Stack) {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["containerd"]
	component.OutputType, component.ArtifactPath = "tarball", "bin/chart.tar.gz"
	base.Components["containerd"] = component
	if base.Tools == nil {
		base.Tools = map[string]catalog.ToolPin{}
	}
	base.Tools["cosign"] = catalog.ToolPin{Format: ToolFormatTarGzip, URL: "https://example.invalid/cosign.tar.gz", Platform: "linux/amd64", ArchiveDigest: testCosignArchiveDigest, ExecutablePath: "cosign", ExecutableDigest: testCosignExecutableDigest}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("derived", "chart", "ubuntu-24.04")
	definition.CosignTool = "cosign"
	environment.Signing = cosignSigningProfile()
	definition.Outputs[0].Format, definition.Outputs[0].Components = authored.OutputHelm, []string{"containerd"}
	definition.Outputs = append(definition.Outputs, authored.DefinitionOutput{Name: "inventory", Platform: "linux/amd64", Format: authored.OutputBundle,
		Derived: &derivedoutput.Selection{Encoding: derivedoutput.InventoryV1, Outputs: []string{"chart"}}})
	environment.Publication = &authored.Publication{RepositoryPrefix: "localhost:5000/nvkr", Anonymous: true, PlainHTTP: true}
	return definition, environment, stack
}

func TestDerivedInventoryPlan(t *testing.T) {
	for _, mode := range []string{"", "per-output-v1"} {
		t.Run(mode, func(t *testing.T) {
			definition, environment, stack := derivedTestInputs(t)
			environment.Publication.Mode = mode
			plan, err := Compile(t.Context(), definition, environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 || len(plan.LifecycleOutputs) != 2 {
				t.Fatal("derived output changed native producer membership")
			}
			node := planNode(plan, "package/inventory")
			if node.Derived == nil || !containsString(node.Inputs, "package/chart") || !containsString(node.Inputs, "result/chart/containerd") {
				t.Fatalf("missing package-to-derive authority edges: %#v", node)
			}
			environment.Publication.RepositoryPrefix = "localhost:5000/relocated"
			relocated, err := Compile(t.Context(), definition, environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			if plan.Builds[0].BuildDigest != relocated.Builds[0].BuildDigest || plan.LifecycleOutputs[1].BuildDigest == relocated.LifecycleOutputs[1].BuildDigest {
				t.Fatal("relocation must preserve payload build identity and change derived identity")
			}
			definition.Outputs[1].Derived.Outputs[0] = "other"
			if !reflect.DeepEqual(plan.Definition.Outputs[1].Derived.Outputs, []string{"chart"}) {
				t.Fatal("compiler retained caller selection")
			}
		})
	}
}

func TestDerivedInventoryAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*authored.Definition, *authored.Environment)
		code   e.Code
	}{
		{"no publication", func(_ *authored.Definition, e *authored.Environment) { e.Publication, e.Signing = nil, nil }, e.CodeEngineCapability},
		{"unknown sibling", func(d *authored.Definition, _ *authored.Environment) {
			d.Outputs[1].Derived.Outputs = []string{"missing"}
		}, e.CodeInvalidCatalog},
		{"self reference", func(d *authored.Definition, _ *authored.Environment) {
			d.Outputs[1].Derived.Outputs = []string{"inventory"}
		}, e.CodeInvalidCatalog},
		{"unknown encoding", func(d *authored.Definition, _ *authored.Environment) { d.Outputs[1].Derived.Encoding = "future" }, e.CodeInvalidCatalog},
		{"source producer", func(d *authored.Definition, _ *authored.Environment) {
			d.Outputs[1].Components = []string{"containerd"}
		}, e.CodeInvalidCatalog},
		{"empty selection", func(d *authored.Definition, _ *authored.Environment) { d.Outputs[1].Derived.Outputs = nil }, e.CodeInvalidCatalog},
		{"duplicate selection", func(d *authored.Definition, _ *authored.Environment) {
			d.Outputs[1].Derived.Outputs = []string{"chart", "chart"}
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, environment, stack := derivedTestInputs(t)
			tc.change(&definition, &environment)
			_, err := Compile(t.Context(), definition, environment, stack, "test")
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}

func TestDerivedInventoryDevelopmentAdmissionBeforeCatalog(t *testing.T) {
	for _, derived := range []bool{true, false} {
		name := "ordinary output control"
		if derived {
			name = "derived output refused"
		}
		t.Run(name, func(t *testing.T) {
			definition, environment, stack := derivedTestInputs(t)
			if !derived {
				definition.Outputs = definition.Outputs[:1]
			}
			environment.GitHubDevelopment = true
			environment.GitHubDispatchBranches = nil
			environment.Publication = &authored.Publication{Mode: "per-output-v1", RepositoryPrefix: "ghcr.io/example/nvkr/development", CredentialRef: development.WriteCredentialRef}
			namespace, err := development.ParseNamespace(environment.Publication.RepositoryPrefix)
			if err != nil {
				t.Fatal(err)
			}
			environment.Signing.Identity, environment.Signing.Issuer = namespace.Identity(), development.Issuer
			if err := definition.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := environment.Validate(); err != nil {
				t.Fatal(err)
			}
			loads := 0
			plan, err := compileDocumentsWithLoader(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test",
				func(context.Context, authored.Catalog) (*catalog.Stack, error) { loads++; return stack, nil })
			if derived {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if plan != nil || loads != 0 {
					t.Fatalf("admission accessed catalog or returned Plan: loads=%d", loads)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if plan == nil || loads != 1 {
					t.Fatalf("ordinary development compilation did not load catalog once: loads=%d", loads)
				}
			}
		})
	}
}
