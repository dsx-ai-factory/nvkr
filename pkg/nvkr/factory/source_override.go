// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// SourceOverride freezes one operator-selected repository revision for a local
// or development Run. Request is the operator's constraint or full commit; Tag
// and Commit are its resolution.
// A commit request records the commit as its tag, as `catalog pin --commit` does.
type SourceOverride struct {
	Request string `json:"request"`
	Tag     string `json:"tag"`
	Commit  string `json:"commit"`
}

// DeclaredSourceOverride is one Environment-declared overridable source: the
// name a Run selects it by and the from-source repository it overrides.
type DeclaredSourceOverride struct {
	Name       string
	Repository string
}

// DeclaredSourceOverrides returns the Environment's overridable sources in name
// order. Every declared repository must be built from source by the Plan. The
// result depends only on authored declarations, never on pins, so refreshed
// runtime Plans keep workflow bytes.
func DeclaredSourceOverrides(plan *Plan) ([]DeclaredSourceOverride, error) {
	if plan == nil || plan.Environment.Overridable == nil {
		return nil, nil
	}
	built := fromSourceRepositories(plan)
	sources := plan.Environment.Overridable.Sources
	declared := make([]DeclaredSourceOverride, 0, len(sources))
	for _, name := range sortedKeys(sources) {
		repository := sources[name].Repository
		if !built[repository] {
			return nil, e.New(e.CodeInvalidCatalog, "overridable source %q selects %s, which no selected output builds from source", name, repository)
		}
		declared = append(declared, DeclaredSourceOverride{Name: name, Repository: repository})
	}
	return declared, nil
}

// SourceOverrideRequest selects one declared overridable source by name and
// asks to build it at a version constraint or full commit instead of its
// reviewed pin. An empty request keeps the pin.
type SourceOverrideRequest struct {
	Name    string
	Request string
}

// AdmitSourceOverrideRequests is the single intake for override requests from
// every lane. It maps each declared name to its repository and drops empty
// requests. An Environment without overridable sources, an undeclared name, a
// name requested twice or a malformed request fails closed. Lane scope and
// resolution remain with compilation.
func AdmitSourceOverrideRequests(environment authored.Environment, requests []SourceOverrideRequest) (map[string]string, error) {
	if environment.Overridable == nil {
		return nil, e.New(e.CodeEngineCapability, "source overrides require overridable sources in the Environment")
	}
	sources := environment.Overridable.Sources
	seen := make(map[string]bool, len(requests))
	admitted := make(map[string]string, len(requests))
	for _, request := range requests {
		source, declared := sources[request.Name]
		if !declared {
			return nil, e.New(e.CodeUsage, "source override %q is not declared in the Environment's overridable sources (declared: %s)",
				request.Name, strings.Join(sortedKeys(sources), ", "))
		}
		if seen[request.Name] {
			return nil, e.New(e.CodeUsage, "source override %q is requested more than once", request.Name)
		}
		seen[request.Name] = true
		if request.Request == "" {
			continue
		}
		if _, err := catalog.SourceOverrideIsCommit(request.Request); err != nil {
			return nil, e.Wrap(e.CodeUsage, err, "source override %q", request.Name)
		}
		admitted[source.Repository] = request.Request
	}
	return admitted, nil
}

// GitHubSourceOverrideRequests reads the dispatch's JSON input object, whose
// keys are declared source names, and admits it through
// AdmitSourceOverrideRequests. A non-string value fails closed.
func GitHubSourceOverrideRequests(plan *Plan, raw string) (map[string]string, error) {
	if plan == nil || !plan.Environment.GitHubDevelopment || plan.Environment.Overridable == nil {
		return nil, e.New(e.CodeEngineCapability, "dispatch source overrides require a development Environment with overridable sources")
	}
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return map[string]string{}, nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, e.Wrap(e.CodeUsage, err, "source overrides must be a JSON object of dispatch inputs")
	}
	requests := make([]SourceOverrideRequest, 0, len(values))
	for _, name := range sortedKeys(values) {
		request, ok := values[name].(string)
		if !ok {
			return nil, e.New(e.CodeUsage, "source override %q must be a string", name)
		}
		requests = append(requests, SourceOverrideRequest{Name: name, Request: request})
	}
	return AdmitSourceOverrideRequests(plan.Environment, requests)
}

// CompileDevelopmentSourceOverrideDocuments resolves a development dispatch's requests
// once, then recompiles the unchanged authored selection through catalog-owned
// overlays. No lock, registry, command or toolchain is rewritten.
func CompileDevelopmentSourceOverrideDocuments(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, environment authored.Environment, generatorVersion string, requests map[string]string,
) (*Plan, error) {
	return compileSourceOverrides(ctx, definitionPath, definition, environmentPath, environment, generatorVersion, requests,
		developmentSourceOverrideScope, resolveSourceOverride)
}

// CompileLocalSourceOverrideDocuments resolves operator-selected overrides for
// one local Run through the same declarations, resolution and freeze. The
// baseline Plan must pass static local admission before any source fetch.
func CompileLocalSourceOverrideDocuments(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, environment authored.Environment, generatorVersion string, requests map[string]string,
) (*Plan, error) {
	return compileSourceOverrides(ctx, definitionPath, definition, environmentPath, environment, generatorVersion, requests,
		localSourceOverrideScope, resolveSourceOverride)
}

func resolveSourceOverride(ctx context.Context, repository, tagPrefix, request string) (*catalog.SourceRevision, error) {
	if commit, err := catalog.SourceOverrideIsCommit(request); err != nil {
		return nil, err
	} else if commit {
		return catalog.ResolveSourceRevision(ctx, repository, request)
	}
	return catalog.ResolveSourceConstraint(ctx, repository, tagPrefix, request)
}

// ReplayDevelopmentSourceOverrideDocuments re-authenticates a Plan's frozen overrides
// without resolving constraints again; a moved tag or rewritten commit fails.
func ReplayDevelopmentSourceOverrideDocuments(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, environment authored.Environment, generatorVersion string, frozen map[string]SourceOverride,
) (*Plan, error) {
	requests := make(map[string]string, len(frozen))
	for repository, override := range frozen {
		requests[repository] = override.Request
	}
	return compileSourceOverrides(ctx, definitionPath, definition, environmentPath, environment, generatorVersion, requests,
		developmentSourceOverrideScope, func(ctx context.Context, repository, tagPrefix, request string) (*catalog.SourceRevision, error) {
			override := frozen[repository]
			commit, err := catalog.SourceOverrideIsCommit(request)
			if err != nil {
				return nil, err
			}
			if commit {
				if override.Tag != request || override.Commit != request {
					return nil, e.New(e.CodeUnpinnedSource, "frozen commit override for %s is inconsistent", repository)
				}
				return catalog.ResolveSourceRevision(ctx, repository, request)
			}
			if !catalog.SourceTagAdmitted(tagPrefix, request, override.Tag) {
				return nil, e.New(e.CodeUnpinnedSource, "frozen tag %q for %s does not satisfy constraint %q", override.Tag, repository, request)
			}
			return catalog.ResolveSourceTag(ctx, repository, tagPrefix, override.Tag, override.Commit)
		})
}

type sourceOverrideResolver func(ctx context.Context, repository, tagPrefix, request string) (*catalog.SourceRevision, error)

// sourceOverrideScope holds the only lane differences: which Environments a
// lane admits and any baseline admission it requires before a source fetch.
// Both lanes override exactly the Environment's declared sources.
type sourceOverrideScope struct {
	admitEnvironment func(authored.Environment) error
	admitBaseline    func(context.Context, *Plan) error
}

var developmentSourceOverrideScope = sourceOverrideScope{
	admitEnvironment: func(environment authored.Environment) error {
		if !environment.GitHubDevelopment || environment.Overridable == nil {
			return e.New(e.CodeEngineCapability, "dispatch source overrides require a development Environment with overridable sources")
		}
		return nil
	},
	admitBaseline: func(context.Context, *Plan) error { return nil },
}

var localSourceOverrideScope = sourceOverrideScope{
	admitEnvironment: func(environment authored.Environment) error {
		if environment.GitHubDevelopment || environment.Overridable == nil {
			return e.New(e.CodeEngineCapability, "local source overrides require a local Environment with overridable sources")
		}
		return nil
	},
	admitBaseline: AdmitLocalPlan,
}

func fromSourceRepositories(plan *Plan) map[string]bool {
	built := map[string]bool{}
	for _, build := range plan.Builds {
		for _, component := range build.ResolvedComponents {
			if component.FromSource {
				built[strings.TrimSuffix(component.Repo, ".git")] = true
			}
		}
	}
	return built
}

type resolvedSourceOverride struct {
	tagPrefix string
	revision  *catalog.SourceRevision
}

func compileSourceOverrides(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, environment authored.Environment, generatorVersion string, requests map[string]string,
	scope sourceOverrideScope, resolve sourceOverrideResolver,
) (*Plan, error) {
	if err := admitInputs(definition, environment); err != nil {
		return nil, err
	}
	if err := scope.admitEnvironment(environment); err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, e.New(e.CodeEngineCapability, "source override compilation requires at least one request")
	}
	// Reject malformed producer graphs and unknown repositories before any fetch.
	baseline, err := CompileDocuments(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
	if err != nil {
		return nil, err
	}
	if err := scope.admitBaseline(ctx, baseline); err != nil {
		return nil, err
	}
	declared, err := DeclaredSourceOverrides(baseline)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(declared))
	for _, source := range declared {
		known[source.Repository] = true
	}
	repositories := make([]string, 0, len(requests))
	for repository, request := range requests {
		if !known[repository] {
			return nil, e.New(e.CodeUsage, "source override %q selects no overridable source of this Run", repository)
		}
		if _, err := catalog.SourceOverrideIsCommit(request); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	sort.Strings(repositories)
	resolved := map[string]resolvedSourceOverride{}
	plan, err := compileDocumentsWithLoader(ctx, definitionPath, definition, environmentPath, environment, generatorVersion,
		func(ctx context.Context, selection authored.Catalog) (*catalog.Stack, error) {
			stack, err := loadFactoryCatalogStack(ctx, definitionPath, selection)
			if err != nil {
				return nil, err
			}
			for _, repository := range repositories {
				tagPrefix, used, err := sourceOverrideTagPrefix(stack.Catalog, repository)
				if err != nil || !used {
					if err != nil {
						return nil, err
					}
					continue
				}
				commit, _ := catalog.SourceOverrideIsCommit(requests[repository])
				previous, exists := resolved[repository]
				if exists && !commit && previous.tagPrefix != tagPrefix {
					return nil, e.New(e.CodeInvalidCatalog, "source %s uses tag prefixes %q and %q; override it with a full commit", repository, previous.tagPrefix, tagPrefix)
				}
				if !exists {
					revision, err := resolve(ctx, repository, tagPrefix, requests[repository])
					if err != nil {
						return nil, err
					}
					previous = resolvedSourceOverride{tagPrefix: tagPrefix, revision: revision}
					resolved[repository] = previous
				}
				if stack, err = catalog.ApplySourceRevision(stack, previous.revision); err != nil {
					return nil, err
				}
			}
			return stack, nil
		})
	if err != nil {
		return nil, err
	}
	plan.SourceOverrides = make(map[string]SourceOverride, len(repositories))
	for _, repository := range repositories {
		revision := resolved[repository].revision
		if revision == nil {
			return nil, e.New(e.CodeUnpinnedSource, "source override %s matched no selected catalog", repository)
		}
		plan.SourceOverrides[repository] = SourceOverride{Request: requests[repository], Tag: revision.Tag(), Commit: revision.Commit()}
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

// sourceOverrideTagPrefix reports whether a stack builds the repository from
// source and the single module tag prefix its build sources declare.
func sourceOverrideTagPrefix(snapshot *catalog.Catalog, repository string) (string, bool, error) {
	used := false
	for _, component := range snapshot.Components {
		used = used || (component.FromSource && strings.TrimSuffix(component.Repo, ".git") == repository)
	}
	prefixes := map[string]bool{}
	for _, source := range snapshot.Sources {
		if source.Kind == "" && source.Tool == nil && strings.TrimSuffix(source.Repo, ".git") == repository {
			prefixes[source.TagPrefix] = true
		}
	}
	if !used {
		return "", false, nil
	}
	if len(prefixes) > 1 {
		return "", true, e.New(e.CodeInvalidCatalog, "source %s declares several tag prefixes in one catalog", repository)
	}
	for prefix := range prefixes {
		return prefix, true, nil
	}
	return "", true, nil
}

func (plan Plan) validateSourceOverrides() error {
	if len(plan.SourceOverrides) == 0 {
		return nil
	}
	if plan.Environment.Overridable == nil || plan.RuntimeSourceCommit != "" {
		return e.New(e.CodeEngineCapability, "frozen source overrides require overridable sources without a runtime source commit")
	}
	declared := map[string]bool{}
	for _, source := range plan.Environment.Overridable.Sources {
		declared[source.Repository] = true
	}
	for _, repository := range sortedKeys(plan.SourceOverrides) {
		if !declared[repository] {
			return e.New(e.CodeEngineCapability, "frozen source override %s is not declared in overridable sources", repository)
		}
		override := plan.SourceOverrides[repository]
		commit, err := catalog.SourceOverrideIsCommit(override.Request)
		if err != nil {
			return err
		}
		if !runtimeSourceCommitValid(override.Commit) || override.Tag == "" ||
			(commit && (override.Tag != override.Commit || override.Request != override.Commit)) || (!commit && override.Tag == override.Commit) {
			return e.New(e.CodeUnpinnedSource, "frozen source override for %s is incomplete", repository)
		}
		matched := 0
		for _, build := range plan.Builds {
			for _, component := range build.ResolvedComponents {
				if !component.FromSource || strings.TrimSuffix(component.Repo, ".git") != repository {
					continue
				}
				matched++
				if component.Commit != override.Commit || component.Tag != override.Tag {
					return e.New(e.CodeUnpinnedSource, "output %q component %q differs from the frozen override of %s", build.Name, component.Name, repository)
				}
			}
		}
		if matched == 0 {
			return e.New(e.CodeUnpinnedSource, "frozen source override %s matches no selected from-source producer", repository)
		}
	}
	return nil
}
