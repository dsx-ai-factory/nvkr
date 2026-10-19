// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func assertCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v; want %s", err, code)
	}
}
func writeFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, raw)
}

func maintenanceFixture(t *testing.T) (Config, func(string)) {
	t.Helper()
	base, err := filepath.Abs("../../../testdata/catalogs/kubernetes-layered")
	if err != nil {
		t.Fatal(err)
	}
	baseSources, err := catalog.ReadSourcesContext(t.Context(), catalog.Dir(base))
	if err != nil {
		t.Fatal(err)
	}
	baseLock, err := catalog.LoadLockContext(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	upstream := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+upstream+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://maintenance.example.invalid/source")
	for key, value := range map[string]string{"GIT_AUTHOR_NAME": "Fixture", "GIT_COMMITTER_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_EMAIL": "fixture@example.invalid", "GIT_AUTHOR_DATE": "2025-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2025-01-01T00:00:00Z"} {
		t.Setenv(key, value)
	}
	git := func(args ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir = upstream
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("init", "-q", "--template=", "--object-format=sha1", "--initial-branch=main")
	promote := func(version string) {
		t.Helper()
		writeFixture(t, filepath.Join(upstream, "versions.yaml"), []byte("release: v"+version+"\n"))
		writeFixture(t, filepath.Join(upstream, "build.sh"), []byte("#!/bin/sh\nprintf fixture-"+version+" > bin/tool\n"))
		git("add", ".")
		git("-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture "+version)
		git("tag", "release/v"+version)
		git("tag", "app/v"+version)
	}
	promote("1.0.0")
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir("catalog", 0700); err != nil {
		t.Fatal(err)
	}
	image := "busybox@sha256:" + strings.Repeat("a", 64)
	sources := map[string]catalog.Source{
		"root": {Repo: "https://maintenance.example.invalid/source", Constraint: "1.x", TagPrefix: "release/", PinSet: &catalog.PinSet{File: "versions.yaml", Members: map[string]catalog.PinSetMember{"app": {Source: "app", Path: "release"}}}},
		"role": {Repo: "https://maintenance.example.invalid/source", Constraint: "1.x", TagPrefix: "release/", ToolchainImage: image},
		"app":  {Repo: "https://maintenance.example.invalid/source", PinnedBy: "root", TagPrefix: "app/", ToolchainImage: image},
	}
	for _, name := range []string{"checkout", "setup-go", "download-artifact", "upload-artifact"} {
		sources[name] = baseSources[name]
	}
	registry := map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "sources": sources, "components": map[string]any{"tool": map[string]any{"source": "app", "fromSource": true, "artifactPath": "bin/tool", "writeDir": "bin", "buildCommand": []string{"sh", "build.sh"}, "outputType": "host-package", "platforms": []string{"linux/amd64"}}}}
	writeJSON(t, "catalog/registry.yaml", registry)
	lock, err := catalog.LoadLockContext(t.Context(), "catalog")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkout", "setup-go", "download-artifact", "upload-artifact"} {
		lock.Pins[name] = baseLock.Pins[name]
	}
	for _, name := range []string{"root", "role", "app"} {
		pin, err := catalog.ResolvePin(t.Context(), sources[name], "v1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		if name == "app" {
			pin.DerivedFrom = "root@" + lock.Pins["root"].Commit
		}
		lock.Pins[name] = pin
	}
	if err := catalog.WriteLockContext(t.Context(), "catalog", lock); err != nil {
		t.Fatal(err)
	}
	cfg := Config{APIVersion: SchemaVersion, Name: "fixture", Catalog: "catalog", Sources: []string{"root", "role"}, SameRelease: [][]string{{"root", "role"}}, Definition: "definition.yaml", Environment: "environment.yaml", Validation: "static"}
	if _, err := catalog.UpdateSourcePins(t.Context(), catalog.SourcePinRequest{Directory: "catalog", Sources: cfg.Sources, SameRelease: cfg.SameRelease, Bump: true}); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, "definition.yaml", []byte("apiVersion: nvkr.dev/definition/v5\nkind: Definition\nmetadata: {name: fixture}\ncatalog: {base: catalog}\noutputs:\n- {name: tools, format: executable, platform: linux/amd64, components: [tool]}\n"))
	writeFixture(t, "environment.yaml", []byte("apiVersion: nvkr.dev/environment/v1\nkind: Environment\nmetadata: {name: fixture}\nutilityContainer: "+image+"\nutilityPlatform: linux/amd64\n"))
	writeJSON(t, "maintenance.yaml", cfg)
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
	return cfg, promote
}

func TestRefreshRealLocalPinSetJourney(t *testing.T) {
	cfg, promote := maintenanceFixture(t)
	baseline := readFixture(t, "catalog/pins.lock.yaml")
	oldPlan := readFixture(t, ".github/nvkr-run/plan.json")
	oldWorkflow := readFixture(t, ".github/workflows/nvkr-run.yml")
	oldManifest := readFixture(t, ".github/nvkr-run/manifest.json")
	result, err := Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || result.Changed || !bytes.Equal(baseline, readFixture(t, "catalog/pins.lock.yaml")) {
		t.Fatalf("no-op: %+v %v", result, err)
	}
	promote("1.1.0")
	result, err = Refresh(t.Context(), cfg, "test (deadbeef)", true, "")
	assertCode(t, err, e.CodeDiff)
	if !result.Changed || result.Update.Changed != 3 || !bytes.Equal(baseline, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(oldPlan, readFixture(t, ".github/nvkr-run/plan.json")) {
		t.Fatal("preview lost derived member or mutated files")
	}
	result, err = Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || !result.Changed {
		t.Fatalf("apply: %+v %v", result, err)
	}
	lock, err := catalog.LoadLockContext(t.Context(), "catalog")
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pins["root"].Tag != "release/v1.1.0" || lock.Pins["role"].Commit != lock.Pins["root"].Commit || lock.Pins["app"].DerivedFrom != "root@"+lock.Pins["root"].Commit {
		t.Fatal("incomplete cohort/pin-set update")
	}
	if lock.Pins["role"].DerivedFrom != "" {
		t.Fatal("independent role became a derived member")
	}
	if bytes.Equal(oldPlan, readFixture(t, ".github/nvkr-run/plan.json")) {
		t.Fatal("changed pins did not regenerate")
	}
	if !bytes.Equal(oldWorkflow, readFixture(t, ".github/workflows/nvkr-run.yml")) {
		t.Fatal("routine source-pin refresh changed workflow topology or presentation")
	}
	if bytes.Equal(oldManifest, readFixture(t, ".github/nvkr-run/manifest.json")) {
		t.Fatal("changed pins did not update the generated bundle manifest")
	}
	current := readFixture(t, "catalog/pins.lock.yaml")
	result, err = Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || result.Changed || !bytes.Equal(current, readFixture(t, "catalog/pins.lock.yaml")) {
		t.Fatalf("second no-op: %+v %v", result, err)
	}
}

func TestRefreshRejectsStaleGenerationBeforePinWork(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	before := readFixture(t, "catalog/pins.lock.yaml")
	writeFixture(t, ".github/nvkr-run/plan.json", []byte("tampered"))
	calls := 0
	_, err := refresh(t.Context(), cfg, "test (deadbeef)", false, "", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		calls++
		return catalog.SourcePinUpdate{}, nil
	})
	assertCode(t, err, e.CodePipelineDrift)
	if calls != 0 || !bytes.Equal(before, readFixture(t, "catalog/pins.lock.yaml")) {
		t.Fatal("stale bundle reached pin transaction")
	}
}

func TestRefreshExactConstraintsNeverWiden(t *testing.T) {
	cfg, promote := maintenanceFixture(t)
	registry := readFixture(t, "catalog/registry.yaml")
	registry = bytes.ReplaceAll(registry, []byte(`"constraint": "1.x"`), []byte(`"constraint": "v1.0.0"`))
	writeFixture(t, "catalog/registry.yaml", registry)
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
	before := readFixture(t, "catalog/pins.lock.yaml")
	beforePlan := readFixture(t, ".github/nvkr-run/plan.json")
	promote("1.1.0")
	result, err := Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || result.Changed || !bytes.Equal(before, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) || !bytes.Equal(registry, readFixture(t, "catalog/registry.yaml")) {
		t.Fatalf("exact policy widened or rewrote inputs: %+v %v", result, err)
	}
}

func TestRefreshFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name            string
		failAfterCommit bool
		code            e.Code
	}{
		{"resolution", false, e.CodeFetchFailed},
		{"regeneration", true, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, promote := maintenanceFixture(t)
			promote("1.1.0")
			before := readFixture(t, "catalog/pins.lock.yaml")
			beforePlan := readFixture(t, ".github/nvkr-run/plan.json")
			result, err := refresh(t.Context(), cfg, "test (deadbeef)", false, "", func(ctx context.Context, request catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
				if !request.RejectRepublishedVersions {
					t.Fatal("maintenance omitted republished-version admission")
				}
				if !tc.failAfterCommit {
					return catalog.SourcePinUpdate{}, e.New(e.CodeFetchFailed, "fixture unavailable source")
				}
				update, err := catalog.UpdateSourcePins(ctx, request)
				if err == nil {
					writeFixture(t, cfg.Definition, []byte("invalid Definition"))
				}
				return update, err
			})
			assertCode(t, err, tc.code)
			lockChanged := !bytes.Equal(before, readFixture(t, "catalog/pins.lock.yaml"))
			if result.Changed != tc.failAfterCommit || lockChanged != tc.failAfterCommit || !bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) {
				t.Fatalf("failure lost transaction boundary: %+v %v", result, err)
			}
		})
	}
}
