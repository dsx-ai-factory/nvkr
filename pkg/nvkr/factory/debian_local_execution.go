// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"golang.org/x/sys/unix"
)

func hasLocalDebianBuild(plan *Plan) bool {
	if plan == nil {
		return false
	}
	for _, output := range plan.Definition.Outputs {
		if output.Format == authored.OutputDebianPackage {
			return true
		}
	}
	return false
}

func admitLocalDebianPlan(ctx context.Context, plan *Plan) error {
	if !hasLocalDebianBuild(plan) {
		return nil
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "native package execution requires Linux")
	}
	if _, ok := ctx.Deadline(); !ok {
		return e.New(e.CodeEngineCapability, "native package execution requires bounded context")
	}
	if len(plan.Builds) != 1 || len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Kind != authored.OutputDebianPackage || plan.Definition.SBOMTool == "" {
		return e.New(e.CodeEngineCapability, "local native package execution requires one standalone inspected Debian output")
	}
	build := plan.Builds[0]
	if build.Platform != "linux/"+runtime.GOARCH {
		return e.New(e.CodeEngineCapability, "native package target must match execution host")
	}
	scanner, ok := plan.CatalogSnapshot.Tools[plan.Definition.SBOMTool]
	if !ok {
		return e.New(e.CodeProvenanceMissing, "native package requires selected catalog scanner")
	}
	if _, err := debianInspectionProfile(build, plan.Environment, scanner); err != nil {
		return err
	}
	for _, node := range plan.Graph.Nodes {
		switch node.Kind {
		case NodeFetch, NodeBuild, NodeSBOM, NodeVerify:
			if node.BuildTarget != build.Name {
				return e.New(e.CodeEngineCapability, "native package graph contains foreign output work")
			}
		case NodeAcquire:
			if node.ID != "tool/"+plan.Definition.SBOMTool || node.IRNode != "" {
				return e.New(e.CodeEngineCapability, "native package graph contains unsupported acquisition")
			}
		default:
			return e.New(e.CodeEngineCapability, "native package graph contains unsupported operation %s", node.Kind)
		}
	}
	return nil
}

func executeLocalDebianNode(ctx context.Context, plan *Plan, node Node, options LocalRunOptions, owner *LocalRunResult) error {
	if owner == nil || owner.debian == nil {
		return e.New(e.CodeProvenanceMissing, "native package scheduler has no original Run")
	}
	output := plan.LifecycleOutputs[0]
	owned, finish, err := owner.debian.begin(ctx, owner, plan, output)
	if err != nil {
		return err
	}
	defer finish()
	_, artifacts := planIndex(plan)
	switch node.Kind {
	case NodeAcquire:
		if err := owner.debian.resetNodeOutputs(plan, node); err != nil {
			return err
		}
		artifact := artifacts[node.Outputs[0]]
		artifact.Path, err = relocatedFactoryPath(artifact.Path, owner.debian.workspace)
		if err != nil {
			return err
		}
		return executeToolAcquireWithWriter(owned, node, artifact, options.Stdout, owner.debian.writeOwnedFile)
	case NodeFetch:
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			return err
		}
		source := artifacts["source/"+node.BuildTarget+"/"+node.IRNode]
		path, err := relocatedFactoryPath(source.Path, owner.debian.workspace)
		if err != nil {
			return err
		}
		suffix := "/" + instruction.Dest
		if !strings.HasSuffix(filepath.ToSlash(path), suffix) {
			return e.New(e.CodePipelineDrift, "native source destination changed")
		}
		sourceRoot := strings.TrimSuffix(filepath.ToSlash(path), suffix)
		result, err := owner.debian.session.ExecNode(instruction, pipeline.RunOptions{Ctx: owned, SourceRoot: sourceRoot, SourceWorkspace: owner.debian.sourceWorkspace, Runtime: options.Runtime, Launch: true, Stdout: options.Stdout, Stderr: options.Stderr})
		if err != nil {
			return err
		}
		if result != nil {
			return e.New(e.CodeVerifyFailed, "native source fetch returned artifact")
		}
		return nil
	case NodeBuild:
		return owner.debian.executeBoundProducer(owned, owner, plan, output, options.Runtime, options.Stdout, options.Stderr)
	case NodeSBOM:
		if err := owner.debian.resetNodeOutputs(plan, node); err != nil {
			return err
		}
		document, err := owner.debian.inspectBoundProducer(owned, owner, plan, output, owner.debian.ownedInspection)
		if err != nil {
			return err
		}
		digest, err := document.Digest()
		if err != nil {
			return err
		}
		owner.debian.mu.Lock()
		subject := owner.debian.completions[output.ArtifactID].description.Digest
		owner.debian.mu.Unlock()
		index := sbom.Index{SchemaVersion: sbom.IndexSchemaVersion, BuildDigest: output.BuildDigest, Entries: []sbom.IndexEntry{{Artifact: output.Components[0], ArtifactSHA: subject, SBOMDigest: digest}}}
		if err := index.Validate(); err != nil {
			return err
		}
		dir, err := relocatedFactoryPath(artifacts[node.Outputs[0]].Path, owner.debian.workspace)
		if err != nil {
			return err
		}
		raw, err := document.Marshal()
		if err != nil {
			return err
		}
		if err := owner.debian.writeOwnedFile(owned, filepath.Join(dir, output.Components[0]+".cdx.json"), raw, 0444); err != nil {
			return err
		}
		raw, err = canonical.Marshal(index)
		if err != nil {
			return err
		}
		return owner.debian.writeOwnedFile(owned, filepath.Join(dir, "index.json"), raw, 0444)
	case NodeVerify:
		if err := owner.debian.resetNodeOutputs(plan, node); err != nil {
			return err
		}
		receipt, err := owner.debian.observeBoundOutput(owned, owner, plan, output)
		if err != nil {
			return err
		}
		provenance, observation, err := receipt.localProducerProvenance(owned)
		if err != nil {
			return err
		}
		dir, err := relocatedFactoryPath(artifacts["sbom/"+output.Name].Path, owner.debian.workspace)
		if err != nil {
			return err
		}
		if err := owner.debian.writeOwnedFile(owned, filepath.Join(dir, "production-predicate.json"), provenance, 0444); err != nil {
			return err
		}
		raw, err := canonical.Marshal(observation)
		if err != nil {
			return err
		}
		path, err := relocatedFactoryPath(artifacts[node.Outputs[0]].Path, owner.debian.workspace)
		if err != nil {
			return err
		}
		return owner.debian.writeOwnedFile(owned, path, raw, 0444)
	default:
		return e.New(e.CodeEngineCapability, "unsupported native package scheduling operation")
	}
}

// ExportDebianLifecyclePayload requires the original live Run's terminal receipt.
// Descriptive paths and serialized results cannot issue or replace it.
func (result *LocalRunResult) ExportDebianLifecyclePayload(ctx context.Context, plan *Plan, output LifecycleOutputPlan, destination io.Writer) (DebianOutputObservation, []byte, []byte, error) {
	var zero DebianOutputObservation
	if result == nil || result.debian == nil || destination == nil {
		return zero, nil, nil, e.New(e.CodeProvenanceMissing, "native package export requires original Run and destination")
	}
	state := result.debian
	owned, finish, err := state.begin(ctx, result, plan, output)
	if err != nil {
		return zero, nil, nil, err
	}
	defer finish()
	state.mu.Lock()
	issued := state.receipts[output.ArtifactID]
	content := state.content[output.ArtifactID]
	state.mu.Unlock()
	if issued == nil {
		return zero, nil, nil, e.New(e.CodeProvenanceMissing, "native package export has no terminal receipt")
	}
	receipt := DebianOutputReceipt{state: issued}
	predicate, observation, err := receipt.localProducerProvenance(owned)
	if err != nil {
		return zero, nil, nil, err
	}
	exported, err := receipt.WriteToContext(owned, destination)
	if err != nil {
		return zero, nil, nil, err
	}
	if exported != observation {
		return zero, nil, nil, e.New(e.CodeVerifyFailed, "native package export changes terminal observation")
	}
	return exported, append([]byte(nil), content.bytes...), predicate, nil
}

// Snapshot native plans before any admission can block. Canonical round-tripping
// uses the existing codec; no caller alias survives into native scheduling.
func freezeLocalDebianPlan(plan *Plan) (*Plan, error) {
	if !hasLocalDebianBuild(plan) {
		return plan, nil
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "freezing native package Plan")
	}
	var frozen Plan
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "decoding frozen native package Plan")
	}
	return &frozen, nil
}
func requireLocalDebianRuntimeOption(plan *Plan, options LocalRunOptions) error {
	if hasLocalDebianBuild(plan) && filepath.Base(options.Runtime) != plan.Environment.RuntimeBinary() {
		return e.New(e.CodeEngineCapability, "native package runtime differs from frozen Environment")
	}
	return nil
}

// Use established confined roots and descriptor-relative publication, rather
// than pathname helpers, for native scheduler metadata inside a reused workspace.
func (state *debianRunState) openDirectory() (returnErr error) {
	held, _, err := securepath.OpenDirectory(state.workspace)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "holding native package workspace")
	}
	defer func() {
		if err := held.Close(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeImmutableViolation, err, "closing native workspace admission"))
		}
	}()
	root, err := os.OpenRoot(state.workspace)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "confining native package workspace")
	}
	a, aErr := held.Stat()
	b, bErr := root.Stat(".")
	if aErr != nil || bErr != nil || !os.SameFile(a, b) {
		return e.Wrap(e.CodeImmutableViolation, errors.Join(aErr, bErr, root.Close()), "native package workspace changed during confinement")
	}
	state.directory = root
	return nil
}
func (state *debianRunState) resetNodeOutputs(plan *Plan, node Node) error {
	if state.directory == nil {
		return e.New(e.CodeEngineCapability, "native package workspace is not held")
	}
	_, artifacts := planIndex(plan)
	for _, id := range node.Outputs {
		path, err := relocatedFactoryPath(artifacts[id].Path, state.workspace)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(state.workspace, path)
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "locating native package output")
		}
		if err := state.directory.RemoveAll(relative); err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "resetting confined native package output")
		}
	}
	return nil
}
func (state *debianRunState) writeOwnedFile(ctx context.Context, path string, data []byte, mode os.FileMode) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native package metadata canceled")
	}
	if state.directory == nil {
		return e.New(e.CodeEngineCapability, "native package metadata requires held workspace")
	}
	relative, err := filepath.Rel(state.workspace, path)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "locating native package metadata")
	}
	root, err := state.directory.Open(".")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening held native package directory")
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeImmutableViolation, err, "closing native workspace root"))
		}
	}()
	parent, name, err := securepath.CreateParent(root, relative)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "holding native package metadata parent")
	}
	defer func() {
		if err := parent.Close(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeImmutableViolation, err, "closing native metadata parent"))
		}
	}()
	file, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "staging native package metadata")
	}
	defer func() {
		if temporary != "" {
			if err := unix.Unlinkat(int(parent.Fd()), temporary, 0); err != nil {
				returnErr = errors.Join(returnErr, e.Wrap(e.CodeImmutableViolation, err, "cleaning native package metadata stage"))
			}
		}
	}()
	count, writeErr := file.Write(data)
	if writeErr == nil && count != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Chmod(mode)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	writeErr = errors.Join(writeErr, file.Close())
	if writeErr != nil {
		return e.Wrap(e.CodeImmutableViolation, writeErr, "writing native package metadata")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native package metadata commit canceled")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), name); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "publishing native package metadata")
	}
	temporary = ""
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting native package metadata")
	}
	return nil
}
