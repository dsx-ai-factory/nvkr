// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
)

// closure is the complete set of committed generated bundles that depend on the
// maintained catalog pins: the production Run, the other named Runs (development
// and additional) and optional production/development qualification pairs. Each member keeps its
// existing generation owner; this type only admits, checks and lists them, it
// renders nothing itself.
type closure struct {
	production               *factory.GeneratedBundle
	runs                     []closureRun
	qualification            *qualification.GeneratedWorkflows
	developmentQualification *qualification.GeneratedWorkflows
}

// closureRun is one non-production Run member with the fresh Plan it renders.
// A runtime-Plan member (githubRuntimePlan) keeps its owner's contract: its
// committed workflow and actions stay fixed while the Plan and action data move.
type closureRun struct {
	bundle *factory.GeneratedBundle
	plan   *factory.Plan
}

func (run closureRun) runtime() bool { return run.plan.Environment.UsesGitHubRuntimePlan() }

// otherRuns lists the policy's Run selections besides production, in policy order.
func otherRuns(config Config) []RunInput {
	var runs []RunInput
	if config.DevelopmentEnvironment != "" {
		runs = append(runs, RunInput{Definition: config.Definition, Environment: config.DevelopmentEnvironment})
	}
	return append(runs, config.AdditionalRuns...)
}

// generatedBundleGlob finds every committed generated bundle manifest.
const generatedBundleGlob = ".github/nvkr-*/manifest.json"

var qualificationDirectory = path.Dir(qualification.QualificationManifestPath)

// buildClosure renders the policy's dependent members through their real owners
// and refuses a committed generated bundle the policy does not name. It writes
// nothing, so it can run before any pin work and at workflow render time.
func buildClosure(ctx context.Context, config Config, generatorVersion string, plan *factory.Plan, production *factory.GeneratedBundle, sourceRepository string) (*closure, error) {
	members := &closure{production: production}
	allowed := map[string]bool{path.Dir(production.ManifestPath): true}
	workflows := map[string]bool{production.WorkflowPath: true}
	for _, run := range otherRuns(config) {
		for _, relative := range []string{run.Definition, run.Environment} {
			if err := admitPath(relative); err != nil {
				return nil, err
			}
		}
		if run.Definition != config.Definition {
			if err := admitRunCatalogs(ctx, run.Definition, sourceRepository); err != nil {
				return nil, err
			}
		}
		runPlan, err := lifecycle.CompileFiles(ctx, run.Definition, run.Environment, generatorVersion)
		if err != nil {
			return nil, err
		}
		if err := admitPlanTransport(sourceRepository, runPlan); err != nil {
			return nil, err
		}
		bundle, err := factory.Render(ctx, runPlan, factory.EngineGitHub)
		if err != nil {
			return nil, err
		}
		directory := path.Dir(bundle.ManifestPath)
		if allowed[directory] || workflows[bundle.WorkflowPath] {
			return nil, e.New(e.CodeUsage, "maintenance run %s with %s must select a generated bundle layout no other selected run uses", run.Definition, run.Environment)
		}
		allowed[directory], workflows[bundle.WorkflowPath] = true, true
		members.runs = append(members.runs, closureRun{bundle: bundle, plan: runPlan})
		if run.Environment == config.DevelopmentEnvironment && config.DevelopmentQualificationPolicy != "" {
			if err := admitPath(config.DevelopmentQualificationPolicy); err != nil {
				return nil, err
			}
			policy, err := lifecycle.LoadQualificationPolicy(ctx, config.DevelopmentQualificationPolicy)
			if err != nil {
				return nil, err
			}
			if !policy.Source.Development {
				return nil, e.New(e.CodeEngineCapability, "development qualification maintenance requires a development policy")
			}
			members.developmentQualification, err = qualification.RenderWorkflows(ctx, runPlan, policy, config.DevelopmentQualificationPolicy)
			if err != nil {
				return nil, err
			}
			allowed[path.Dir(qualification.DevelopmentQualificationManifestPath)] = true
		}
	}
	if config.QualificationPolicy != "" {
		if err := admitPath(config.QualificationPolicy); err != nil {
			return nil, err
		}
		policy, err := lifecycle.LoadQualificationPolicy(ctx, config.QualificationPolicy)
		if err != nil {
			return nil, err
		}
		if members.qualification, err = qualification.RenderWorkflows(ctx, plan, policy, config.QualificationPolicy); err != nil {
			return nil, err
		}
		allowed[qualificationDirectory] = true
	}
	if err := refuseUnnamedBundles(allowed); err != nil {
		return nil, err
	}
	return members, nil
}

// refuseUnnamedBundles fails closed on a committed generated bundle that this
// maintenance run would leave stale: refresh cannot regenerate what the policy
// does not name, and the bot cannot transport what it did not regenerate.
func refuseUnnamedBundles(allowed map[string]bool) error {
	manifests, err := filepath.Glob(generatedBundleGlob)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "listing committed generated bundles")
	}
	sort.Strings(manifests)
	for _, manifest := range manifests {
		directory := filepath.ToSlash(filepath.Dir(manifest))
		if !allowed[directory] {
			return e.New(e.CodeEngineCapability, "committed generated bundle %q is not named by the maintenance policy; name its Environment or qualification policy, or retire it, because a pin change would leave it stale", directory)
		}
	}
	return nil
}

// check verifies the dependent members, read-only, against their committed bytes.
// The production bundle is checked by the caller.
func (members *closure) check(ctx context.Context) error {
	for _, run := range members.runs {
		if run.runtime() {
			// The runtime owner authenticates every committed byte, then admits only Plan and action-data changes.
			if err := factory.PrepareGitHubRuntimeBundle(ctx, run.plan, ".", true); err != nil {
				return err
			}
			continue
		}
		if err := factory.CheckGeneratedBundle(ctx, ".", run.bundle); err != nil {
			return err
		}
	}
	for _, pair := range []*qualification.GeneratedWorkflows{members.qualification, members.developmentQualification} {
		if pair != nil {
			if err := qualification.CheckWorkflows(ctx, ".", pair); err != nil {
				return err
			}
		}
	}
	return nil
}

// plans lists the committed Plan path of every Run bundle in the closure.
func (members *closure) plans() []string {
	plans := []string{members.production.PlanPath}
	for _, run := range members.runs {
		plans = append(plans, run.bundle.PlanPath)
	}
	return plans
}

// paths returns the exact, sorted directories and files a candidate patch may touch.
func (members *closure) paths() (directories, files []string) {
	add := func(bundle *factory.GeneratedBundle) {
		directories = append(directories, path.Dir(bundle.ManifestPath))
		files = append(files, bundle.WorkflowPath)
	}
	add(members.production)
	for _, run := range members.runs {
		add(run.bundle)
	}
	if members.qualification != nil {
		directories = append(directories, qualificationDirectory)
		for name := range members.qualification.Files() {
			files = append(files, name)
		}
	}
	if members.developmentQualification != nil {
		directories = append(directories, path.Dir(qualification.DevelopmentQualificationManifestPath))
		for name := range members.developmentQualification.Files() {
			files = append(files, name)
		}
	}
	sort.Strings(directories)
	sort.Strings(files)
	return directories, files
}

// committedPlanGlob finds every committed Run Plan, whatever its bundle layout.
const committedPlanGlob = ".github/nvkr-*/plan.json"

// admitCommittedPlanStamps compares the rendering generator with the stamp of
// every committed Plan before any catalog or source resolution, so a mismatched
// binary fails without network or private-source access. Every committed Run
// must belong to the closure anyway, so no Plan is exempt.
func admitCommittedPlanStamps(ctx context.Context, generatorVersion string) error {
	plans, err := filepath.Glob(committedPlanGlob)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "listing committed Plans")
	}
	sort.Strings(plans)
	for _, plan := range plans {
		if err := admitPlanStamp(ctx, filepath.ToSlash(plan), generatorVersion); err != nil {
			return err
		}
	}
	return nil
}

// admitGeneratorStamp repeats the comparison for each closure member, which also
// refuses a member whose Plan is not committed at all.
func (members *closure) admitGeneratorStamp(ctx context.Context, generatorVersion string) error {
	for _, plan := range members.plans() {
		if err := admitPlanStamp(ctx, plan, generatorVersion); err != nil {
			return err
		}
	}
	return nil
}

// admitPlanStamp reads one committed Plan as a bounded, no-follow, nonblocking
// regular file. A different stamp would render a workflow whose refresh can
// never pass its own baseline drift check.
func admitPlanStamp(ctx context.Context, plan, generatorVersion string) error {
	if err := admitPath(plan); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "admitting committed Plan %q", plan)
	}
	held, err := heldfile.OpenContext(ctx, plan, heldfile.Options{Label: "committed-plan", MaxBytes: factory.MaxPlanBytes})
	if err != nil {
		return contextError(ctx, e.Wrap(e.CodePipelineDrift, err, "reading committed Plan %q", plan))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, factory.MaxPlanBytes)
	if err != nil {
		return contextError(ctx, e.Wrap(e.CodePipelineDrift, err, "reading committed Plan %q", plan))
	}
	var committed struct {
		GeneratorVersion string `json:"generatorVersion"`
	}
	if err := json.Unmarshal(raw, &committed); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding committed Plan %q", plan)
	}
	if committed.GeneratorVersion != generatorVersion {
		return e.New(e.CodePipelineDrift, "committed Plan %q records generator %q but this binary is %q; render with the canonical generator build so the installed workflow can pass its baseline check", plan, committed.GeneratorVersion, generatorVersion)
	}
	return nil
}

// generateClosure writes or checks every member through its own owner. Production
// goes through the qualification owner when a policy is named, because that owner
// composes the producer and qualification pair; every other Run uses the Run
// owner, or the runtime-Plan owner when its Environment opts into githubRuntimePlan. An owner's coded drift or conflict error is returned unchanged.
func generateClosure(ctx context.Context, config Config, generatorVersion string, check bool,
	generate func(context.Context, string, string, string, string, string, bool) (string, error),
) (string, error) {
	var digest string
	var err error
	if config.QualificationPolicy != "" {
		digest, err = qualification.GenerateFiles(ctx, config.Definition, config.Environment, config.QualificationPolicy, generatorVersion, factory.EngineGitHub, ".", check)
	} else {
		digest, err = generate(ctx, config.Definition, config.Environment, generatorVersion, factory.EngineGitHub, ".", check)
	}
	if err != nil {
		return "", err
	}
	for _, run := range otherRuns(config) {
		if run.Environment == config.DevelopmentEnvironment && config.DevelopmentQualificationPolicy != "" {
			if _, err := qualification.GenerateFiles(ctx, run.Definition, run.Environment, config.DevelopmentQualificationPolicy, generatorVersion, factory.EngineGitHub, ".", check); err != nil {
				return "", err
			}
			continue
		}
		environment, err := lifecycle.LoadEnvironment(ctx, run.Environment)
		if err != nil {
			return "", err
		}
		owner := generate
		if environment.UsesGitHubRuntimePlan() {
			owner = lifecycle.GenerateRuntimeFiles
		}
		if _, err := owner(ctx, run.Definition, run.Environment, generatorVersion, factory.EngineGitHub, ".", check); err != nil {
			return "", err
		}
	}
	return digest, nil
}
