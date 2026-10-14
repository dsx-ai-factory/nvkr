// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// This retained document is an inspected-content description. It is not a
// terminal receipt, release authorization or proof of exhaustive control coverage.
// Inspector ownership remains with its caller; the copied facade freezes the
// original container rather than following a later caller facade replacement.
type debianRunContentDocument struct {
	bytes     []byte
	inspector sbom.DiskInspectionRuntime
	profile   definition.DiskInspectionProfile
	runtime   string
}

// inspectBoundProducer borrows both original owners through scanning/cleanup and
// document composition. The Environment profile owns the scanner pin; this private
// operation neither enables Definition.SBOMTool nor opens lifecycle scheduling.
func (state *debianRunState) inspectBoundProducer(ctx context.Context, result *LocalRunResult,
	plan *Plan, output LifecycleOutputPlan, inspector *sbom.DiskInspectionRuntime,
) (*sbom.BOM, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "Debian content requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "inspecting original Debian output")
	}
	if inspector == nil {
		return nil, e.New(e.CodeEngineCapability, "Debian content requires an admitted inspector")
	}
	frozenInspector := *inspector
	owned, finish, err := state.begin(ctx, result, plan, output)
	if err != nil {
		return nil, err
	}
	defer finish()
	var original Plan
	if err := canonical.UnmarshalExact(state.plan, &original); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading original Debian inspection Plan")
	}
	state.mu.Lock()
	completion, exists := state.completions[output.ArtifactID]
	binding, bound := state.executors[output.ArtifactID]
	_, already := state.content[output.ArtifactID]
	state.mu.Unlock()
	if !exists || !bound {
		return nil, e.New(e.CodeProvenanceMissing, "original named Debian completion is absent")
	}
	if already {
		return nil, e.New(e.CodeVerifyFailed, "named Debian content document already exists")
	}
	description, err := completion.archive.DescribeContext(owned)
	if err != nil {
		return nil, err
	}
	if err := binding.executor.RequireArchiveContext(owned, output.ArtifactID, &completion.archive, description.InputDigest); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(description)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "encoding original Debian completion")
	}
	want, err := canonical.Marshal(completion.description)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "encoding stored Debian completion")
	}
	if !bytes.Equal(raw, want) || description.BuildDigest != output.BuildDigest || description.Subject != output.ArtifactID {
		return nil, e.New(e.CodeVerifyFailed, "Debian content changes original producer completion")
	}
	host := runtime.GOOS + "/" + runtime.GOARCH
	if description.Instruction.Debian == nil || host != description.Instruction.Platform {
		return nil, e.New(e.CodeEngineCapability, "Debian content requires original native producer host")
	}
	selection, err := frozenInspector.SelectionContext(owned)
	if err != nil {
		return nil, err
	}
	if selection.Platform != host {
		return nil, e.New(e.CodeVerifyFailed, "Debian inspector changes original native host")
	}
	profile, err := diskEvidenceProfile(owned, &original, &frozenInspector)
	if err != nil {
		return nil, err
	}
	runtimeID, _, _, err := frozenInspector.BindingContext(owned)
	if err != nil {
		return nil, err
	}
	inspected, err := frozenInspector.InspectDebianProducerContent(owned, &completion.archive)
	if err != nil {
		return nil, err
	}
	subject, size, kind, platform, observedRuntime, scanner := inspected.Binding()
	if subject != description.Digest || size != description.Size || kind != "host-package" || platform != description.Instruction.Debian.Platform || observedRuntime != runtimeID || scanner != profile.ScannerDigest {
		return nil, e.New(e.CodeVerifyFailed, "Debian content changes original bytes/target/profile")
	}
	document, err := sbom.DiskContentDocument(output.BuildDigest, description.Instruction.Component, inspected)
	if err != nil {
		return nil, err
	}
	var build BuildPlan
	var selected definition.DefinitionOutput
	for _, candidate := range original.Builds {
		if candidate.BuildDigest == output.BuildDigest && candidate.Name == output.Name {
			build = candidate
			break
		}
	}
	for _, candidate := range original.Definition.Outputs {
		if candidate.Name == build.Name {
			selected = candidate
			break
		}
	}
	if build.Graph == nil {
		return nil, e.New(e.CodeVerifyFailed, "named Debian content has no original Build")
	}
	if err := validateBuildCatalog(build, selected, original.Environment); err != nil {
		return nil, err
	}
	resolved, err := buildgraph.Resolve(build.CatalogSnapshot, buildgraph.Selection{Components: selected.Components, Platform: selected.Platform, OutputFormat: selected.Format, Debian: debianOutputRequest(selected)})
	if err != nil {
		return nil, err
	}
	sources, _, err := sbom.PerArtifact(resolved, output.BuildDigest, map[string]string{description.Instruction.Component: description.Digest})
	if err != nil {
		return nil, err
	}
	source := sources[description.Instruction.Component]
	if source == nil {
		return nil, e.New(e.CodeProvenanceMissing, "Debian content lacks original source description")
	}
	document, err = sbom.WithSourceSubject(document, source)
	if err != nil {
		return nil, err
	}
	finalProfile, err := diskEvidenceProfile(owned, &original, &frozenInspector)
	if err != nil {
		return nil, err
	}
	finalRuntime, _, _, err := frozenInspector.BindingContext(owned)
	if err != nil {
		return nil, err
	}
	if finalProfile != profile || finalRuntime != runtimeID {
		return nil, e.New(e.CodeVerifyFailed, "Debian inspector changed during document composition")
	}
	if err := binding.executor.RequireArchiveContext(owned, output.ArtifactID, &completion.archive, description.InputDigest); err != nil {
		return nil, err
	}
	data, err := document.Marshal()
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := owned.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "recording original Debian content")
	}
	if state.closed {
		return nil, e.New(e.CodeEngineCapability, "original Debian Run closed before document transfer")
	}
	if _, exists := state.content[output.ArtifactID]; exists {
		return nil, e.New(e.CodeVerifyFailed, "named Debian content document already exists")
	}
	if state.content == nil {
		state.content = make(map[string]debianRunContentDocument)
	}
	state.content[output.ArtifactID] = debianRunContentDocument{bytes: data, inspector: frozenInspector, profile: profile, runtime: runtimeID}
	return document, nil
}
