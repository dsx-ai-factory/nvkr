// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestGenerateInventoryAdmissionBeforeCompilation(t *testing.T) {
	// All selected files are deliberately absent: malformed inventory must fail
	// admission without resolving any input or creating an output directory.
	for _, raw := range []string{
		`[]`,
		`[{"bundle":"run","group":"main","definition":"missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-z","group":"main","definition":"missing.yaml","environment":"missing.yaml"},{"bundle":"nvkr-a","group":"main","definition":"missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"main","definition":"missing.yaml","environment":"missing.yaml"},{"bundle":"nvkr-run","group":"nightly","definition":"missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"main","definition":"missing.yaml","environment":"missing.yaml","ignored":true}]`,
		`[{"bundle":"nvkr-run","group":"main","group":"nightly","definition":"missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"unknown","definition":"missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"main","definition":"../missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"main","definition":"missing.yaml","environment":"missing.yaml","runtime":"false"}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "inventory.json")
			if err := os.WriteFile(input, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			err := GenerateInventory(t.Context(), input, "", "test", root)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodePipelineDrift {
				t.Fatalf("expected inventory admission failure, got %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal("admission changed output root", err)
			}
		})
	}
}

func TestGenerateSelectionsRetainsRuntimeGuards(t *testing.T) {
	environment, err := lifecycle.LoadEnvironment(t.Context(), "../../../inputs/environments/github-nvke-nightly-gpu-site.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                 string
		projection, unpinned bool
		code                 e.Code
	}{
		{"projection adapter", true, false, e.CodeEngineCapability},
		{"unpinned source", false, true, e.CodeUnpinnedSource},
		{"runtime not selected", false, false, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := environment.Clone()
			source := env.GitHubRuntimeSources[0]
			env.GitHubRuntimeSources = nil
			env.GitHubRuntimePlan = tc.unpinned || tc.projection
			if tc.unpinned {
				env.GitHubRuntimeSource = &source
			}
			raw, err := canonical.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(t.TempDir(), "environment.json")
			if err := os.WriteFile(input, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if tc.projection {
				input = filepath.Join(t.TempDir(), "missing-environment.json")
				ctx = factory.WithProjectionAdapter(ctx, factory.ProjectionAdapter{Transport: "unsupported", Release: "test"})
			}
			root := filepath.Join(t.TempDir(), "untouched")
			_, err = GenerateSelections(ctx, []GenerationInput{{Definition: "missing-definition.json", Environment: input, Runtime: true}}, "test", root, false)
			requireCode(t, err, tc.code)
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("runtime refusal wrote output", err)
			}
		})
	}
}

func TestCoordinatedProducerQualificationOverlap(t *testing.T) {
	qualifier := &GeneratedWorkflows{files: map[string][]byte{"qualifier.yml": {}, "worker.yml": {}}}
	for _, name := range []string{"qualifier.yml", "worker.yml", "producer.yml"} {
		t.Run(name, func(t *testing.T) {
			producer := &factory.GeneratedBundle{Files: map[string][]byte{name: {}}}
			err := checkProducerQualificationOverlap(producer, qualifier)
			if name != "producer.yml" {
				requireCode(t, err, e.CodeEngineCapability)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenerateInventorySelectsGroupAndReservesRuntimeForChecks(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	var baseline []byte
	for _, tc := range []struct {
		name    string
		runtime bool
	}{{"ordinary", false}, {"runtime check flag", true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			selections := []generationSelection{
				{Bundle: "nvkr-a", Group: "development", Definition: "missing-development.yaml", Environment: "missing.yaml"},
				{Bundle: "nvkr-run", Group: "main", Definition: "docs/examples/nvke-module-tags/definition.yaml", Environment: "docs/examples/nvke-module-tags/environment-github.yaml", Runtime: tc.runtime},
				{Bundle: "nvkr-z", Group: "nightly", Definition: "missing-nightly.yaml", Environment: "missing.yaml"},
			}
			raw, err := canonical.Marshal(selections)
			if err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(root, "inventory.json")
			if err := os.WriteFile(input, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := GenerateInventory(t.Context(), input, "main", "test (deadbeef)", root); err != nil {
				t.Fatal(err)
			}
			plan, err := factory.LoadPlanContext(t.Context(), filepath.Join(root, factory.FactoryPlanPath))
			if err != nil {
				t.Fatal(err)
			}
			if plan.RuntimeInputs != nil || plan.Environment.GitHubRuntimePlan {
				t.Fatal("inventory write changed runtime policy or froze sources")
			}
			raw, err = canonical.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if baseline == nil {
				baseline = raw
			} else if !bytes.Equal(baseline, raw) {
				t.Fatal("check-only runtime flag changed generated Plan")
			}
			for _, name := range []string{"nvkr-a", "nvkr-z"} {
				if _, err := os.Stat(filepath.Join(root, ".github", name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unselected group wrote output", err)
				}
			}
		})
	}
}
