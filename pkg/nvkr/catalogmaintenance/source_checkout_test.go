// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const checkoutRepo = "https://github.com/example/nvke"

// checkoutOptions is the transport the rendered workflow would apply.
var checkoutOptions = WorkflowOptions{SourceRepository: "example/nvke", SourceSecret: "SOURCE_TOKEN"}

func sourceCheckoutFixture(t *testing.T) Config {
	t.Helper()
	cfg, _ := maintenanceFixture(t)
	registry := bytes.ReplaceAll(readFixture(t, "catalog/registry.yaml"), []byte("https://maintenance.example.invalid/source"), []byte(checkoutRepo))
	writeFixture(t, "catalog/registry.yaml", registry)
	// Apply the rewrite the generated workflow renders, with the local upstream standing in for the checkout.
	transport := transportFor(checkoutOptions.SourceRepository)
	upstream := strings.TrimSuffix(strings.TrimPrefix(os.Getenv("GIT_CONFIG_KEY_0"), "url."), ".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", transport.Value)
	t.Setenv("GIT_CONFIG_KEY_0", strings.Replace(transport.Key, workspaceExpression+"/"+sourceCheckoutPath, upstream, 1))
	cfg.SameRelease = nil
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func changeCheckoutSource(t *testing.T, directory, name, repo string) {
	t.Helper()
	filename := filepath.Join(directory, "registry.yaml")
	var registry map[string]json.RawMessage
	if err := json.Unmarshal(readFixture(t, filename), &registry); err != nil {
		t.Fatal(err)
	}
	var sources map[string]catalog.Source
	if err := json.Unmarshal(registry["sources"], &sources); err != nil {
		t.Fatal(err)
	}
	source := sources[name]
	source.Repo = repo
	sources[name] = source
	raw, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	registry["sources"] = raw
	writeJSON(t, filename, registry)
}

func TestWorkflowRejectsSourceCheckoutPrefixCollisionsBeforeWork(t *testing.T) {
	baseline := sourceCheckoutFixture(t)
	registry := readFixture(t, "catalog/registry.yaml")
	definition := readFixture(t, baseline.Definition)
	beforeLock := readFixture(t, "catalog/pins.lock.yaml")
	beforePlan := readFixture(t, ".github/nvkr-run/plan.json")
	// Both entrypoints share prepare before mode-specific admission. Exercise
	// every source location once and retain both modes without their cross-product.
	for _, tc := range []struct{ location, mode string }{
		{"selected root", "static"},
		{"derived member", "static"},
		{"required fetch", "local-run"},
		{"output catalog", "static"},
	} {
		t.Run(tc.mode+"/"+tc.location, func(t *testing.T) {
			cfg := baseline
			cfg.Validation = tc.mode
			t.Cleanup(func() {
				writeFixture(t, "catalog/registry.yaml", registry)
				writeFixture(t, cfg.Definition, definition)
			})
			switch tc.location {
			case "selected root":
				changeCheckoutSource(t, "catalog", "role", checkoutRepo+"-tools")
			case "derived member":
				changeCheckoutSource(t, "catalog", "app", checkoutRepo+"-tools")
			case "required fetch":
				cfg.Sources = []string{"root"}
				changeCheckoutSource(t, "catalog", "role", checkoutRepo+"-tools")
				changed := bytes.Replace(readFixture(t, "catalog/registry.yaml"), []byte(`"source": "app"`), []byte(`"source": "role"`), 1)
				writeFixture(t, "catalog/registry.yaml", changed)
			case "output catalog":
				if err := os.Mkdir("output-catalog", 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"registry.yaml", "pins.lock.yaml"} {
					writeFixture(t, filepath.Join("output-catalog", name), readFixture(t, filepath.Join("catalog", name)))
				}
				changeCheckoutSource(t, "output-catalog", "app", checkoutRepo+"-tools")
				changed := bytes.Replace(definition, []byte("name: tools,"), []byte("name: tools, catalog: {base: output-catalog},"), 1)
				writeFixture(t, cfg.Definition, changed)
			}
			enroll(t, cfg)
			raw, err := Workflow(t.Context(), checkoutOptions, "test (deadbeef)")
			assertCode(t, err, e.CodeEngineCapability)
			if len(raw) != 0 || !strings.Contains(err.Error(), "would redirect distinct repository") {
				t.Fatalf("collision emitted workflow or unrelated error: %s %v", raw, err)
			}
			calls := 0
			_, err = refresh(t.Context(), cfg, "test (deadbeef)", false, "example/nvke", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
				calls++
				return catalog.SourcePinUpdate{}, nil
			})
			assertCode(t, err, e.CodeEngineCapability)
			if calls != 0 || !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) {
				t.Fatal("collision reached pin mutation or changed generated files")
			}
		})
	}
}

func TestWorkflowAllowsRepeatedRepositoryAndUnrelatedSources(t *testing.T) {
	baseline := sourceCheckoutFixture(t)
	registry := readFixture(t, "catalog/registry.yaml")
	for _, tc := range []struct {
		name, repo string
		selected   bool
	}{
		{"same", checkoutRepo, true},
		{"git suffix", checkoutRepo + ".git", true},
		{"trailing slash", checkoutRepo + "/", true},
		{"git suffix and slash", checkoutRepo + ".git/", true},
		{"unrelated", "https://github.com/example/other", true},
		{"unused sibling", checkoutRepo + "-tools", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseline
			t.Cleanup(func() { writeFixture(t, "catalog/registry.yaml", registry) })
			changeCheckoutSource(t, "catalog", "role", tc.repo)
			if !tc.selected {
				cfg.Sources = []string{"root"}
			}
			enroll(t, cfg)
			raw, err := Workflow(t.Context(), checkoutOptions, "test (deadbeef)")
			if err != nil || len(raw) == 0 {
				t.Fatalf("supported transport rejected: %v", err)
			}
			if tc.selected && strings.HasPrefix(tc.repo, checkoutRepo) {
				command := exec.CommandContext(t.Context(), "git", "ls-remote", tc.repo, "refs/tags/release/v1.0.0")
				if output, err := command.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("refs/tags/release/v1.0.0")) {
					t.Fatalf("same-repository transport failed: %s %v", output, err)
				}
			}
		})
	}
}

func TestSourceCheckoutWithOnlyAliasedRoots(t *testing.T) {
	testutil.RequireExtended(t)
	for _, suffix := range []string{".git", "/", ".git/"} {
		for _, mode := range []string{"static", "local-run"} {
			t.Run(suffix+"/"+mode, func(t *testing.T) {
				cfg := sourceCheckoutFixture(t)
				cfg.Validation = mode
				for _, name := range []string{"root", "role", "app"} {
					changeCheckoutSource(t, "catalog", name, checkoutRepo+suffix)
				}
				// Exercise the exact generated Git rewrite before either public owner.
				command := exec.CommandContext(t.Context(), "git", "ls-remote", checkoutRepo+suffix, "refs/tags/release/v1.0.0")
				if output, err := command.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("refs/tags/release/v1.0.0")) {
					t.Fatalf("alias did not reach the real local Git repository: %s %v", output, err)
				}
				if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", false); err != nil {
					t.Fatal(err)
				}
				beforeLock := readFixture(t, "catalog/pins.lock.yaml")
				beforePlan := readFixture(t, ".github/nvkr-run/plan.json")
				enroll(t, cfg)
				raw, err := Workflow(t.Context(), checkoutOptions, "test (deadbeef)")
				if err != nil || len(raw) == 0 {
					t.Errorf("alias-only workflow rejected: %v", err)
				}
				result, err := Refresh(t.Context(), cfg, "test (deadbeef)", false, "example/nvke")
				if err != nil || result.Changed || result.PlanDigest == "" {
					t.Errorf("alias-only refresh rejected or changed pinned release: %+v %v", result, err)
				}
				if !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) {
					t.Fatal("alias-only no-op changed the lock or generated Plan")
				}
			})
		}
	}
}

func TestAliasedRootStillRejectsDistinctRepository(t *testing.T) {
	cfg := sourceCheckoutFixture(t)
	changeCheckoutSource(t, "catalog", "root", checkoutRepo+".git")
	changeCheckoutSource(t, "catalog", "app", checkoutRepo+".git")
	changeCheckoutSource(t, "catalog", "role", checkoutRepo+"-tools")
	enroll(t, cfg)
	raw, err := Workflow(t.Context(), checkoutOptions, "test (deadbeef)")
	assertCode(t, err, e.CodeEngineCapability)
	if len(raw) != 0 {
		t.Fatal("sibling collision emitted a workflow")
	}
	calls := 0
	_, err = refresh(t.Context(), cfg, "test (deadbeef)", false, "example/nvke", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		calls++
		return catalog.SourcePinUpdate{}, nil
	})
	assertCode(t, err, e.CodeEngineCapability)
	if calls != 0 {
		t.Fatal("sibling collision reached the pin transaction")
	}
}

// An additional Run's catalogs are admitted before it is compiled: compilation
// revalidates every declared pin set, so a pin-set root that only the additional
// Run's catalog declares would otherwise be fetched through the prefix rewrite.
func TestAdditionalRunCatalogsAreAdmittedBeforeCompilation(t *testing.T) {
	cfg := sourceCheckoutFixture(t)
	if err := os.Mkdir("extra-catalog", 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"registry.yaml", "pins.lock.yaml"} {
		writeFixture(t, filepath.Join("extra-catalog", name), readFixture(t, filepath.Join("catalog", name)))
	}
	changeCheckoutSource(t, "extra-catalog", "root", checkoutRepo+"-tools")
	definition := bytes.Replace(readFixture(t, cfg.Definition), []byte("catalog: {base: catalog}"), []byte("catalog: {base: extra-catalog}"), 1)
	writeFixture(t, "extra-definition.yaml", definition)
	writeFixture(t, "environment-extra.yaml", append(readFixture(t, cfg.Environment), "githubBundle: extra\n"...))
	cfg.AdditionalRuns = []RunInput{{Definition: "extra-definition.yaml", Environment: "environment-extra.yaml"}}
	enroll(t, cfg)
	beforeLock := readFixture(t, "catalog/pins.lock.yaml")
	raw, err := Workflow(t.Context(), checkoutOptions, "test (deadbeef)")
	assertCode(t, err, e.CodeEngineCapability)
	if len(raw) != 0 || !strings.Contains(err.Error(), "would redirect distinct repository") {
		t.Fatalf("the additional Run's pin-set root reached compilation: %s %v", raw, err)
	}
	calls := 0
	_, err = refresh(t.Context(), cfg, "test (deadbeef)", false, "example/nvke", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		calls++
		return catalog.SourcePinUpdate{}, nil
	})
	assertCode(t, err, e.CodeEngineCapability)
	if calls != 0 || !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) {
		t.Fatal("the additional Run's collision reached pin mutation")
	}
}
