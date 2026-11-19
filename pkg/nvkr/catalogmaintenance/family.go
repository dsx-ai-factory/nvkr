// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
)

// FamilyResult describes maintenance, never artifact or release acceptance.
// Complete is set only after the prepared owner bytes commit successfully.
// Failed commits restore this operation's files or report retained recovery backups.
type FamilyResult struct {
	Family            string                `json:"family"`
	Catalogs          []FamilyCatalogUpdate `json:"catalogs"`
	Overlays          []string              `json:"overlays"`
	Consumers         []string              `json:"consumers"`
	Recipes           []FamilyRecipeUpdate  `json:"recipes"`
	Changed           int                   `json:"changed"`
	CommittedCatalogs []string              `json:"committedCatalogs,omitempty"`
	Complete          bool                  `json:"complete"`
}

type FamilyCatalogUpdate struct {
	Catalog string                  `json:"catalog"`
	Update  catalog.SourcePinUpdate `json:"update"`
}

type FamilyRecipeUpdate struct {
	Catalog       string `json:"catalog"`
	Component     string `json:"component"`
	Before        string `json:"before"`
	After         string `json:"after"`
	CommitVersion string `json:"commitVersion,omitempty"`
	BeforeTag     string `json:"beforeTag"`
	AfterTag      string `json:"afterTag"`
}

type familyRecipeVersion struct{ version, commitVersion, tag string }

type preparedFamilyRun struct {
	bundle              *factory.GeneratedBundle
	pair                *qualification.GeneratedWorkflows
	baselineRuntimePlan *factory.Plan
}

// BumpFamily uses the existing maintenance policy's complete generated closure.
// Resolution, overlays and generation finish in private staging before any
// working-file commit. The coordinator uses every owner's writer guard and
// exact input bytes; ordinary commit errors roll back the prepared set.
func BumpFamily(ctx context.Context, configPath, familyName, generatorVersion string, check bool) (FamilyResult, error) {
	return bumpFamily(ctx, configPath, familyName, generatorVersion, check, catalog.PrepareSourcePins)
}

func bumpFamily(ctx context.Context, configPath, familyName, generatorVersion string, check bool,
	preparePins func(context.Context, catalog.SourcePinRequest) (*catalog.PreparedSourcePins, catalog.SourcePinUpdate, error),
) (FamilyResult, error) {
	var result FamilyResult
	if ctx == nil || !safeRelative(configPath) {
		return result, e.New(e.CodeUsage, "family maintenance requires a context and repository-relative policy")
	}
	config, err := Load(ctx, configPath)
	if err != nil {
		return result, err
	}
	if config.Family == nil || config.Family.Name != familyName {
		return result, e.New(e.CodeNoMatch, "maintenance policy does not declare catalog family %q", familyName)
	}
	result.Family = familyName
	slices.SortFunc(config.Family.Catalogs, func(a, b FamilyCatalog) int { return strings.Compare(a.Catalog, b.Catalog) })
	for i := range config.Family.Catalogs {
		slices.Sort(config.Family.Catalogs[i].Sources)
	}
	if err := admitCommittedPlanStamps(ctx, generatorVersion); err != nil {
		return result, err
	}
	plan, err := prepare(ctx, config, generatorVersion, "")
	if err != nil {
		return result, err
	}
	baseline, err := factory.Render(ctx, plan, factory.EngineGitHub)
	if err != nil {
		return result, err
	}
	if err := factory.CheckGeneratedBundle(ctx, ".", baseline); err != nil {
		return result, err
	}
	closure, err := buildClosure(ctx, config, generatorVersion, plan, baseline, "")
	if err != nil {
		return result, err
	}
	if err := closure.check(ctx); err != nil {
		return result, err
	}
	result.Consumers = closure.plans()
	pairs := append([]*qualification.GeneratedWorkflows{closure.qualification, closure.developmentQualification}, closure.additionalQualifications...)
	for _, pair := range pairs {
		if pair != nil {
			result.Consumers = append(result.Consumers, pair.ManifestPath())
		}
	}
	slices.Sort(result.Consumers)
	guards := []string{factory.PlanStoreDirectory + ".lease", ".nvkr-catalog-family.lease", producerOverlayGuard, baseline.ManifestPath + ".lease"}
	for _, run := range closure.runs {
		guards = append(guards, run.bundle.ManifestPath+".lease")
	}
	for _, pair := range pairs {
		if pair != nil {
			guards = append(guards, pair.ManifestPath()+".lease")
		}
	}
	for _, member := range config.Family.Catalogs {
		guards = append(guards, filepath.Join(member.Catalog, "."+catalog.LockFile+".lock"))
	}
	snapshot, err := snapshotFamilyInputs(ctx, config, configPath, guards...)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(snapshot.root) }()
	if err := validateFamilyBaseline(ctx, snapshot, config, configPath, generatorVersion); err != nil {
		return result, err
	}
	var release *catalog.Pin
	var repository, prefix, constraint string
	for _, member := range config.Family.Catalogs {
		beforeRecipes, err := familyRecipes(ctx, snapshot.root, member.Catalog)
		if err != nil {
			return result, err
		}
		sources, err := catalog.ReadSourcesContext(ctx, catalog.Dir(filepath.Join(snapshot.root, member.Catalog)))
		if err != nil {
			return result, err
		}
		for _, name := range member.Sources {
			source, ok := sources[name]
			if !ok || source.PinnedBy != "" || source.Constraint == "" {
				return result, e.New(e.CodeInvalidCatalog, "family source %s/%s requires an independent root and explicit release constraint", member.Catalog, name)
			}
			repo := strings.TrimSuffix(source.Repo, ".git")
			if repository == "" {
				repository, prefix, constraint = repo, source.TagPrefix, source.Constraint
			}
			if repo != repository || source.TagPrefix != prefix || source.Constraint != constraint {
				return result, e.New(e.CodeInvalidCatalog, "family source %s/%s has a different release cohort; select it separately", member.Catalog, name)
			}
		}
		receipt, update, err := preparePins(ctx, catalog.SourcePinRequest{Directory: member.Catalog, Sources: member.Sources, Bump: true, RejectRepublishedVersions: true})
		result.Catalogs = append(result.Catalogs, FamilyCatalogUpdate{Catalog: member.Catalog, Update: update})
		if err != nil {
			return result, err
		}
		reg, _, err := readFamilyFile(ctx, filepath.Join(snapshot.root, member.Catalog, catalog.RegistryFile))
		if err != nil {
			return result, err
		}
		lock, _, err := readFamilyFile(ctx, filepath.Join(snapshot.root, member.Catalog, catalog.LockFile))
		if err != nil {
			return result, err
		}
		if !receipt.MatchesInputs(reg, lock) {
			return result, e.New(e.CodeWriteConflict, "family catalog %q changed during resolution", member.Catalog)
		}
		for _, change := range update.Pins {
			if !slices.Contains(member.Sources, change.Name) {
				continue
			}
			if release == nil {
				pin := change.After
				release = &pin
			}
			pin := change.After
			if pin.Tag != release.Tag || pin.Commit != release.Commit || pin.TreeHash != release.TreeHash || pin.SourceDateEpoch != release.SourceDateEpoch {
				return result, e.New(e.CodeUnverifiedRef, "family sources resolved different upstream releases")
			}
		}
		result.Changed += update.Changed
		if update.Changed != 0 {
			if err := os.WriteFile(filepath.Join(snapshot.root, member.Catalog, catalog.LockFile), receipt.CandidateBytes(), 0600); err != nil {
				return result, e.Wrap(e.CodeInternal, err, "staging prepared family pins")
			}
		}
		afterRecipes, err := familyRecipes(ctx, snapshot.root, member.Catalog)
		if err != nil {
			return result, err
		}
		for _, name := range slices.Sorted(maps.Keys(beforeRecipes)) {
			result.Recipes = append(result.Recipes, FamilyRecipeUpdate{Catalog: member.Catalog, Component: name, Before: beforeRecipes[name].version, After: afterRecipes[name].version, CommitVersion: afterRecipes[name].commitVersion, BeforeTag: beforeRecipes[name].tag, AfterTag: afterRecipes[name].tag})
		}
	}
	var overlays []overlayFile
	if config.ProducerOverlays != "" {
		overlays, err = renderProducerOverlaysAtRoot(ctx, snapshot.root, config.ProducerOverlays)
		if err != nil {
			return result, err
		}
		for _, file := range overlays {
			result.Overlays = append(result.Overlays, file.path)
			before, err := catalog.DecodeOverlay(file.before)
			if err != nil {
				return result, err
			}
			after, err := catalog.DecodeOverlay(file.candidate)
			if err != nil {
				return result, err
			}
			for _, name := range slices.Sorted(maps.Keys(after.Components)) {
				component := after.Components[name].Component
				if component == nil || component.SourceRecipe == nil {
					continue
				}
				version, err := component.RecipeVersion()
				if err != nil {
					return result, err
				}
				oldVersion := ""
				oldTag := ""
				if previous := before.Components[name].Component; previous != nil {
					oldTag = previous.Tag
					oldVersion, err = previous.RecipeVersion()
					if err != nil {
						return result, err
					}
				}
				result.Recipes = append(result.Recipes, FamilyRecipeUpdate{Catalog: file.path, Component: name, Before: oldVersion, After: version, CommitVersion: component.SourceRecipe.CommitVersion, BeforeTag: oldTag, AfterTag: component.Tag})
			}
			if err := os.WriteFile(filepath.Join(snapshot.root, file.path), file.candidate, 0600); err != nil {
				return result, e.Wrap(e.CodeInternal, err, "staging prepared family overlay")
			}
		}
	}
	slices.Sort(result.Overlays)
	if result.Changed == 0 {
		if err := snapshot.checkOriginals(ctx); err != nil {
			return result, err
		}
		result.Complete = true
		return result, nil
	}
	runs, err := prepareFamilyRuns(ctx, config, generatorVersion, snapshot.root, false)
	if err != nil {
		return result, err
	}
	if _, err := prepareFamilyRuns(ctx, config, generatorVersion, snapshot.root, true); err != nil {
		return result, err
	}
	if err := snapshot.checkOriginals(ctx); err != nil {
		return result, err
	}
	paths := map[string]bool{factory.PlanStoreIndexPath: true}
	// The validated snapshot contains only manifest-owned shared objects,
	// including older records retained by an admitted runtime baseline.
	baselineStore := map[string]bool{}
	for name := range snapshot.files {
		if strings.HasPrefix(name, factory.PlanStoreDirectory+"/") && name != factory.PlanStoreIndexPath {
			baselineStore[name] = true
		}
	}
	retired := map[string]bool{}
	manifests := map[string]bool{}
	for name := range snapshot.files {
		if filepath.Base(name) == catalog.RegistryFile {
			guards = append(guards, filepath.Join(filepath.Dir(name), "."+catalog.LockFile+".lock"))
		}
	}
	for _, member := range config.Family.Catalogs {
		paths[filepath.Join(member.Catalog, catalog.LockFile)] = true
	}
	for _, file := range overlays {
		paths[file.path] = true
	}
	for _, run := range runs {
		paths[run.bundle.ManifestPath] = true
		manifests[run.bundle.ManifestPath] = true
		guards = append(guards, run.bundle.ManifestPath+".lease")
		for name := range run.bundle.Files {
			paths[name] = true
		}
		if run.pair != nil {
			paths[run.pair.ManifestPath()] = true
			manifests[run.pair.ManifestPath()] = true
			guards = append(guards, run.pair.ManifestPath()+".lease")
			for name := range run.pair.Files() {
				paths[name] = true
			}
		}
	}
	// The owner has checked the final manifests and swept unreferenced store
	// objects in staging. Other retirements still require authored regeneration.
	for name, original := range snapshot.files {
		raw, _, err := readFamilyFile(ctx, filepath.Join(snapshot.root, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && baselineStore[name] {
				retired[name], paths[name] = true, true
				continue
			}
			return result, contextError(ctx, e.Wrap(e.CodeEngineCapability, err, "family bump cannot retire input %q", name))
		}
		if sha256.Sum256(raw) != original.digest && !paths[name] {
			return result, e.New(e.CodeEngineCapability, "family preparation changed an unowned input %q", name)
		}
	}
	if err := snapshot.checkPreparedDirectories(ctx, paths); err != nil {
		return result, err
	}
	if check {
		return result, e.New(e.CodeDiff, "family %s would change %d pins across its explicitly selected catalogs", familyName, result.Changed)
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	slices.Sort(names)
	// Publish payloads, then ownership records, then retire unreferenced objects.
	// The descriptive store index follows the final inventory.
	phase := func(name string) int {
		if name == factory.PlanStoreIndexPath {
			return 3
		}
		if retired[name] {
			return 2
		}
		if manifests[name] {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(names, func(a, b string) int { return phase(a) - phase(b) })
	var changes []familyChange
	for _, name := range names {
		change, changed, err := snapshot.change(ctx, name, retired[name])
		if err != nil {
			return result, err
		}
		if changed {
			changes = append(changes, change)
		}
	}
	if err := commitFamilyChanges(ctx, snapshot, changes, guards); err != nil {
		return result, err
	}
	for _, member := range result.Catalogs {
		if member.Update.Changed != 0 {
			result.CommittedCatalogs = append(result.CommittedCatalogs, member.Catalog)
		}
	}
	result.Complete = true
	return result, nil
}

// Validate the bytes actually captured, not the earlier working-tree view.
func validateFamilyBaseline(ctx context.Context, snapshot *familySnapshot, config Config, configPath, generatorVersion string) error {
	captured, err := Load(ctx, filepath.Join(snapshot.root, configPath))
	if err != nil {
		return err
	}
	if captured.Family != nil {
		slices.SortFunc(captured.Family.Catalogs, func(a, b FamilyCatalog) int { return strings.Compare(a.Catalog, b.Catalog) })
		for i := range captured.Family.Catalogs {
			slices.Sort(captured.Family.Catalogs[i].Sources)
		}
	}
	if !reflect.DeepEqual(config, captured) {
		return e.New(e.CodeWriteConflict, "family policy changed during baseline capture")
	}
	if captured.ProducerOverlays != "" {
		files, err := renderProducerOverlaysAtRoot(ctx, snapshot.root, captured.ProducerOverlays)
		if err != nil {
			return err
		}
		if err := checkOverlayFiles(files); err != nil {
			return err
		}
	}
	if err := admitCommittedPlanStampsAtRoot(ctx, snapshot.root, generatorVersion); err != nil {
		return err
	}
	runs, err := prepareFamilyRuns(ctx, captured, generatorVersion, snapshot.root, true)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, run := range runs {
		allowed[filepath.ToSlash(filepath.Dir(run.bundle.ManifestPath))] = true
		if run.pair != nil {
			allowed[filepath.ToSlash(filepath.Dir(run.pair.ManifestPath()))] = true
		}
	}
	return refuseUnnamedBundlesAtRoot(snapshot.root, allowed)
}

func familyRecipes(ctx context.Context, root, directory string) (map[string]familyRecipeVersion, error) {
	cat, err := catalog.LoadContext(ctx, catalog.Dir(filepath.Join(root, directory)))
	if err != nil {
		return nil, err
	}
	versions := map[string]familyRecipeVersion{}
	for name, component := range cat.Components {
		if component.SourceRecipe == nil {
			continue
		}
		version, err := component.RecipeVersion()
		if err != nil {
			return nil, err
		}
		versions[name] = familyRecipeVersion{version: version, commitVersion: component.SourceRecipe.CommitVersion, tag: component.Tag}
	}
	return versions, nil
}

func prepareFamilyRuns(ctx context.Context, config Config, generatorVersion, root string, check bool) ([]preparedFamilyRun, error) {
	inputs := append([]RunInput{{Definition: config.Definition, Environment: config.Environment, QualificationPolicy: config.QualificationPolicy}}, otherRuns(config)...)
	var runs []preparedFamilyRun
	for i, input := range inputs {
		plan, err := factory.CompileAuthoredFiles(ctx, root, input.Definition, input.Environment, generatorVersion)
		if err != nil {
			return nil, err
		}
		// Maintenance regenerates authored bundles, as run generate does. Runtime
		// source-head freezing belongs only to dispatch bootstrap, never a bump.
		bundle, err := factory.Render(ctx, plan, factory.EngineGitHub)
		if err != nil {
			return nil, err
		}
		run := preparedFamilyRun{bundle: bundle}
		if i > 0 && plan.Environment.GitHubRuntimePlan {
			run.baselineRuntimePlan = plan
		}
		policyPath := input.QualificationPolicy
		if input.Environment == config.DevelopmentEnvironment && config.DevelopmentQualificationPolicy != "" {
			policyPath = config.DevelopmentQualificationPolicy
		}
		if policyPath != "" {
			policy, err := lifecycle.LoadQualificationPolicy(ctx, filepath.Join(root, policyPath))
			if err != nil {
				return nil, err
			}
			run.pair, err = qualification.RenderWorkflows(ctx, plan, policy, policyPath)
			if err != nil {
				return nil, err
			}
		}
		if err := commitFamilyRun(ctx, root, run, check); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func commitFamilyRun(ctx context.Context, root string, run preparedFamilyRun, check bool) error {
	if check {
		var err error
		if run.baselineRuntimePlan != nil {
			err = factory.PrepareGitHubRuntimeBundle(ctx, run.baselineRuntimePlan, root, true)
		} else {
			err = factory.CheckGeneratedBundle(ctx, root, run.bundle)
		}
		if err != nil {
			return err
		}
		if run.pair != nil {
			return qualification.CheckWorkflows(ctx, root, run.pair)
		}
		return nil
	}
	if err := factory.WriteGeneratedBundle(ctx, root, run.bundle); err != nil {
		return err
	}
	if run.pair != nil {
		return qualification.WriteWorkflows(ctx, root, run.pair)
	}
	return nil
}
