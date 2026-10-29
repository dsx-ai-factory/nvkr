// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"maps"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// SourceRevision is a process-local exact-tree resolution, issued only after
// the existing Git pin owner authenticates the requested commit and metadata.
// It grants no producer execution, publication or release authority.
type SourceRevision struct {
	repository string
	pin        Pin
	issued     bool
}

type sourceStampDeclaration struct {
	repository string
	commit     string
	templates  map[string]string
	rendered   map[string]string
}

func sourceStampDeclarationFor(component Component, templates map[string]string) sourceStampDeclaration {
	return sourceStampDeclaration{repository: component.Repo, commit: component.Commit,
		templates: cloneStringMap(templates), rendered: cloneStringMap(component.VersionStamp)}
}

// ResolveSourceRevision authenticates one exact commit once. The repository
// policy belongs to the caller; .git and non-.git URLs identify the same source.
func ResolveSourceRevision(ctx context.Context, repository, commit string) (*SourceRevision, error) {
	if err := checkSourceRepoScheme("runtime-source", repository); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "runtime source resolution requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	pin, err := ResolveCommitPin(ctx, Source{Repo: repository}, commit)
	if err != nil {
		return nil, err
	}
	return &SourceRevision{repository: strings.TrimSuffix(repository, ".git"), pin: pin, issued: true}, nil
}

// ApplySourceRevision derives a complete overlay from the validated stack.
// Only matching from-source producers change. Acquired dependencies, commands,
// patches and toolchains retain their independently selected contracts.
func ApplySourceRevision(stack *Stack, revision *SourceRevision) (*Stack, error) {
	if stack == nil || stack.Catalog == nil || revision == nil || !revision.issued {
		return nil, e.New(e.CodeEngineCapability, "runtime source replacement requires a validated stack and owner-issued revision")
	}
	overlay := Overlay{APIVersion: OverlayAPIVersion, Kind: OverlayKind, Name: "runtime-source",
		RequiresCatalogDigest: stack.Digest, Components: make(map[string]ComponentReplacement)}
	declarations := make(map[string]sourceStampDeclaration)
	for _, name := range slices.Sorted(maps.Keys(stack.Catalog.Components)) {
		component := stack.Catalog.Components[name]
		if !component.FromSource || strings.TrimSuffix(component.Repo, ".git") != revision.repository {
			continue
		}
		declaration, known := stack.Catalog.stampDeclarations[name]
		if len(component.VersionStamp) != 0 && (!known || declaration.repository != component.Repo || declaration.commit != component.Commit || !maps.Equal(declaration.rendered, component.VersionStamp)) {
			return nil, e.New(e.CodeUnpinnedSource, "component %q has no owning version-stamp declaration for runtime source selection", name)
		}
		stamps, err := renderStamps(declaration.templates, revision.pin)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "component %q runtime version stamps", name)
		}
		component.Tag, component.Commit, component.TreeHash, component.SourceDate = revision.pin.Tag, revision.pin.Commit, revision.pin.TreeHash, revision.pin.SourceDateEpoch
		component.VersionStamp = stamps
		overlay.Components[name] = ComponentReplacement{Component: &component}
		declarations[name] = sourceStampDeclarationFor(component, declaration.templates)
	}
	if len(overlay.Components) == 0 {
		clone, err := ApplyStack(stack.Catalog)
		if err != nil {
			return nil, err
		}
		clone.Applied = append([]AppliedOverlay(nil), stack.Applied...)
		return clone, nil
	}
	result, err := ApplyStack(stack.Catalog, overlay)
	if err != nil {
		return nil, err
	}
	result.Applied = append(append([]AppliedOverlay(nil), stack.Applied...), result.Applied...)
	for name, declaration := range declarations {
		result.Catalog.stampDeclarations[name] = declaration
	}
	return result, nil
}
