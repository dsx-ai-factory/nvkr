// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const terminalReceiptDomain = canonical.KindTerminalVerification

// terminalVerificationReceipt is deliberately package-private and has no
// constructor accepting declarations. It is minted only by issueTerminalReceipt
// after the terminal verifier has re-opened the selected graph's real artifacts.
type terminalVerificationReceipt struct {
	valid       bool
	binding     string
	producerIDs []string
	artifacts   []terminalArtifact
}

type terminalArtifact struct {
	node       ir.Node
	result     ArtifactResult
	sourceRoot string
	root       string
	relative   string
	fileInfo   os.FileInfo
	tree       *terminalHeldTree
}

type terminalArtifactView struct {
	NodeID     string         `json:"nodeId"`
	Result     ArtifactResult `json:"result"`
	LiveRoot   string         `json:"liveRoot,omitempty"`
	LivePath   string         `json:"livePath,omitempty"`
	TreeDigest string         `json:"treeDigest,omitempty"`
}

type terminalReceiptView struct {
	BuildDigest string                 `json:"buildDigest"`
	Nodes       []NodeResult           `json:"nodes"`
	Artifacts   []terminalArtifactView `json:"artifacts"`
}

func issueTerminalReceipt(
	ctx context.Context,
	report *Report,
	producers map[string]ir.Node,
	included map[string]bool,
	opts RunOptions,
) (terminalVerificationReceipt, error) {
	var zero terminalVerificationReceipt
	if ctx == nil {
		return zero, e.New(e.CodeUsage, "terminal verification requires a non-nil context")
	}
	if report == nil {
		return zero, e.New(e.CodeVerifyFailed, "terminal verification received no report")
	}
	byComponent := make(map[string]ArtifactResult, len(report.Artifacts))
	for _, artifact := range report.Artifacts {
		if artifact.Component == "" {
			return zero, e.New(e.CodeVerifyFailed, "terminal result contains an empty component")
		}
		if _, duplicate := byComponent[artifact.Component]; duplicate {
			return zero, e.New(e.CodeVerifyFailed, "terminal result contains duplicate component %q", artifact.Component)
		}
		byComponent[artifact.Component] = artifact
	}
	producerIDs := make([]string, 0, len(producers))
	for id := range producers {
		if included[id] {
			producerIDs = append(producerIDs, id)
		}
	}
	sort.Strings(producerIDs)
	if len(byComponent) != len(producerIDs) {
		return zero, e.New(e.CodeVerifyFailed,
			"terminal result set has %d artifacts, want exact selected producer set of %d", len(byComponent), len(producerIDs))
	}

	receipt := terminalVerificationReceipt{producerIDs: append([]string(nil), producerIDs...)}
	for _, id := range producerIDs {
		if err := ctx.Err(); err != nil {
			return zero, e.Wrap(e.CodeTimeout, err, "terminal artifact verification canceled")
		}
		node := producers[id]
		artifact, ok := byComponent[node.Component]
		if !ok {
			return zero, e.New(e.CodeVerifyFailed, "producer %q produced no terminal artifact", id)
		}
		if err := validateTerminalDeclaration(node, artifact, opts); err != nil {
			return zero, err
		}
		verified, err := captureTerminalArtifact(ctx, node, artifact, opts)
		if err != nil {
			if ctx.Err() != nil {
				return zero, mapCancellation(ctx, err, "terminal artifact verification")
			}
			return zero, e.Wrap(e.CodeVerifyFailed, err, "terminally verifying %s", node.Component)
		}
		if err := ctx.Err(); err != nil {
			return zero, e.Wrap(e.CodeTimeout, err, "terminal artifact verification canceled")
		}
		receipt.artifacts = append(receipt.artifacts, verified)
	}
	binding, err := terminalReceiptBinding(report, receipt.artifacts)
	if err != nil {
		return zero, err
	}
	receipt.binding = binding
	receipt.valid = true
	return receipt, nil
}

func validateTerminalDeclaration(node ir.Node, artifact ArtifactResult, opts RunOptions) error {
	if artifact.Component != node.Component || artifact.OutputType != node.OutputType {
		return e.New(e.CodeVerifyFailed, "artifact %q does not match producer %q", artifact.Component, node.ID)
	}
	switch node.Kind {
	case ir.KindBuild:
		if artifact.Toolchain != node.Toolchain || artifact.BuilderType != node.BuilderType || artifact.InputDigest == "" {
			return e.New(e.CodeVerifyFailed, "build artifact %q does not match its pinned build declaration", node.Component)
		}
		if len(node.Materials) != 0 && !canonical.IsDigest(artifact.MaterialManifestDigest) {
			return e.New(e.CodeVerifyFailed,
				"build artifact %q lacks its exact material manifest digest", node.Component)
		}
		if len(node.Materials) == 0 && artifact.MaterialManifestDigest != "" {
			return e.New(e.CodeVerifyFailed,
				"build artifact %q records undeclared material inputs", node.Component)
		}
		if !artifact.Packaged && artifact.Path != node.ArtifactPath {
			return e.New(e.CodeVerifyFailed, "build artifact %q path does not match its graph declaration", node.Component)
		}
		if opts.execution != nil {
			identity, ok := observedNodeIdentity(opts.execution, node.ID)
			if !ok || artifact.Qualification != ArtifactQualifiedExecution || artifact.EngineClass != identity.EngineClass ||
				artifact.ProfileDigest != opts.execution.receipt.ProfileDigest() ||
				artifact.ExecutionBinding != opts.execution.receipt.ExecutionBinding() {
				return e.New(e.CodeVerifyFailed, "build artifact %q lacks the exact selected execution identity", node.Component)
			}
		}
	case ir.KindPull:
		if artifact.Qualification != ArtifactProfileFreeAcquisition || artifact.BuilderType != "" ||
			artifact.ProfileDigest != "" || artifact.ExecutionBinding != "" ||
			artifact.EngineClass != ir.EnginePrebuiltAcquire || artifact.SHA256 != node.Digest {
			return e.New(e.CodeVerifyFailed, "prebuilt artifact %q is not the exact graph-selected acquisition", node.Component)
		}
		if node.OutputType == "oci-image" {
			wantRef, err := ir.NativeImageReference(node)
			if err != nil {
				return err
			}
			if artifact.Ref != wantRef {
				return e.New(e.CodeVerifyFailed, "prebuilt artifact %q does not retain its digest-pinned reference", node.Component)
			}
		}
	default:
		return e.New(e.CodeInternal, "node %q is not an artifact producer", node.ID)
	}
	return nil
}

func captureTerminalArtifact(ctx context.Context, node ir.Node, artifact ArtifactResult, opts RunOptions) (terminalArtifact, error) {
	captured := terminalArtifact{node: node, result: artifact}
	switch {
	case artifact.Ref != "":
		captured.sourceRoot = artifact.TransportPath
		snapshot, err := snapshotRegularTreeContext(ctx, captured.sourceRoot)
		if err != nil {
			return terminalArtifact{}, err
		}
		captured.tree = snapshot
		_, want, err := parseDigestReference(artifact.Ref)
		if err != nil {
			return terminalArtifact{}, err
		}
		if err := verifyOCILayoutSource(ctx, snapshot, want); err != nil {
			return terminalArtifact{}, err
		}
		return captured, nil
	case artifact.Packaged && artifact.OutputType == "oci-image":
		captured.sourceRoot = artifact.Path
		snapshot, err := snapshotRegularTreeContext(ctx, captured.sourceRoot)
		if err != nil {
			return terminalArtifact{}, err
		}
		captured.tree = snapshot
		if err := verifyOCILayoutSource(ctx, snapshot, artifact.SHA256); err != nil {
			return terminalArtifact{}, err
		}
		return captured, nil
	}

	root, expectedPath, err := trustedArtifactPath(node, artifact, opts)
	if err != nil {
		return terminalArtifact{}, err
	}
	if artifact.AbsPath != expectedPath {
		return terminalArtifact{}, fmt.Errorf("artifact %s live path %q does not match graph-derived path %q", node.Component, artifact.AbsPath, expectedPath)
	}
	relative, err := relativeBeneath(root, expectedPath)
	if err != nil {
		return terminalArtifact{}, err
	}
	digest, size, info, err := hashRegularBeneathContext(ctx, root, relative)
	if err != nil {
		return terminalArtifact{}, err
	}
	if digest != artifact.SHA256 || size != artifact.Size {
		return terminalArtifact{}, fmt.Errorf("artifact %s bytes are %s/%d, want %s/%d", node.Component, digest, size, artifact.SHA256, artifact.Size)
	}
	captured.sourceRoot, captured.root, captured.relative, captured.fileInfo = root, root, relative, info
	return captured, nil
}

func trustedArtifactPath(node ir.Node, artifact ArtifactResult, opts RunOptions) (string, string, error) {
	switch node.Kind {
	case ir.KindBuild:
		if !artifact.Packaged {
			return privOutDir(opts, node), artifactHostPath(opts, node), nil
		}
		root := opts.PackageDir
		if artifact.OutputType == "host-package" {
			want := filepath.Join(root, fmt.Sprintf("%s_%s_%s.deb", node.Component, orVersion(node.Version), debArch(node.Platform)))
			return root, want, nil
		}
		return "", "", fmt.Errorf("unsupported packaged artifact type %q", artifact.OutputType)
	case ir.KindPull:
		root := prebuiltDir(opts, node)
		name := node.ArtifactPath
		if name == "" {
			name = node.Component
		}
		want, err := pathWithin(root, name)
		return root, want, err
	default:
		return "", "", fmt.Errorf("node %q is not an artifact producer", node.ID)
	}
}

func (receipt terminalVerificationReceipt) require(ctx context.Context, report *Report) error {
	if !receipt.valid || receipt.binding == "" {
		return e.New(e.CodePreflightFailed, "report lacks an opaque terminal verification receipt")
	}
	binding, err := terminalReceiptBinding(report, receipt.artifacts)
	if err != nil {
		return err
	}
	if binding != receipt.binding {
		return e.New(e.CodeVerifyFailed, "report changed after terminal verification")
	}
	for _, artifact := range receipt.artifacts {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "qualified artifact revalidation canceled")
		}
		if artifact.tree != nil {
			if artifact.result.Ref != "" {
				_, want, err := parseDigestReference(artifact.result.Ref)
				if err != nil {
					return err
				}
				if err := verifyOCILayoutSource(ctx, artifact.tree, want); err != nil {
					if ctx.Err() != nil {
						return mapCancellation(ctx, err, "qualified artifact revalidation")
					}
					return e.Wrap(e.CodeVerifyFailed, err, "revalidating OCI artifact %s", artifact.node.Component)
				}
			} else if err := verifyOCILayoutSource(ctx, artifact.tree, artifact.result.SHA256); err != nil {
				if ctx.Err() != nil {
					return mapCancellation(ctx, err, "qualified artifact revalidation")
				}
				return e.Wrap(e.CodeVerifyFailed, err, "revalidating OCI artifact %s", artifact.node.Component)
			}
			continue
		}
		digest, size, info, err := hashRegularBeneathContext(ctx, artifact.root, artifact.relative)
		if err != nil || !os.SameFile(artifact.fileInfo, info) || digest != artifact.result.SHA256 || size != artifact.result.Size {
			if ctx.Err() != nil {
				return e.Wrap(e.CodeTimeout, ctx.Err(), "qualified artifact revalidation canceled")
			}
			return e.New(e.CodeVerifyFailed, "artifact %q changed after terminal verification", artifact.node.Component)
		}
	}
	return nil
}

func terminalReceiptBinding(report *Report, captured []terminalArtifact) (string, error) {
	byComponent := make(map[string]ArtifactResult, len(report.Artifacts))
	for _, artifact := range report.Artifacts {
		if _, exists := byComponent[artifact.Component]; exists {
			return "", e.New(e.CodeVerifyFailed, "report contains duplicate component %q", artifact.Component)
		}
		byComponent[artifact.Component] = artifact
	}
	views := make([]terminalArtifactView, 0, len(captured))
	for _, artifact := range captured {
		current, ok := byComponent[artifact.node.Component]
		if !ok {
			return "", e.New(e.CodeVerifyFailed, "report omits terminal artifact %q", artifact.node.Component)
		}
		delete(byComponent, artifact.node.Component)
		treeDigest := ""
		if artifact.tree != nil {
			treeDigest = artifact.tree.digest
		}
		views = append(views, terminalArtifactView{
			NodeID: artifact.node.ID, Result: current,
			LiveRoot: artifact.sourceRoot, LivePath: current.AbsPath, TreeDigest: treeDigest,
		})
	}
	if len(byComponent) != 0 {
		return "", e.New(e.CodeVerifyFailed, "report contains artifacts outside the terminal producer selection")
	}
	return canonical.Digest(terminalReceiptDomain, terminalReceiptView{
		BuildDigest: report.BuildDigest,
		Nodes:       append([]NodeResult(nil), report.Nodes...), Artifacts: views,
	})
}

type terminalHeldTree struct {
	snapshot *ociclosure.Tree
	digest   string
}

func snapshotRegularTreeContext(ctx context.Context, root string) (*terminalHeldTree, error) {
	tree, err := ociclosure.Snapshot(ctx, root)
	if err != nil {
		return nil, err
	}
	return &terminalHeldTree{snapshot: tree, digest: tree.Digest()}, nil
}
func (tree *terminalHeldTree) readRegular(ctx context.Context, path string, limit int64) ([]byte, error) {
	if tree == nil {
		return nil, fmt.Errorf("held OCI tree is nil")
	}
	return tree.snapshot.ReadRegular(ctx, path, limit)
}
func (tree *terminalHeldTree) blobInventory(ctx context.Context) (map[string]int64, error) {
	if tree == nil {
		return nil, fmt.Errorf("held OCI tree is nil")
	}
	return tree.snapshot.BlobInventory(ctx)
}
func captureRegularTreeContext(ctx context.Context, root string) (ociclosure.TreeState, error) {
	return ociclosure.CaptureTreeState(ctx, root)
}
func readDirectoryEntriesBounded(directory *os.File, limit int) ([]os.DirEntry, error) {
	return ociclosure.ReadDirectoryEntriesBounded(directory, limit)
}
