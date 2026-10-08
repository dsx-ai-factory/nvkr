// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/testutil"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestLaptopARM64DefinitionMembership(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "definitions", "nke-local-dev-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]authored.DefinitionOutput{}
	parts := map[string]authored.DefinitionOutput{}
	for _, tc := range []struct {
		file  string
		count int
		disk  bool
	}{
		{"definition.json", 23, true},
		{"definition-components.json", 22, false},
		{"definition-os.json", 1, true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			d, err := authored.LoadDefinition(t.Context(), filepath.Join(root, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Outputs) != tc.count {
				t.Fatalf("outputs=%d want %d", len(d.Outputs), tc.count)
			}
			hasDisk := false
			for _, output := range d.Outputs {
				if output.Platform != "linux/arm64" {
					t.Fatalf("foreign platform: %+v", output)
				}
				if output.Format == authored.OutputDiskImage {
					hasDisk = true
				}
				if tc.file == "definition.json" {
					all[output.Name] = output
				} else {
					if _, exists := parts[output.Name]; exists {
						t.Fatalf("duplicate split output %s", output.Name)
					}
					parts[output.Name] = output
				}
			}
			if hasDisk != tc.disk {
				t.Fatalf("disk=%t want %t", hasDisk, tc.disk)
			}
		})
	}
	if !reflect.DeepEqual(all, parts) {
		t.Fatal("component and OS selections do not exactly partition the combined set")
	}
	providers, err := authored.LoadDefinition(t.Context(), filepath.Join(root, "definition-providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(providers.Outputs) != 2 {
		t.Fatal("provider loop must select both required source-built provider images")
	}
	for _, output := range providers.Outputs {
		combined, ok := all[output.Name]
		if !ok {
			t.Fatalf("provider %s omitted from combined selection", output.Name)
		}
		// The provider-only Definition uses CAPNICo as its default catalog.
		// Normalize that one explicit relationship before comparing outputs.
		if output.Catalog == nil {
			output.Catalog = &providers.Catalog
		}
		if !reflect.DeepEqual(output, combined) {
			t.Fatalf("provider loop diverges from combined selection: %s", output.Name)
		}
	}
	env, err := authored.LoadEnvironment(t.Context(), filepath.Join(root, "environment-local.json"))
	if err != nil {
		t.Fatal(err)
	}
	if env.Publication != nil || env.Signing != nil || env.GitHubARC != "" || env.UtilityPlatform != "linux/arm64" || env.RuntimeBinary() != "podman" {
		t.Fatalf("local example grants an unintended route: %+v", env)
	}
}

// The actual CAPNICo catalog has no derived remote pin set. Keep its compiler
// contract in the default gate and prove it needs no Git executable. Provider
// membership is checked above; this one-output test is not full-provider proof.
func TestLaptopARM64CAPNICoCatalogCompilation(t *testing.T) {
	t.Chdir("../../..")
	root := filepath.Join("definitions", "nke-local-dev-arm64")
	path := filepath.Join(root, "definition-providers.json")
	doc, err := authored.LoadDefinition(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Outputs) != 2 || doc.Outputs[0].Name != "capnico-controller-arm64" || doc.Outputs[0].Catalog != nil {
		t.Fatal("provider Definition no longer starts with the catalog-local CAPNICo output")
	}
	doc.Outputs = doc.Outputs[:1]
	envPath := filepath.Join(root, "environment-local.json")
	env, err := authored.LoadEnvironment(t.Context(), envPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // Any attempted remote Git derivation must fail.
	plan, err := factory.CompileDocuments(t.Context(), path, doc, envPath, env, "laptop-capnico-contract")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "capnico-controller-arm64" || plan.Qualification != factory.TierStaticUnqualified {
		t.Fatal("unexpected CAPNICo compiled membership or tier")
	}
}

// Compile the complete, unmodified maintained catalogs. These integration
// matrices validate derived chart pins against exact NKE Git bytes, including
// when only the provisioned provider is selected. They require authenticated
// NKE Git access or an exact local Git mirror; the default gate has neither.
func TestLaptopARM64CatalogCompilation(t *testing.T) {
	testutil.RequireExtended(t)
	t.Chdir("../../..")
	root := filepath.Join("definitions", "nke-local-dev-arm64")
	for _, tc := range []struct {
		file    string
		outputs int
	}{
		{"definition-providers.json", 2},
		{"definition-components.json", 22},
		{"definition.json", 23},
	} {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join(root, tc.file)
			doc, err := authored.LoadDefinition(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			envPath := filepath.Join(root, "environment-local.json")
			env, err := authored.LoadEnvironment(t.Context(), envPath)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := factory.CompileDocuments(t.Context(), path, doc, envPath, env, "laptop-catalog-contract")
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if len(plan.LifecycleOutputs) != tc.outputs || plan.Qualification != factory.TierStaticUnqualified {
				t.Fatalf("unexpected compiled membership/tier: %d %s", len(plan.LifecycleOutputs), plan.Qualification)
			}
		})
	}
}

func TestLaptopARM64OSExecutionClassAdmission(t *testing.T) {
	t.Chdir("../../..")
	root := filepath.Join("definitions", "nke-local-dev-arm64")
	definitionPath := filepath.Join(root, "definition-os.json")
	d, err := authored.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(root, "environment-local.json")
	env, err := authored.LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*authored.Environment)
	}{
		{name: "complete TCG host declaration"},
		{name: "runtime alone is not a VM class", change: func(env *authored.Environment) { env.ExecutionClasses = nil }},
		{name: "foreign host is not implicit", change: func(env *authored.Environment) { env.ExecutionClasses[0].HostPlatform = "linux/amd64" }},
		{name: "insufficient workspace declaration", change: func(env *authored.Environment) { env.ExecutionClasses[0].Resources.EphemeralStorageMiB = 1 }},
		{name: "insufficient timeout declaration", change: func(env *authored.Environment) { env.ExecutionClasses[0].MaxTimeoutSeconds = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := env.Clone()
			if tc.change != nil {
				tc.change(&selected)
			}
			plan, err := factory.CompileDocuments(t.Context(), definitionPath, d, environmentPath, selected, "test")
			if tc.change != nil {
				var coded *e.Error
				if plan != nil || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatalf("expected no Plan and %s, got plan=%v err=%v", e.CodeEngineCapability, plan != nil, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "nke-node-image-arm64" {
				t.Fatal("complete Environment did not admit the exact ARM64 disk Plan")
			}
		})
	}
	dockerPath := filepath.Join(root, "environment-local-docker.json")
	dockerEnv, err := authored.LoadEnvironment(t.Context(), dockerPath)
	if err != nil {
		t.Fatal(err)
	}
	if dockerEnv.RuntimeBinary() != "docker" || dockerEnv.Publication != nil || dockerEnv.Signing != nil {
		t.Fatal("Docker developer environment grants an unintended route")
	}
	if _, err := factory.CompileDocuments(t.Context(), definitionPath, d, dockerPath, dockerEnv, "test"); err != nil {
		t.Fatalf("native Docker TCG Plan: %v", err)
	}
}
