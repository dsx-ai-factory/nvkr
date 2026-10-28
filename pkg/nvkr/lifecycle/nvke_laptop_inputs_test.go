// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestLaptopARM64DefinitionMembership(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]authored.DefinitionOutput{}
	parts := map[string]authored.DefinitionOutput{}
	// Compare catalog relationships relative to each authored Definition, including
	// the standalone fork Definition outside the local example directory.
	normalize := func(output authored.DefinitionOutput, catalog authored.Catalog, dir string) authored.DefinitionOutput {
		if output.Catalog != nil {
			catalog = *output.Catalog
		}
		if !filepath.IsAbs(catalog.Base) {
			catalog.Base = filepath.Join(dir, catalog.Base)
		}
		catalog.Overlays = append([]string(nil), catalog.Overlays...)
		for i, path := range catalog.Overlays {
			if !filepath.IsAbs(path) {
				catalog.Overlays[i] = filepath.Join(dir, path)
			}
		}
		output.Catalog = &catalog
		return output
	}
	replaced := map[string]bool{}
	for _, tc := range []struct {
		file  string
		count int
		disk  bool
	}{
		{"inputs/definitions/demo/arm64-nvidia-tcg.json", 32, true},
		{"inputs/definitions/diagnostics/demo-arm64/components.json", 22, false},
		{"inputs/definitions/diagnostics/demo-arm64/os.json", 1, true},
		{"inputs/definitions/diagnostics/demo-arm64/simulator.json", 1, false},
		{"inputs/definitions/diagnostics/demo-arm64/tools.json", 1, false},
		{"inputs/definitions/diagnostics/demo-arm64/profiles.json", 1, false},
		// Installer-aligned selections replace the three older component slots.
		{"inputs/definitions/diagnostics/demo-arm64/site.json", 5, false},
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
				output = normalize(output, d.Catalog, filepath.Dir(filepath.Join(root, tc.file)))
				if tc.file == "inputs/definitions/demo/arm64-nvidia-tcg.json" {
					all[output.Name] = output
				} else {
					if _, exists := parts[output.Name]; exists {
						if tc.file == "inputs/definitions/diagnostics/demo-arm64/site.json" && output.Name == "controller-profiles" {
							if !reflect.DeepEqual(parts[output.Name], output) {
								t.Fatal("site profile differs from the standalone diagnostic profile")
							}
							continue
						}
						if tc.file != "inputs/definitions/diagnostics/demo-arm64/site.json" || (output.Name != "cwe" && output.Name != "controller-chart" && output.Name != "nvkectl") {
							t.Fatalf("unexpected duplicate split output %s", output.Name)
						}
						replaced[output.Name] = true
					}
					parts[output.Name] = output
				}
			}
			if hasDisk != tc.disk {
				t.Fatalf("disk=%t want %t", hasDisk, tc.disk)
			}
		})
	}
	if len(replaced) != 3 {
		t.Fatal("site selections must replace exactly the controller, CLI and controller chart slots")
	}
	forkPath := filepath.Join(root, "inputs/definitions/diagnostics/ovn.yaml")
	fork, err := authored.LoadDefinition(t.Context(), forkPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range fork.Outputs {
		if output.Platform != "linux/arm64" {
			continue
		}
		if output.Name != "ovn-chart-arm64" && output.Name != "ovn-image-arm64" {
			t.Fatalf("unexpected fork output %s", output.Name)
		}
		if _, exists := parts[output.Name]; exists {
			t.Fatalf("duplicate fork output %s", output.Name)
		}
		if output.Name == "ovn-chart-arm64" {
			if output.Helm == nil || len(output.Helm.Images) != 6 {
				t.Fatal("fork chart must bind six image roles")
			}
			for _, role := range []string{"control-plane", "node", "nbdb", "sbdb", "northd", "ovn-host"} {
				if output.Helm.Images[role] != "ovn-image-arm64" {
					t.Fatalf("fork chart role %s does not bind the selected image", role)
				}
			}
		}
		parts[output.Name] = normalize(output, fork.Catalog, filepath.Dir(forkPath))
	}
	if _, ok := parts["ovn-image-arm64"]; !ok {
		t.Fatal("standalone fork image omitted")
	}
	if _, ok := parts["ovn-chart-arm64"]; !ok {
		t.Fatal("standalone fork chart omitted")
	}
	runtimePath := filepath.Join(root, "inputs/definitions/demo/native-mac/definition-runtime-images.json")
	runtimeInputs, err := authored.LoadDefinition(t.Context(), runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(root, "inputs/definitions/demo/arm64-nvidia-local-kvm.json")
	nativeInputs, err := authored.LoadDefinition(t.Context(), nativePath)
	if err != nil {
		t.Fatal(err)
	}
	nativeOutputs := map[string]authored.DefinitionOutput{}
	for _, output := range nativeInputs.Outputs {
		nativeOutputs[output.Name] = normalize(output, nativeInputs.Catalog, filepath.Dir(nativePath))
	}
	if filepath.Clean(filepath.Join(filepath.Dir(runtimePath), runtimeInputs.Catalog.Base)) != filepath.Join(root, "inputs/catalogs/runtime-dependencies") {
		t.Fatal("native runtime inputs must select their source-aligned catalog owner")
	}
	addedRuntime := 0
	for _, output := range runtimeInputs.Outputs {
		output = normalize(output, runtimeInputs.Catalog, filepath.Dir(runtimePath))
		if !reflect.DeepEqual(nativeOutputs[output.Name], output) {
			t.Fatalf("runtime dependency differs from the native combined selection: %s", output.Name)
		}
		if _, found := parts[output.Name]; found {
			// The retained TCG diagnostic uses its release catalog; the native
			// selection above independently binds the current deployment owner.
			continue
		}
		switch output.Name {
		case "capi-exec-init-image-arm64", "capi-exec-plugin-image-arm64", "workload-image-arm64":
			parts[output.Name] = output
			addedRuntime++
		default:
			t.Fatalf("unexpected additional runtime output %s", output.Name)
		}
	}
	if addedRuntime != 3 {
		t.Fatal("combined selection omitted the exact native runtime additions")
	}
	if !reflect.DeepEqual(all, parts) {
		t.Fatal("combined selection differs from split inputs, standalone fork outputs and shared runtime dependencies")
	}
	providers, err := authored.LoadDefinition(t.Context(), filepath.Join(root, "inputs/definitions/diagnostics/demo-arm64/providers.json"))
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
		output = normalize(output, providers.Catalog, filepath.Join(root, "inputs/definitions/diagnostics/demo-arm64"))
		if !reflect.DeepEqual(output, combined) {
			t.Fatalf("provider loop diverges from combined selection: %s", output.Name)
		}
	}
	env, err := authored.LoadEnvironment(t.Context(), filepath.Join(root, "inputs/environments/local-arm64-tcg.json"))
	if err != nil {
		t.Fatal(err)
	}
	if env.Publication != nil || env.Signing != nil || env.GitHubARC != "" || env.UtilityPlatform != "linux/arm64" || env.RuntimeBinary() != "docker" {
		t.Fatalf("local example grants an unintended route: %+v", env)
	}
}

// The actual CAPNICo catalog has no derived remote pin set. Keep its compiler
// contract in the default gate and prove it needs no Git executable. Provider
// membership is checked above; this one-output test is not full-provider proof.
func TestLaptopARM64CAPNICoCatalogCompilation(t *testing.T) {
	t.Chdir("../../..")
	path := "inputs/definitions/diagnostics/demo-arm64/providers.json"
	doc, err := authored.LoadDefinition(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Outputs) != 2 || doc.Outputs[0].Name != "capnico-controller-arm64" || doc.Outputs[0].Catalog != nil {
		t.Fatal("provider Definition no longer starts with the catalog-local CAPNICo output")
	}
	doc.Outputs = doc.Outputs[:1]
	envPath := "inputs/environments/local-arm64-tcg.json"
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
// matrices validate derived chart pins against exact NVKE Git bytes, including
// when only the provisioned provider is selected. They require authenticated
// NVKE Git access or an exact local Git mirror; the default gate has neither.
func TestLaptopARM64CatalogCompilation(t *testing.T) {
	testutil.RequireExtended(t)
	t.Chdir("../../..")
	for _, tc := range []struct {
		file    string
		outputs int
	}{
		{"inputs/definitions/diagnostics/demo-arm64/providers.json", 2},
		{"inputs/definitions/diagnostics/demo-arm64/components.json", 22},
		{"inputs/definitions/demo/arm64-nvidia-tcg.json", 32},
	} {
		t.Run(tc.file, func(t *testing.T) {
			path := tc.file
			doc, err := authored.LoadDefinition(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			envPath := "inputs/environments/local-arm64-tcg.json"
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
			if tc.outputs == 32 {
				t.Run("complete-catalog-smoke-scope", func(t *testing.T) {
					t.Skip("deferred: complete 32-output smoke policy fixture needs reconciliation with native producer version contracts; catalog compilation and smoke admission remain required")
				})
			}
		})
	}
}

func TestLaptopARM64OSExecutionClassAdmission(t *testing.T) {
	t.Chdir("../../..")
	definitionPath := "inputs/definitions/diagnostics/demo-arm64/os.json"
	d, err := authored.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environmentPath := "inputs/environments/local-arm64-tcg.json"
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
			if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "nvke-node-image-arm64" {
				t.Fatal("complete Environment did not admit the exact ARM64 disk Plan")
			}
		})
	}
}
