// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// nil retains complete metadata traversal for all existing callers.
type metadataSelection map[string]bool

func newMetadataSelection(names []string) (metadataSelection, error) {
	if len(names) == 0 || len(names) > maxDeliveryObjects {
		return nil, e.New(e.CodeUsage, "selected metadata requires a bounded nonempty output set")
	}
	selection := make(metadataSelection, len(names))
	previous := ""
	for _, name := range names {
		if !canonicalToken(name) || strings.ContainsAny(name, "/\\*?[]") || name <= previous {
			return nil, e.New(e.CodeUsage, "selected metadata outputs must have sorted unique exact names")
		}
		previous = name
		selection[name] = true
	}
	return selection, nil
}

func (selection metadataSelection) includes(object PublishedObject) bool {
	if selection == nil {
		return true
	}
	if object.Output != nil {
		return selection[object.Name]
	}
	if object.Evidence != nil {
		for _, output := range object.Evidence.Outputs {
			if selection[output] {
				return true
			}
		}
	}
	return false
}

func (selection metadataSelection) requireOutputs(root PublicationRoot) error {
	if selection == nil {
		return nil
	}
	seen := 0
	for _, object := range root.Objects {
		if object.Output != nil && selection[object.Name] {
			seen++
		}
	}
	if seen != len(selection) {
		return e.New(e.CodeVerifyFailed, "selected metadata omits an expected named output")
	}
	return nil
}

// InspectSelectedMetadata checks the intact original root and complete logical
// evidence graph, but reads controls and evidence bytes only for the named set.
// It accepts only that bounded physical closure, never payloads or extra blobs.
// The result describes integrity, not signatures or acceptance of omitted bytes.
func InspectSelectedMetadata(ctx context.Context, root, expectedRoot string, names []string) (Inspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return Inspection{}, err
	}
	selection, err := newMetadataSelection(names)
	if err != nil {
		return Inspection{}, err
	}
	if !isDigest(expectedRoot) {
		return Inspection{}, e.New(e.CodeUsage, "selected metadata requires an exact original root digest")
	}
	return inspectSelectedMetadata(ctx, root, false, expectedRoot, nil, selection)
}

// CopySelectedMetadataWithExpectedRoot exports only the named outputs' controls
// and evidence from a complete metadata source. Original bootstrap bytes and
// shared evidence remain intact. Output payloads are never read or copied.
func CopySelectedMetadataWithExpectedRoot(ctx context.Context, source, out, expectedRoot string, names []string) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	selection, err := newMetadataSelection(names)
	if err != nil {
		return Result{}, err
	}
	if !isDigest(expectedRoot) {
		return Result{}, e.New(e.CodeUsage, "selected metadata requires an exact original root digest")
	}
	inspection, err := inspectMetadataProgress(ctx, source, true, expectedRoot, nil)
	if err != nil {
		return Result{}, err
	}
	sources := make(map[string]string, len(inspection.Root.Objects))
	for _, object := range inspection.Root.Objects {
		if selection.includes(object) {
			sources[object.Name] = source
		}
	}
	return buildSelectedMetadataRoot(ctx, out, inspection.Root, sources, expectedRoot, source, selection)
}

// VisitSelectedMetadataEvidence checks the sparse physical closure before
// visiting the original attested documents needed by the named outputs. The
// visitor owns signature policy; success does not authenticate omitted outputs.
func VisitSelectedMetadataEvidence(ctx context.Context, root, expectedRoot string, names []string, visit func(context.Context, string, []byte) error) error {
	if visit == nil {
		return e.New(e.CodeUsage, "selected metadata evidence requires a visitor")
	}
	inspection, err := InspectSelectedMetadata(ctx, root, expectedRoot, names)
	if err != nil {
		return err
	}
	selection, err := newMetadataSelection(names)
	if err != nil {
		return err
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence != nil && selection.includes(object) && object.ArtifactType != sbom.BundleMediaType {
			return e.New(e.CodeProvenanceMissing, "selected evidence requires an original attestation")
		}
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil || !selection.includes(object) {
			continue
		}
		raw, err := metadataEvidence(ctx, root, object)
		if err != nil {
			return err
		}
		if err := visit(ctx, object.Evidence.SubjectDigest, raw); err != nil {
			return err
		}
	}
	return nil
}
