// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
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

// The committed policy and Plans give the real qualification pairs and named
// family and nightly Runs; their complete committed file inventories must be in
// the allowlist even without private source access. This checks local snapshots,
// not source authentication or regeneration; the real compile/refresh owner is
// exercised on fixtures in closure_qualification_test.go and by workflows-check.
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
	if cfg.DevelopmentEnvironment == "" || cfg.QualificationPolicy == "" || cfg.DevelopmentQualificationPolicy == "" {
		t.Fatal("the maintained policy does not name its dependent bundles")
	}
	wantAdditional := []RunInput{
		{Definition: "inputs/definitions/nvke/core.yaml", Environment: "inputs/environments/github-nvke-core.yaml"},
		{Definition: "inputs/definitions/nvke/services.yaml", Environment: "inputs/environments/github-nvke-services.yaml"},
		{Definition: "inputs/definitions/nvke/telemetry.yaml", Environment: "inputs/environments/github-nvke-telemetry.yaml"},
		{Definition: "inputs/definitions/nvke/disks.yaml", Environment: "inputs/environments/github-nvke-disks.yaml"},
		{Definition: "inputs/definitions/demo/arm64-nvidia-kvm.json", Environment: "inputs/environments/github-nightly-nvidia.json"},
		{Definition: "inputs/definitions/demo/arm64-upstream-kvm.json", Environment: "inputs/environments/github-nightly-upstream.json"},
	}
	if len(cfg.AdditionalRuns) != len(wantAdditional) {
		t.Fatalf("maintained policy selects %d additional Runs, want %d", len(cfg.AdditionalRuns), len(wantAdditional))
	}
	for _, want := range wantAdditional {
		if !slices.Contains(cfg.AdditionalRuns, want) {
			t.Fatalf("maintained policy omits additional Run %+v", want)
		}
	}
	planPaths, err := filepath.Glob(committedPlanGlob)
	if err != nil {
		t.Fatal(err)
	}
	wantRuns := append([]RunInput{{Definition: cfg.Definition, Environment: cfg.Environment}}, otherRuns(cfg)...)
	if len(planPaths) != len(wantRuns) {
		t.Fatalf("maintained closure has %d committed Plans, want %d", len(planPaths), len(wantRuns))
	}
	members := &closure{}
	plans := make(map[RunInput]*factory.Plan, len(wantRuns))
	for _, planPath := range planPaths {
		plan, bundle := committedClosureRun(t, filepath.ToSlash(planPath))
		input := RunInput{Definition: plan.DefinitionPath, Environment: plan.EnvironmentPath}
		if !slices.Contains(wantRuns, input) || plans[input] != nil {
			t.Fatalf("committed Plan %s has an unexpected or duplicate policy selection %+v", planPath, input)
		}
		plans[input] = plan
		if input == wantRuns[0] {
			members.production = bundle
		} else {
			members.runs = append(members.runs, closureRun{bundle: bundle, plan: plan})
		}
	}
	for _, input := range wantRuns {
		if plans[input] == nil {
			t.Fatalf("maintenance policy selection %+v has no committed Plan", input)
		}
	}
	for _, selected := range []struct {
		policyPath string
		input      RunInput
		target     **qualification.GeneratedWorkflows
	}{
		{cfg.QualificationPolicy, wantRuns[0], &members.qualification},
		{cfg.DevelopmentQualificationPolicy, RunInput{Definition: cfg.Definition, Environment: cfg.DevelopmentEnvironment}, &members.developmentQualification},
	} {
		policy, err := lifecycle.LoadQualificationPolicy(t.Context(), selected.policyPath)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := qualification.RenderWorkflows(t.Context(), plans[selected.input], policy, selected.policyPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := qualification.CheckWorkflows(t.Context(), ".", pair); err != nil {
			t.Fatalf("committed qualification owner %s: %v", selected.policyPath, err)
		}
		*selected.target = pair
	}
	directories, files := members.paths()
	wantDirectories := []string{
		".github/nvkr-dev",
		".github/nvkr-dev-qualification",
		".github/nvkr-qualification",
		".github/nvkr-run",
		".github/nvkr-run-nvke-core",
		".github/nvkr-run-nvke-disks",
		".github/nvkr-run-nvke-nightly-arm64",
		".github/nvkr-run-nvke-nightly-upstream-ovn-arm64",
		".github/nvkr-run-nvke-services",
		".github/nvkr-run-nvke-telemetry",
	}
	if !slices.Equal(directories, wantDirectories) {
		t.Fatalf("closure directories = %v, want %v", directories, wantDirectories)
	}
	wantFiles := []string{
		".github/workflows/nvkr-dev-qualify.yml",
		".github/workflows/nvkr-dev-smoke.yml",
		".github/workflows/nvkr-dev.yml",
		".github/workflows/nvkr-qualify.yml",
		".github/workflows/nvkr-run-nvke-core.yml",
		".github/workflows/nvkr-run-nvke-disks.yml",
		".github/workflows/nvkr-run-nvke-nightly-arm64.yml",
		".github/workflows/nvkr-run-nvke-nightly-upstream-ovn-arm64.yml",
		".github/workflows/nvkr-run-nvke-services.yml",
		".github/workflows/nvkr-run-nvke-telemetry.yml",
		".github/workflows/nvkr-run.yml",
		".github/workflows/nvkr-smoke.yml",
	}
	if !slices.Equal(files, wantFiles) {
		t.Fatalf("closure workflow files = %v, want %v", files, wantFiles)
	}
	allowed := make(map[string]bool, len(directories))
	for _, directory := range directories {
		allowed[directory] = true
	}
	if err := refuseUnnamedBundles(allowed); err != nil {
		t.Fatal(err)
	}
}

// Reconstitute only the committed description; the factory owner checks every
// member's bytes/mode, exact manifest and complete namespace inventory. Rendering
// every family here would repeat the separate extended regeneration matrix.
func committedClosureRun(t *testing.T, planPath string) (*factory.Plan, *factory.GeneratedBundle) {
	t.Helper()
	plan, err := factory.LoadPlanContext(t.Context(), planPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.ToSlash(filepath.Join(filepath.Dir(planPath), "manifest.json"))
	var manifest factory.GeneratedManifest
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(readFixture(t, manifestPath), []byte("\n")), &manifest); err != nil {
		t.Fatalf("committed manifest %s: %v", manifestPath, err)
	}
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.PlanDigest != digest {
		t.Fatalf("committed manifest %s does not bind its Plan", manifestPath)
	}
	bundle := &factory.GeneratedBundle{
		Engine: manifest.Engine, WorkflowPath: manifest.WorkflowPath, PlanPath: planPath,
		ManifestPath: manifestPath, PlanDigest: digest, WorkflowDigest: manifest.WorkflowDigest,
		Files: make(map[string][]byte, len(manifest.Members)),
	}
	for _, member := range manifest.Members {
		bundle.Files[member.Path] = readFixture(t, member.Path)
	}
	if err := factory.CheckGeneratedBundle(t.Context(), ".", bundle); err != nil {
		t.Fatalf("committed producer owner %s: %v", manifestPath, err)
	}
	return plan, bundle
}
