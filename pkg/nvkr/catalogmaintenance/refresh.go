// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// Result describes pin and generated-file maintenance only. Candidate producer
// execution is a separate explicit workflow gate; this is not release evidence.
type Result struct {
	Changed    bool                    `json:"changed"`
	PlanDigest string                  `json:"planDigest,omitempty"`
	Update     catalog.SourcePinUpdate `json:"update"`
}

// Refresh operates from a repository root, preserving its relative input paths.
// It requires an initially current generated bundle. Pin CAS and generated bundle
// writes retain their own owners; they are not one crash-atomic transaction.
// On failure after pin commit, keep the dirty checkout for diagnosis or discard
// that disposable checkout. No PR should be published until every gate succeeds.
func Refresh(ctx context.Context, config Config, generatorVersion string, check bool) (Result, error) {
	return refresh(ctx, config, generatorVersion, check, catalog.UpdateSourcePins)
}

func refresh(ctx context.Context, config Config, generatorVersion string, check bool,
	updatePins func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error),
) (Result, error) {
	var result Result
	plan, err := prepare(ctx, config, generatorVersion)
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
	result.PlanDigest = baseline.PlanDigest
	update, err := updatePins(ctx, catalog.SourcePinRequest{Directory: config.Catalog, Sources: config.Sources, SameRelease: config.SameRelease, RejectRepublishedVersions: true, Bump: true, Check: check, Runtime: "docker"})
	result.Update, result.Changed = update, update.Changed != 0
	if err != nil {
		return result, err
	}
	if !result.Changed {
		return result, nil
	}
	// Check mode must never mutate even if a future resolver stops returning Diff.
	if check {
		return result, e.New(e.CodeDiff, "catalog pins would change")
	}
	digest, err := regenerate(ctx, config, generatorVersion, lifecycle.GenerateFiles)
	if err != nil {
		return result, err
	}
	result.PlanDigest = digest
	return result, nil
}

func regenerate(ctx context.Context, config Config, generatorVersion string,
	generate func(context.Context, string, string, string, string, string, bool) (string, error),
) (string, error) {
	digest, err := generate(ctx, config.Definition, config.Environment, generatorVersion, factory.EngineGitHub, ".", false)
	if err != nil {
		return "", fmt.Errorf("pins committed but regeneration failed; retain the disposable checkout for diagnosis: %w", err)
	}
	if _, err := generate(ctx, config.Definition, config.Environment, generatorVersion, factory.EngineGitHub, ".", true); err != nil {
		return "", fmt.Errorf("pins committed but candidate regeneration did not reproduce; retain the disposable checkout for diagnosis: %w", err)
	}
	return digest, nil
}

func prepare(ctx context.Context, config Config, generatorVersion string) (*factory.Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "catalog maintenance requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, contextError(ctx, err)
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	for _, relative := range []string{config.Catalog, config.Definition, config.Environment} {
		if err := admitPath(relative); err != nil {
			return nil, err
		}
	}
	def, err := definition.LoadDefinition(ctx, config.Definition)
	if err != nil {
		return nil, err
	}
	used := false
	catalogs := map[string]bool{}
	selections := []definition.Catalog{def.Catalog}
	for _, output := range def.Outputs {
		if output.Catalog != nil {
			selections = append(selections, *output.Catalog)
		}
	}
	for _, selection := range selections {
		paths := append([]string{selection.Base}, selection.Overlays...)
		for i, selected := range paths {
			if filepath.IsAbs(selected) {
				return nil, e.New(e.CodeUsage, "maintenance catalogs must stay within the repository")
			}
			relative := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(config.Definition), selected)))
			if !safeRelative(relative) {
				return nil, e.New(e.CodeUsage, "maintenance catalog escapes repository: %q", relative)
			}
			if err := admitPath(relative); err != nil {
				return nil, err
			}
			if i == 0 {
				catalogs[relative] = true
				used = used || relative == config.Catalog
			}
		}
	}
	if !used {
		return nil, e.New(e.CodeUsage, "maintenance catalog is not selected by this Definition")
	}
	sources, err := catalog.ReadSourcesContext(ctx, catalog.Dir(config.Catalog))
	if err != nil {
		return nil, err
	}
	sourceAccessUsed := config.SourceCheckout == nil
	for _, name := range config.Sources {
		source, ok := sources[name]
		if !ok || source.PinnedBy != "" || strings.TrimSpace(source.Constraint) == "" {
			return nil, e.New(e.CodeInvalidCatalog, "maintenance source %q requires an independent root with an explicit release constraint", name)
		}
		if matchesSourceCheckout(config.SourceCheckout, source.Repo) {
			sourceAccessUsed = true
		}
	}
	if !sourceAccessUsed {
		return nil, e.New(e.CodeUsage, "source checkout does not transport a selected maintenance root")
	}
	if err := admitSourceCheckoutCatalogs(ctx, config, sources, catalogs); err != nil {
		return nil, err
	}
	plan, err := lifecycle.CompileFiles(ctx, config.Definition, config.Environment, generatorVersion)
	if err != nil {
		return nil, err
	}
	for _, build := range plan.Builds {
		for _, node := range build.Graph.Nodes {
			if err := admitSourceCheckoutRepo(config.SourceCheckout, node.Repo); err != nil {
				return nil, err
			}
		}
	}
	if config.Validation == "local-run" {
		if err := factory.AdmitLocalPlan(ctx, plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// Reject authored path symlinks before resolution. Owning loaders still hold and
// revalidate exact regular files; this is containment, not a concurrency receipt.
func admitPath(relative string) error {
	current := "."
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "reading maintenance path %q", relative)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return e.New(e.CodeInvalidCatalog, "maintenance paths cannot traverse symlinks: %q", relative)
		}
	}
	return nil
}
