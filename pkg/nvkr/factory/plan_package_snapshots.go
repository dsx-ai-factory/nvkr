// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// The wire Plan shares exact package-input documents, including acquisition
// locations. Executable descriptions remain complete in memory; Build and catalog
// identities still use their original descriptions. These are blob references,
// not runtime receipts or permission to reuse another requirement's result.
type planValue Plan

type planDocument struct {
	*planValue
	PackageSnapshots map[string]packageSnapshot `json:"packageSnapshots,omitempty"`
}

type packageSnapshot struct {
	Digest string                 `json:"digest"`
	Inputs material.PackageInputs `json:"inputs"`
}

const (
	// New wire majors preserve the meaning and bytes of Plans without mkosi
	// snapshots. The frozen nvkr.run-plan digest family remains unchanged.
	sharedPlanSchemaVersion          = "nvkr.dev/run-plan/v8"
	sharedCompositePlanSchemaVersion = "nvkr.dev/run-plan/v9"
	sharedBootPlanSchemaVersion      = "nvkr.dev/run-plan/v10"
	maxPlanPackageSnapshots          = 64
	maxPlanPackageReferences         = 1024
	// Bound expansion independently of the unchanged 8 MiB wire read limit.
	maxPlanPackageExpansion = 32 << 20
)

func packageSnapshotPath(digest string) string {
	return "store/package-inputs@" + strings.TrimPrefix(digest, "sha256:")[:12] + ".json"
}

// MarshalJSON emits the same shared representation for every Plan consumer,
// including identity, projection, capture/replay, and runtime installation.
func (plan Plan) MarshalJSON() ([]byte, error) {
	snapshots := make(map[string]packageSnapshot)
	var expanded, references int
	copy, err := mapPlanPackageInputs(plan, func(root *mkosi.Root) error {
		if root.InputsDigest != "" {
			return e.New(e.CodePipelineDrift, "cannot serialize unresolved Plan package inputs")
		}
		raw, err := root.Inputs.Marshal()
		if err != nil {
			return err
		}
		if err := admitPackageExpansion(&expanded, &references, len(raw)); err != nil {
			return err
		}
		digest := pipeline.FileDigest(raw)
		path := packageSnapshotPath(digest)
		if previous, ok := snapshots[path]; ok && previous.Digest != digest {
			return e.New(e.CodePipelineDrift, "Plan package snapshot path collision")
		}
		snapshots[path] = packageSnapshot{Digest: digest, Inputs: root.Inputs}
		if len(snapshots) > maxPlanPackageSnapshots {
			return e.New(e.CodePipelineDrift, "Plan package snapshot count exceeds its bound")
		}
		root.Inputs = material.PackageInputs{}
		root.InputsDigest = digest
		return nil
	})
	if err != nil {
		return nil, err
	}
	value := planValue(copy)
	shared := isSharedPackageSchema(value.SchemaVersion)
	if shared != (len(snapshots) != 0) {
		return nil, e.New(e.CodeInvalidCatalog, "Plan package snapshots do not match its schema")
	}
	return canonical.Marshal(planDocument{planValue: &value, PackageSnapshots: snapshots})
}

// UnmarshalJSON authenticates all targets and budgets expansion before restoring
// any executable description. Normal Plan validation then recompiles every named
// Build and verifies its catalog, platform, graph and evidence membership.
func (plan *Plan) UnmarshalJSON(raw []byte) error {
	if int64(len(raw)) > MaxPlanBytes {
		// Display JSON may exceed the packed limit through indentation alone.
		// File readers bound external bytes before decoding; UnmarshalExact
		// separately rejects whitespace-bearing representations as authority.
		if int64(len(raw)) > maxGeneratedFileBytes {
			return e.New(e.CodePipelineDrift, "Plan display exceeds its size bound")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading Plan display")
		}
		if int64(compact.Len()) > MaxPlanBytes {
			return e.New(e.CodePipelineDrift, "Plan exceeds its %d-byte read bound", MaxPlanBytes)
		}
		raw = compact.Bytes()
	}
	schema, err := admitPlanDocumentSchema(raw)
	if err != nil {
		return err
	}
	value := new(planValue)
	document := planDocument{planValue: value}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding Plan package snapshots")
	}
	shared := isSharedPackageSchema(schema)
	if shared != (len(document.PackageSnapshots) != 0) {
		return e.New(e.CodePipelineDrift, "Plan package snapshots do not match its schema")
	}
	if len(document.PackageSnapshots) > maxPlanPackageSnapshots {
		return e.New(e.CodePipelineDrift, "Plan package snapshot count exceeds its bound")
	}
	byDigest := make(map[string]packageSnapshot, len(document.PackageSnapshots))
	sizes := make(map[string]int, len(document.PackageSnapshots))
	for path, snapshot := range document.PackageSnapshots {
		input, err := snapshot.Inputs.Marshal()
		if err != nil {
			return err
		}
		if !canonical.IsDigest(snapshot.Digest) || pipeline.FileDigest(input) != snapshot.Digest || path != packageSnapshotPath(snapshot.Digest) {
			return e.New(e.CodePipelineDrift, "Plan package snapshot path or exact bytes differ from its digest")
		}
		byDigest[snapshot.Digest] = snapshot
		sizes[snapshot.Digest] = len(input)
	}
	var expanded, references int
	used := make(map[string]bool, len(byDigest))
	// First pass only admits references. No repeated package slices are allocated
	// until every reference and the aggregate expansion budget have been checked.
	_, err = mapPlanPackageInputs(Plan(*value), func(root *mkosi.Root) error {
		if !reflect.DeepEqual(root.Inputs, material.PackageInputs{}) || root.InputsDigest == "" {
			return e.New(e.CodePipelineDrift, "Plan package inputs require exactly one digest reference")
		}
		if _, ok := byDigest[root.InputsDigest]; !ok {
			return e.New(e.CodePipelineDrift, "Plan package snapshot reference is missing")
		}
		used[root.InputsDigest] = true
		return admitPackageExpansion(&expanded, &references, sizes[root.InputsDigest])
	})
	if err != nil {
		return err
	}
	if len(used) != len(byDigest) {
		return e.New(e.CodePipelineDrift, "Plan has unused package snapshots")
	}
	restored, err := mapPlanPackageInputs(Plan(*value), func(root *mkosi.Root) error {
		root.Inputs = byDigest[root.InputsDigest].Inputs
		root.Inputs.Repositories = slices.Clone(root.Inputs.Repositories)
		root.Inputs.Packages = slices.Clone(root.Inputs.Packages)
		root.InputsDigest = ""
		return nil
	})
	if err != nil {
		return err
	}
	*plan = restored
	return nil
}

func admitPlanDocumentSchema(raw []byte) (string, error) {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return "", e.Wrap(e.CodePipelineDrift, err, "reading Plan document")
	}
	var header struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return "", e.Wrap(e.CodePipelineDrift, err, "reading Plan schema")
	}
	switch header.SchemaVersion {
	case PlanSchemaVersion, CompositePlanSchemaVersion, BootPlanSchemaVersion, sharedPlanSchemaVersion, sharedCompositePlanSchemaVersion, sharedBootPlanSchemaVersion:
		return header.SchemaVersion, nil
	default:
		return "", e.New(e.CodeInvalidCatalog, "unsupported Plan schema %q", header.SchemaVersion)
	}
}

func isSharedPackageSchema(schema string) bool {
	return schema == sharedPlanSchemaVersion || schema == sharedCompositePlanSchemaVersion || schema == sharedBootPlanSchemaVersion
}

func planSchemaFor(plan Plan) string {
	hasPackages := false
	_, _ = mapPlanPackageInputs(plan, func(*mkosi.Root) error { hasPackages = true; return nil })
	if plan.BootPublication != nil {
		if hasPackages {
			return sharedBootPlanSchemaVersion
		}
		return BootPlanSchemaVersion
	}
	if plan.Composition != nil {
		if hasPackages {
			return sharedCompositePlanSchemaVersion
		}
		return CompositePlanSchemaVersion
	}
	if hasPackages {
		return sharedPlanSchemaVersion
	}
	return PlanSchemaVersion
}

func admitPackageExpansion(expanded, references *int, size int) error {
	if *references >= maxPlanPackageReferences || size > maxPlanPackageExpansion-*expanded {
		return e.New(e.CodePipelineDrift, "Plan package snapshot expansion exceeds its bound")
	}
	*references += 1
	*expanded += size
	return nil
}

// Copy only the containers on paths to recipes. Neither encoding nor decoding
// may mutate a caller's catalog, resolved components or producer instructions.
func mapPlanPackageInputs(plan Plan, visit func(*mkosi.Root) error) (Plan, error) {
	recipe := func(spec *mkosi.Spec) (*mkosi.Spec, error) {
		if spec == nil {
			return nil, nil
		}
		copy := *spec
		copy.Roots = slices.Clone(spec.Roots)
		for i := range copy.Roots {
			if err := visit(&copy.Roots[i]); err != nil {
				return nil, err
			}
		}
		return &copy, nil
	}
	catalogCopy := func(source *catalog.Catalog) (*catalog.Catalog, error) {
		if source == nil {
			return nil, nil
		}
		copy := *source
		copy.Components = maps.Clone(source.Components)
		for _, name := range sortedKeys(copy.Components) {
			component := copy.Components[name]
			var err error
			component.Mkosi, err = recipe(component.Mkosi)
			if err != nil {
				return nil, err
			}
			copy.Components[name] = component
		}
		return &copy, nil
	}
	var err error
	plan.CatalogSnapshot, err = catalogCopy(plan.CatalogSnapshot)
	if err != nil {
		return Plan{}, err
	}
	plan.CatalogCandidates = slices.Clone(plan.CatalogCandidates)
	for i := range plan.CatalogCandidates {
		plan.CatalogCandidates[i].CatalogSnapshot, err = catalogCopy(plan.CatalogCandidates[i].CatalogSnapshot)
		if err != nil {
			return Plan{}, err
		}
	}
	plan.Builds = slices.Clone(plan.Builds)
	for i := range plan.Builds {
		build := &plan.Builds[i]
		build.CatalogSnapshot, err = catalogCopy(build.CatalogSnapshot)
		if err != nil {
			return Plan{}, err
		}
		build.ResolvedComponents = slices.Clone(build.ResolvedComponents)
		for j := range build.ResolvedComponents {
			component := &build.ResolvedComponents[j]
			component.Mkosi, err = recipe(component.Mkosi)
			if err != nil {
				return Plan{}, err
			}
		}
		if build.Graph == nil {
			continue
		}
		graph := *build.Graph
		graph.Nodes = slices.Clone(build.Graph.Nodes)
		build.Graph = &graph
		for j := range graph.Nodes {
			node := &graph.Nodes[j]
			node.Mkosi, err = recipe(node.Mkosi)
			if err != nil {
				return Plan{}, err
			}
		}
	}
	return plan, nil
}
