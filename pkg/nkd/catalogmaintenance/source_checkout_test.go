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

	"github.com/NVIDIA/nke-recipes/internal/testutil"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

const checkoutRepo = "https://github.com/example/nke"

func sourceCheckoutFixture(t *testing.T) Config {
	t.Helper()
	cfg, _ := maintenanceFixture(t)
	registry := bytes.ReplaceAll(readFixture(t, "catalog/registry.yaml"), []byte("https://maintenance.example.invalid/source"), []byte(checkoutRepo))
	writeFixture(t, "catalog/registry.yaml", registry)
	t.Setenv("GIT_CONFIG_VALUE_0", checkoutRepo)
	// Match the trailing slash used by the generated workflow's local rewrite.
	key := strings.TrimSuffix(os.Getenv("GIT_CONFIG_KEY_0"), ".insteadOf") + "/.insteadOf"
	t.Setenv("GIT_CONFIG_KEY_0", key)
	lock, err := catalog.LoadLockContext(t.Context(), "catalog")
	if err != nil {
		t.Fatal(err)
	}
	cfg.SourceCheckout = &SourceCheckout{Repository: "example/nke", Ref: lock.Pins["root"].Commit, Secret: "SOURCE_TOKEN"}
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
	for _, mode := range []string{"static", "local-run"} {
		for _, location := range []string{"selected root", "derived member", "required fetch", "output catalog"} {
			t.Run(mode+"/"+location, func(t *testing.T) {
				cfg := sourceCheckoutFixture(t)
				cfg.Validation = mode
				switch location {
				case "selected root":
					changeCheckoutSource(t, "catalog", "role", checkoutRepo+"-tools")
				case "derived member":
					changeCheckoutSource(t, "catalog", "app", checkoutRepo+"-tools")
				case "required fetch":
					cfg.Sources = []string{"root"}
					changeCheckoutSource(t, "catalog", "role", checkoutRepo+"-tools")
					registry := bytes.Replace(readFixture(t, "catalog/registry.yaml"), []byte(`"source": "app"`), []byte(`"source": "role"`), 1)
					writeFixture(t, "catalog/registry.yaml", registry)
				case "output catalog":
					if err := os.Mkdir("output-catalog", 0700); err != nil {
						t.Fatal(err)
					}
					for _, name := range []string{"registry.yaml", "pins.lock.yaml"} {
						writeFixture(t, filepath.Join("output-catalog", name), readFixture(t, filepath.Join("catalog", name)))
					}
					changeCheckoutSource(t, "output-catalog", "app", checkoutRepo+"-tools")
					definition := bytes.Replace(readFixture(t, cfg.Definition), []byte("name: tools,"), []byte("name: tools, catalog: {base: output-catalog},"), 1)
					writeFixture(t, cfg.Definition, definition)
				}
				beforeLock := readFixture(t, "catalog/pins.lock.yaml")
				beforePlan := readFixture(t, ".github/nkd-run/plan.json")
				raw, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
				assertCode(t, err, e.CodeEngineCapability)
				if len(raw) != 0 || !strings.Contains(err.Error(), "would redirect distinct repository") {
					t.Fatalf("collision emitted workflow or unrelated error: %s %v", raw, err)
				}
				calls := 0
				_, err = refresh(t.Context(), cfg, "test (deadbeef)", false, func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
					calls++
					return catalog.SourcePinUpdate{}, nil
				})
				assertCode(t, err, e.CodeEngineCapability)
				if calls != 0 || !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nkd-run/plan.json")) {
					t.Fatal("collision reached pin mutation or changed generated files")
				}
			})
		}
	}
}

func TestWorkflowAllowsRepeatedRepositoryAndUnrelatedSources(t *testing.T) {
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
			cfg := sourceCheckoutFixture(t)
			changeCheckoutSource(t, "catalog", "role", tc.repo)
			if !tc.selected {
				cfg.Sources = []string{"root"}
			}
			raw, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
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

func TestGitSourceCheckoutPrefixWouldRedirectSibling(t *testing.T) {
	_ = sourceCheckoutFixture(t)
	command := exec.CommandContext(t.Context(), "git", "ls-remote", checkoutRepo+"-tools")
	output, err := command.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("/-tools")) || !bytes.Contains(output, []byte("does not appear to be a git repository")) {
		t.Fatalf("expected local prefix collision: %s %v", output, err)
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
				beforePlan := readFixture(t, ".github/nkd-run/plan.json")
				raw, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
				if err != nil || len(raw) == 0 {
					t.Errorf("alias-only workflow rejected: %v", err)
				}
				result, err := Refresh(t.Context(), cfg, "test (deadbeef)", false)
				if err != nil || result.Changed || result.PlanDigest == "" {
					t.Errorf("alias-only refresh rejected or changed pinned release: %+v %v", result, err)
				}
				if !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nkd-run/plan.json")) {
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
	raw, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
	assertCode(t, err, e.CodeEngineCapability)
	if len(raw) != 0 {
		t.Fatal("sibling collision emitted a workflow")
	}
	calls := 0
	_, err = refresh(t.Context(), cfg, "test (deadbeef)", false, func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		calls++
		return catalog.SourcePinUpdate{}, nil
	})
	assertCode(t, err, e.CodeEngineCapability)
	if calls != 0 {
		t.Fatal("sibling collision reached the pin transaction")
	}
}
