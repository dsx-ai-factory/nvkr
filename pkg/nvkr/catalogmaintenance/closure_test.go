// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const developmentFixtureEnvironment = "environment-dev.yaml"

// closureFixture adds a committed development bundle that shares the fixture
// catalog, and names it in the returned policy.
func closureFixture(t *testing.T) (Config, func(string)) {
	t.Helper()
	cfg, promote := maintenanceFixture(t)
	raw := append(readFixture(t, cfg.Environment), "githubDevelopment: true\n"...)
	writeFixture(t, developmentFixtureEnvironment, raw)
	cfg.DevelopmentEnvironment = developmentFixtureEnvironment
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.DevelopmentEnvironment, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
	return cfg, promote
}

func checkDevelopment(t *testing.T, cfg Config) error {
	t.Helper()
	_, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.DevelopmentEnvironment, "test (deadbeef)", "github", ".", true)
	return err
}

func TestRefreshRegeneratesEveryBundleThatSharesAChangedLock(t *testing.T) {
	cfg, promote := closureFixture(t)
	if err := checkDevelopment(t, cfg); err != nil {
		t.Fatal(err)
	}
	oldDevelopment := readFixture(t, ".github/nvkr-dev/plan.json")
	oldProduction := readFixture(t, ".github/nvkr-run/plan.json")
	workflows := []string{".github/workflows/nvkr-dev.yml", ".github/workflows/nvkr-run.yml"}
	oldWorkflows := map[string][]byte{}
	for _, workflow := range workflows {
		oldWorkflows[workflow] = readFixture(t, workflow)
	}
	promote("1.1.0")
	// Without the closure the shared lock change leaves the development bundle stale.
	stale := cfg
	stale.DevelopmentEnvironment = ""
	if _, err := refresh(t.Context(), stale, "test (deadbeef)", true, "", catalog.UpdateSourcePins); err == nil {
		t.Fatal("an unnamed committed bundle was admitted")
	}
	result, err := Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || !result.Changed {
		t.Fatalf("apply: %+v %v", result, err)
	}
	if bytes.Equal(oldProduction, readFixture(t, ".github/nvkr-run/plan.json")) || bytes.Equal(oldDevelopment, readFixture(t, ".github/nvkr-dev/plan.json")) {
		t.Fatal("changed lock did not regenerate every bundle")
	}
	if err := checkDevelopment(t, cfg); err != nil {
		t.Fatalf("development bundle is stale after refresh: %v", err)
	}
	// A pin-only refresh must leave every workflow intact; only a topology or interface change may move one.
	for _, workflow := range workflows {
		if !bytes.Equal(oldWorkflows[workflow], readFixture(t, workflow)) {
			t.Fatalf("pin-only refresh changed workflow %s", workflow)
		}
	}
	current := readFixture(t, "catalog/pins.lock.yaml")
	result, err = Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || result.Changed || !bytes.Equal(current, readFixture(t, "catalog/pins.lock.yaml")) {
		t.Fatalf("second no-op: %+v %v", result, err)
	}
}

func TestRefreshRefusesAnUnsupportedDependentClosureBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Config)
		code  e.Code
	}{
		{"development bundle not named", func(t *testing.T, cfg *Config) { cfg.DevelopmentEnvironment = "" }, e.CodeEngineCapability},
		{"qualification manifest without a policy", func(t *testing.T, _ *Config) {
			if err := os.MkdirAll(".github/nvkr-qualification", 0o700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, ".github/nvkr-qualification/manifest.json", []byte("{}\n"))
		}, e.CodeEngineCapability},
		{"unknown bundle", func(t *testing.T, _ *Config) {
			if err := os.MkdirAll(".github/nvkr-other", 0o700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, ".github/nvkr-other/manifest.json", []byte("{}\n"))
		}, e.CodeEngineCapability},
		{"development Environment selects the production layout", func(t *testing.T, cfg *Config) { cfg.DevelopmentEnvironment = cfg.Environment }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, promote := closureFixture(t)
			promote("1.1.0")
			beforeLock := readFixture(t, "catalog/pins.lock.yaml")
			tc.setup(t, &cfg)
			calls := 0
			_, err := refresh(t.Context(), cfg, "test (deadbeef)", false, "", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
				calls++
				return catalog.SourcePinUpdate{}, nil
			})
			assertCode(t, err, tc.code)
			if calls != 0 || !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) {
				t.Fatal("unsupported closure reached the pin transaction")
			}
			enroll(t, cfg)
			out, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
			assertCode(t, err, tc.code)
			if len(out) != 0 {
				t.Fatal("unsupported closure emitted a workflow")
			}
		})
	}
}

func TestRefreshRejectsDriftInADependentBundleBeforePinWork(t *testing.T) {
	cfg, promote := closureFixture(t)
	promote("1.1.0")
	beforeLock := readFixture(t, "catalog/pins.lock.yaml")
	writeFixture(t, ".github/nvkr-dev/plan.json", []byte("tampered"))
	calls := 0
	_, err := refresh(t.Context(), cfg, "test (deadbeef)", false, "", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		calls++
		return catalog.SourcePinUpdate{}, nil
	})
	assertCode(t, err, e.CodePipelineDrift)
	if calls != 0 || !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) {
		t.Fatal("dependent bundle drift reached the pin transaction")
	}
}

// An owner's coded cause must survive the maintenance wrapper: a stage conflict
// stays a write conflict and drift stays drift, never a generic capability error.
func TestRegenerateKeepsTheOwnersCodedFailure(t *testing.T) {
	cfg := validConfig()
	cfg.DevelopmentEnvironment = "environment-dev.yaml"
	// The closure reads each Run's Environment to choose its owner; this one opts out of runtime Plans.
	t.Chdir(t.TempDir())
	writeFixture(t, cfg.DevelopmentEnvironment, []byte("apiVersion: nvkr.dev/environment/v1\nkind: Environment\nmetadata: {name: dev}\nutilityContainer: busybox@sha256:"+strings.Repeat("a", 64)+"\nutilityPlatform: linux/amd64\ngithubDevelopment: true\n"))
	for _, tc := range []struct {
		name         string
		failEnv      string
		failOnCheck  bool
		code         e.Code
		wantMessages string
	}{
		{"production write conflict", cfg.Environment, false, e.CodeWriteConflict, "regeneration failed"},
		{"development write conflict", cfg.DevelopmentEnvironment, false, e.CodeWriteConflict, "regeneration failed"},
		{"development drift on recheck", cfg.DevelopmentEnvironment, true, e.CodePipelineDrift, "did not reproduce"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wrote []string
			_, err := regenerate(t.Context(), cfg, "test (deadbeef)", func(_ context.Context, _, environment, _, _, _ string, check bool) (string, error) {
				if !check {
					wrote = append(wrote, environment)
				}
				if environment == tc.failEnv && check == tc.failOnCheck {
					return "", e.New(tc.code, "owner failure")
				}
				return "sha256:plan", nil
			})
			assertCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.wantMessages) {
				t.Fatalf("lost the retained-checkout guidance: %v", err)
			}
			if tc.failOnCheck && len(wrote) != 2 {
				t.Fatalf("every member must be written before the recheck: %v", wrote)
			}
		})
	}
}

func TestClosureAllowlistIsExactInBothPlaces(t *testing.T) {
	cfg, _ := closureFixture(t)
	enroll(t, cfg)
	raw, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	const stage = `git add -- "$POLICY_CATALOG/pins.lock.yaml" '.github/nvkr-dev' '.github/nvkr-run' '.github/workflows/nvkr-dev.yml' '.github/workflows/nvkr-run.yml'`
	const admit = `"$catalog/pins.lock.yaml" | '.github/nvkr-dev'/* | '.github/nvkr-run'/* | '.github/workflows/nvkr-dev.yml' | '.github/workflows/nvkr-run.yml') ;;`
	for _, want := range []string{stage, admit} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("rendered workflow lacks the exact closure allowlist %q", want)
		}
	}
	if bytes.Contains(raw, []byte("nvkr-qualification")) {
		t.Fatal("allowlist names a qualification pair the policy does not configure")
	}
}

// The committed policy and Plans give the real qualification pair and named
// nightly Runs; their files must be in the allowlist. This proves the listing
// only; regenerating a pair after a pin change is exercised on a fixture in
// closure_qualification_test.go.
func TestClosureListsTheMaintainedBundles(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cfg, err := Load(t.Context(), PolicyDirectory+"/nvke"+PolicySuffix)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DevelopmentEnvironment == "" || cfg.QualificationPolicy == "" {
		t.Fatal("the maintained policy does not name its dependent bundles")
	}
	plan, err := lifecycle.CompileFiles(t.Context(), cfg.Definition, cfg.Environment, "dev (unknown)")
	if err != nil {
		t.Skipf("committed Plan does not compile offline: %v", err)
	}
	production, err := factory.Render(t.Context(), plan, factory.EngineGitHub)
	if err != nil {
		t.Fatal(err)
	}
	members, err := buildClosure(t.Context(), cfg, "dev (unknown)", plan, production, "")
	if err != nil {
		t.Fatal(err)
	}
	directories, files := members.paths()
	for _, want := range []string{".github/nvkr-dev", ".github/nvkr-qualification", ".github/nvkr-run", ".github/nvkr-run-nvke-nightly-arm64", ".github/nvkr-run-nvke-nightly-upstream-ovn-arm64"} {
		if !contains(directories, want) {
			t.Fatalf("directories %v lack %s", directories, want)
		}
	}
	for _, want := range []string{".github/workflows/nvkr-dev.yml", ".github/workflows/nvkr-qualify.yml", ".github/workflows/nvkr-run-nvke-nightly-arm64.yml", ".github/workflows/nvkr-run-nvke-nightly-upstream-ovn-arm64.yml", ".github/workflows/nvkr-run.yml", ".github/workflows/nvkr-smoke.yml"} {
		if !contains(files, want) {
			t.Fatalf("files %v lack %s", files, want)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
