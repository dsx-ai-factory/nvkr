// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"golang.org/x/sys/unix"
)

// PackageReconciledDisk consumes the original live execution and its concrete
// same-output evidence receipt. The graph's raw lifecycle subject is retained;
// output is its distinct compiler-issued OCI package artifact. The returned BOM
// is the exact root inventory, not a waiver of other component evidence slots.
// This operation constructs transport bytes; signing/publication remain owned
// by the normal factory routes and configured Environment.
func (owner *LocalRunResult) PackageReconciledDisk(ctx context.Context, plan *Plan, expected LifecycleOutputPlan,
	runtime *sbom.DiskInspectionRuntime, receipt *DiskEvidenceReceipt, output Artifact,
) (result delivery.Result, document *sbom.BOM, returnErr error) {
	if ctx == nil {
		return delivery.Result{}, nil, e.New(e.CodeUsage, "disk packaging requires context")
	}
	if err := ctx.Err(); err != nil {
		return delivery.Result{}, nil, e.Wrap(e.CodeTimeout, err, "disk packaging canceled")
	}
	if owner == nil || plan == nil || runtime == nil || receipt == nil {
		return delivery.Result{}, nil, e.New(e.CodeProvenanceMissing, "disk packaging requires original live execution and reconciled evidence")
	}
	if err := plan.Validate(); err != nil {
		return delivery.Result{}, nil, err
	}
	platform, profile, err := reconciledDiskPackageBinding(plan, expected, output)
	if err != nil {
		return delivery.Result{}, nil, err
	}
	digest, size := receipt.Subject()
	// The getter sizes resource admission only. ExportReconciledDisk authenticates
	// this same subject, original owner, runtime and complete document before it
	// can invoke the staging writer below; a description cannot replace it.
	if !canonical.IsDigest(digest) || size <= 0 || size > 64<<30 {
		return delivery.Result{}, nil, e.New(e.CodeVerifyFailed, "disk evidence lacks a bounded exact subject")
	}
	workspace := plan.workspace
	if workspace == "" {
		workspace = filepath.Join(".nvkr", "factory")
	}
	capture := &reconciledDiskCapture{ctx: ctx, parent: filepath.Dir(output.Path), workspace: workspace, size: size,
		storageMiB:    profile.Resources.EphemeralStorageMiB,
		metadataBytes: int64(len(expected.Components)+4) * sbom.MaxDocumentBytes}
	defer func() {
		if err := capture.close(); err != nil {
			result, document = delivery.Result{}, nil
			returnErr = joinFactoryCleanupError(returnErr, err, "closing private reconciled disk carrier")
		}
	}()
	observation, document, err := owner.ExportReconciledDisk(ctx, plan, expected, runtime, receipt, capture)
	if err != nil {
		return delivery.Result{}, nil, err
	}
	if observation.Digest != digest || observation.Size != size || observation.Format != "qcow2" || observation.Encryption != "none" ||
		capture.written != size || document == nil {
		return delivery.Result{}, nil, e.New(e.CodeVerifyFailed, "reconciled export differs from exact unencrypted disk subject")
	}
	if err := document.Validate(); err != nil {
		return delivery.Result{}, nil, err
	}
	componentDocuments, err := receipt.ComponentDocuments()
	if err != nil {
		return delivery.Result{}, nil, err
	}
	evidence, err := prepareReconciledEvidence(ctx, plan, expected, document, componentDocuments)
	if err != nil {
		return delivery.Result{}, nil, err
	}
	if err := capture.finish(); err != nil {
		return delivery.Result{}, nil, err
	}
	result, err = publishReconciledDiskPackage(ctx, workspace, output.Path, evidence, capture.close,
		func(ctx context.Context, path string) (delivery.Result, error) {
			return delivery.BuildDiskArtifact(ctx, path, delivery.DiskArtifactRequest{Path: capture.path,
				Digest: digest, Size: size, Platform: platform, Format: "qcow2", Encryption: "none", ArtifactType: output.MediaType})
		})
	if err != nil {
		return delivery.Result{}, nil, err
	}
	return result, document, nil
}

// Both roots are private until construction and carrier cleanup succeed. This
// uses the existing descriptor-relative atomic no-replace transport primitive;
// a failed second publication moves only our first root back for cleanup. A
// process crash can expose unauthenticated bytes, never a completed node receipt.
func publishReconciledDiskPackage(ctx context.Context, workspace, output string, evidence reconciledEvidenceFiles,
	closeCarrier func() error, build func(context.Context, string) (delivery.Result, error),
) (result delivery.Result, returnErr error) {
	if ctx == nil || closeCarrier == nil || build == nil {
		return delivery.Result{}, e.New(e.CodeUsage, "disk package transaction requires context and private construction")
	}
	if err := ctx.Err(); err != nil {
		return delivery.Result{}, e.Wrap(e.CodeTimeout, err, "disk package transaction canceled")
	}
	root, _, err := securepath.OpenDirectory(workspace)
	if err != nil {
		return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "holding disk package transaction workspace")
	}
	defer func() { _ = root.Close() }()
	type target struct {
		parent       *os.File
		name, staged string
		info         os.FileInfo
		published    bool
	}
	targets := []target{{staged: "package"}, {staged: "evidence"}}
	relative := make([]string, 2)
	for index, path := range []string{output, evidence.directory} {
		relative[index], err = filepath.Rel(workspace, path)
		if err != nil || !securepath.WorkspaceRelative(relative[index]) {
			return delivery.Result{}, e.New(e.CodePipelineDrift, "disk package destination escapes its workspace")
		}
	}
	if relative[0] == relative[1] || strings.HasPrefix(relative[0], relative[1]+string(filepath.Separator)) ||
		strings.HasPrefix(relative[1], relative[0]+string(filepath.Separator)) {
		return delivery.Result{}, e.New(e.CodePipelineDrift, "disk package and evidence destinations overlap")
	}
	defer func() {
		for _, target := range targets {
			if target.parent != nil {
				_ = target.parent.Close() // cannot revoke durable commit or rollback
			}
		}
	}()
	for index := range targets {
		targets[index].parent, targets[index].name, err = securepath.CreateParent(root, relative[index])
		if err != nil {
			return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "holding disk package destination")
		}
		var observed unix.Stat_t
		err = unix.Fstatat(int(targets[index].parent.Fd()), targets[index].name, &observed, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil || !errors.Is(err, unix.ENOENT) {
			return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "disk package destination already exists or cannot be admitted")
		}
	}
	reserved, name, err := securepath.CreateTemporarySibling(root)
	if err != nil {
		return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "reserving private disk package stage")
	}
	if err := errors.Join(reserved.Close(), unix.Unlinkat(int(root.Fd()), name, 0)); err != nil {
		return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "preparing private disk package stage")
	}
	if err := unix.Mkdirat(int(root.Fd()), name, 0o700); err != nil {
		return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "creating private disk package stage")
	}
	stagePath := filepath.Join(workspace, name)
	stage, stageInfo, err := securepath.OpenChild(root, name, true)
	if err != nil {
		cleanupErr := unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR)
		return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, errors.Join(err, cleanupErr), "holding private disk package stage")
	}
	committed := false
	defer func() {
		if !committed {
			var rollbackErr error
			for index := len(targets) - 1; index >= 0; index-- {
				target := &targets[index]
				if !target.published {
					continue
				}
				rollbackErr = errors.Join(rollbackErr, rollbackDiskPackageRoot(target.parent, target.name, target.info, stage, target.staged))
			}
			if rollbackErr != nil {
				returnErr = joinFactoryCleanupError(returnErr, rollbackErr, "rolling back disk package transaction")
			}
		}
		// Only this invocation's private directory is removed; a substituted
		// pathname is never traversed or mistaken for our cleanup target.
		current, err := os.Lstat(stagePath)
		if err != nil || !os.SameFile(stageInfo, current) {
			if !committed {
				returnErr = joinFactoryCleanupError(returnErr, e.New(e.CodeImmutableViolation, "private disk package stage changed"), "cleaning disk package stage")
			}
		} else if err := os.RemoveAll(stagePath); err != nil && !committed {
			returnErr = joinFactoryCleanupError(returnErr, err, "cleaning disk package stage")
		}
		_ = stage.Close() // no authority remains in this scratch descriptor
	}()
	result, err = build(ctx, filepath.Join(stagePath, "package"))
	if err != nil {
		return delivery.Result{}, err
	}
	if err := evidence.writeTo(ctx, filepath.Join(stagePath, "evidence")); err != nil {
		return delivery.Result{}, err
	}
	if err := closeCarrier(); err != nil {
		return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "closing private disk carrier before publication")
	}
	if err := ctx.Err(); err != nil {
		return delivery.Result{}, e.Wrap(e.CodeTimeout, err, "disk package transaction canceled before publication")
	}
	for index := range targets {
		child, info, err := securepath.OpenChild(stage, targets[index].staged, true)
		if err != nil {
			return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "holding staged disk package root")
		}
		targets[index].info = info
		if err := child.Close(); err != nil {
			return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "closing staged disk package root")
		}
	}
	// Once the short multi-root commit starts, finish or roll it back rather
	// than leaving a half-commit because voluntary cancellation arrived.
	for index := range targets {
		target := &targets[index]
		if err := transportarchive.PublishAtNoReplace(int(stage.Fd()), target.staged, int(target.parent.Fd()), target.name); err != nil {
			return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "publishing disk package transaction without replacement")
		}
		target.published = true
		if err := target.parent.Sync(); err != nil {
			return delivery.Result{}, e.Wrap(e.CodeImmutableViolation, err, "persisting disk package transaction")
		}
	}
	committed = true
	result.LayoutPath = output
	return result, nil
}

// Rollback moves only the directory this transaction published. A missing or
// replaced directory is not ours to move, and the private stage is never replaced.
func rollbackDiskPackageRoot(parent *os.File, name string, expected os.FileInfo, stage *os.File, staged string) error {
	current, info, err := securepath.OpenChild(parent, name, true)
	if err == nil {
		err = current.Close()
	}
	if err != nil || !os.SameFile(expected, info) {
		return e.New(e.CodeImmutableViolation, "published disk package root changed before rollback")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), name, int(stage.Fd()), staged); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "restoring published disk package root without replacement")
	}
	return parent.Sync()
}

func reconciledDiskPackageBinding(plan *Plan, expected LifecycleOutputPlan, output Artifact) (string, definition.DiskInspectionProfile, error) {
	invalid := func() (string, definition.DiskInspectionProfile, error) {
		return "", definition.DiskInspectionProfile{}, e.New(e.CodePipelineDrift, "disk wrapper is not the exact planned output package")
	}
	if expected.Kind != "disk-image" || output.ID != "package/"+expected.Name || output.Kind != "disk-image" ||
		output.Name != expected.Name || output.BuildTarget != "" || output.Path == "" || output.MediaType == "" {
		return invalid()
	}
	matchedOutput, matchedRequirement, linkedProducer := false, false, false
	for _, planned := range plan.Graph.Artifacts {
		if planned.ID == output.ID {
			matchedOutput = planned == output
		}
	}
	for _, planned := range plan.LifecycleOutputs {
		if planned.Name == expected.Name {
			matchedRequirement = planned.Kind == expected.Kind && planned.ArtifactID == expected.ArtifactID &&
				planned.BuildDigest == expected.BuildDigest && slices.Equal(planned.Components, expected.Components)
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.ID == output.Producer {
			linkedProducer = node.Kind == NodePackage && node.PackageKind == "disk-image" &&
				slices.Contains(node.Inputs, expected.ArtifactID) && slices.Contains(node.Outputs, output.ID)
		}
	}
	if !matchedOutput || !matchedRequirement || !linkedProducer {
		return invalid()
	}
	if _, err := plannedDiskPublicationEncryption(plan, expected); err != nil {
		return "", definition.DiskInspectionProfile{}, err
	}
	for _, build := range plan.Builds {
		if build.Name != expected.Name || build.BuildDigest != expected.BuildDigest {
			continue
		}
		if diskBuildProvenance(plan) {
			return build.Platform, definition.DiskInspectionProfile{}, nil
		}
		_, profile, err := diskPublicationInspectionProfile(build, plan.Environment)
		if err != nil {
			return "", definition.DiskInspectionProfile{}, err
		}
		return build.Platform, profile, nil
	}
	return invalid()
}

// reconciledDiskCapture is lazy: B's receipt validation precedes its first write,
// which admits actual workspace capacity before creating any private carrier.
// It adds two disk-backed copies (export plus OCI stage), not two RAM copies.
type reconciledDiskCapture struct {
	ctx           context.Context
	parent        string
	workspace     string
	size          int64
	storageMiB    uint64
	metadataBytes int64
	written       int64
	path          string
	name          string
	file          *os.File
	directory     *os.File
}

func (capture *reconciledDiskCapture) Write(raw []byte) (int, error) {
	if err := capture.ctx.Err(); err != nil {
		return 0, e.Wrap(e.CodeTimeout, err, "reconciled disk export canceled")
	}
	if int64(len(raw)) > capture.size-capture.written {
		return 0, e.New(e.CodeVerifyFailed, "reconciled disk export exceeds exact subject size")
	}
	if capture.file == nil {
		workspace, _, err := securepath.OpenDirectory(capture.workspace)
		if err != nil {
			return 0, e.Wrap(e.CodeEngineCapability, err, "opening disk package workspace")
		}
		facts, err := execution.ObserveLocalHost(capture.ctx, workspace)
		if err != nil {
			_ = workspace.Close()
			return 0, e.Wrap(e.CodeEngineCapability, err, "observing disk package workspace capacity")
		}
		const mib = int64(1 << 20)
		minimum := uint64((2*capture.size + capture.metadataBytes + mib - 1) / mib)
		if facts.Resources.EphemeralStorageMiB < max(minimum, capture.storageMiB) {
			_ = workspace.Close()
			return 0, e.New(e.CodeEngineCapability, "disk package workspace cannot hold export and OCI stage")
		}
		// Only metadata/chunk buffers consume additional RAM here. The envelope
		// export and OCI staging copies are charged to workspace, never RAM.
		if facts.Resources.MemoryMiB < uint64((3*capture.metadataBytes+mib-1)/mib)+1 {
			_ = workspace.Close()
			return 0, e.New(e.CodeEngineCapability, "disk package lacks bounded complete-evidence memory headroom")
		}
		relative, err := filepath.Rel(capture.workspace, capture.parent)
		if err != nil {
			_ = workspace.Close()
			return 0, e.Wrap(e.CodePipelineDrift, err, "locating disk package beneath workspace")
		}
		capture.directory, _, err = securepath.CreateParent(workspace, filepath.Join(relative, ".disk-carrier"))
		_ = workspace.Close()
		if err != nil {
			return 0, e.Wrap(e.CodeImmutableViolation, err, "holding disk package parent beneath admitted workspace")
		}
		capture.file, capture.name, err = securepath.CreateTemporarySibling(capture.directory)
		if err != nil {
			return 0, e.Wrap(e.CodeImmutableViolation, err, "opening private disk package carrier")
		}
		capture.path = filepath.Join(capture.parent, capture.name)
	}
	n, err := capture.file.Write(raw)
	capture.written += int64(n)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return n, e.Wrap(e.CodeVerifyFailed, err, "capturing exact reconciled disk")
	}
	return n, nil
}

func (capture *reconciledDiskCapture) finish() error {
	if capture.file == nil || capture.written != capture.size {
		return e.New(e.CodeVerifyFailed, "reconciled disk export was incomplete")
	}
	err := errors.Join(capture.file.Sync(), capture.file.Close())
	capture.file = nil
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting private reconciled disk")
	}
	return nil
}

func (capture *reconciledDiskCapture) close() error {
	var cleanup []error
	if capture.file != nil {
		cleanup = append(cleanup, capture.file.Close())
		capture.file = nil
	}
	if capture.directory != nil {
		if capture.name != "" {
			err := unix.Unlinkat(int(capture.directory.Fd()), capture.name, 0)
			if !errors.Is(err, unix.ENOENT) {
				cleanup = append(cleanup, err)
			}
		}
		cleanup = append(cleanup, capture.directory.Close())
		capture.directory = nil
	}
	return errors.Join(cleanup...)
}
