// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/rootfs"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeuse"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

// ProducedRuntime holds exact filesystem bytes from a concrete mkosi producer.
// It is production authority only. The Run owner must separately join its Plan,
// attempt, evidence and consumer purpose before admitting runtime execution.
// There is no constructor from a pathname, manifest, result file or digest.
type ProducedRuntime struct{ state *producedRuntimeState }

type producedRuntimeState struct {
	mu       sync.Mutex
	layer    *rootfs.Layer
	binding  ProducedRuntimeBinding
	result   ArtifactResult
	root     string
	rootInfo os.FileInfo
	recipe   *mkosi.Spec
}

// ProducedRuntimeBinding describes the live production capability; this value
// cannot restore it or authorize a different Run or consumer.
type ProducedRuntimeBinding = runtimeuse.ProductionBinding

func ValidateProducedRuntimeProducer(graph *ir.Graph, buildDigest, component string) error {
	if err := validateProducedExecutableGraph(graph, buildDigest); err != nil {
		return err
	}
	_, err := selectProducedRuntimeProducer(graph, component)
	return err
}

func selectProducedRuntimeProducer(graph *ir.Graph, component string) (*ir.Node, error) {
	var selected *ir.Node
	for index := range graph.Nodes {
		node := &graph.Nodes[index]
		if node.Component != component || (node.Kind != ir.KindBuild && node.Kind != ir.KindPull) {
			continue
		}
		if selected != nil {
			return nil, e.New(e.CodeVerifyFailed, "runtime component %q has ambiguous producers", component)
		}
		selected = node
	}
	if selected == nil || selected.Kind != ir.KindBuild || selected.BuilderType != ir.BuilderMkosi || selected.OutputType != "oci-image" {
		return nil, e.New(e.CodeEngineCapability, "internal runtime requires its selected mkosi OCI producer")
	}
	return selected, nil
}

// ExecuteProducedRuntime can issue production authority only after the concrete
// executor has succeeded. Native assembly admission and source authentication
// remain owned by ExecNode; restored descriptions cannot call the capture path.
func (local Local) ExecuteProducedRuntime(graph *ir.Graph, buildDigest, component string, options RunOptions) (*ArtifactResult, *ProducedRuntime, error) {
	if err := requireRunContext(options.Ctx, "produced runtime execution"); err != nil {
		return nil, nil, err
	}
	if err := options.Ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "produced runtime execution canceled")
	}
	if err := ValidateProducedRuntimeProducer(graph, buildDigest, component); err != nil {
		return nil, nil, err
	}
	if runtime.GOOS != "linux" || graph.Platform != "linux/"+runtime.GOARCH || !options.Launch || options.DryRun || options.Package {
		return nil, nil, e.New(e.CodeEngineCapability, "runtime production requires launched native Linux assembly")
	}
	producer, err := selectProducedRuntimeProducer(graph, component)
	if err != nil {
		return nil, nil, err
	}
	result, err := local.ExecNode(*producer, options)
	if err != nil {
		return nil, nil, err
	}
	if result == nil {
		return nil, nil, e.New(e.CodeVerifyFailed, "runtime producer returned no result")
	}
	tool, err := captureExecutedRuntime(options.Ctx, graph.Platform, buildDigest, *producer, *result, withDefaults(options))
	if err != nil {
		return nil, nil, err
	}
	return result, tool, nil
}

func captureExecutedRuntime(ctx context.Context, platform, buildDigest string, producer ir.Node, observed ArtifactResult, options RunOptions) (*ProducedRuntime, error) {
	result, err := authenticateConsumedResultMetadata(producer, observed)
	if err != nil {
		return nil, err
	}
	root := artifactHostPath(options, producer)
	manifest := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(result.SHA256, "sha256:"))
	if producer.BuilderType != ir.BuilderMkosi || result.Component != producer.Component || result.OutputType != "oci-image" ||
		result.BuilderType != ir.BuilderMkosi || result.EngineClass != ir.BuilderMkosi || result.Toolchain != "" || result.Ref != "" || result.Packaged || result.AICRValidation != nil ||
		result.Path != producer.ArtifactPath || result.TransportPath != root || result.AbsPath != manifest || !canonical.IsDigest(result.SHA256) || result.Size <= 0 {
		return nil, e.New(e.CodeVerifyFailed, "runtime result differs from its concrete mkosi producer")
	}
	directory, info, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding produced runtime directory")
	}
	if err := directory.Close(); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "closing runtime directory inspection")
	}
	tree, err := ociclosure.Snapshot(ctx, root)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "capturing produced runtime layout")
	}
	size, err := tree.Size(ctx)
	if err != nil {
		return nil, err
	}
	if size != result.Size {
		return nil, e.New(e.CodeVerifyFailed, "runtime layout size differs from its producer result")
	}
	layer, err := rootfs.CaptureLayer(ctx, root, result.SHA256, platform)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = layer.Close()
		}
	}()
	raw, err := canonical.Marshal(result)
	if err != nil {
		return nil, err
	}
	var retained ArtifactResult
	if err := canonical.UnmarshalExact(raw, &retained); err != nil {
		return nil, err
	}
	retained.AbsPath = manifest
	terminal, err := canonical.Digest(canonical.KindTerminalVerification, struct {
		BuildDigest  string                           `json:"buildDigest"`
		Nodes        []producedExecutableNodeView     `json:"nodes"`
		Artifacts    []producedExecutableArtifactView `json:"artifacts"`
		LayoutDigest string                           `json:"layoutDigest"`
	}{buildDigest, []producedExecutableNodeView{{ID: producer.ID, Kind: producer.Kind, Status: "ok"}},
		[]producedExecutableArtifactView{{NodeID: producer.ID, Result: retained, LiveRoot: root, LivePath: manifest}}, tree.Digest()})
	if err != nil {
		return nil, err
	}
	state := &producedRuntimeState{layer: layer, root: root, rootInfo: info, result: retained, recipe: mkosi.Clone(producer.Mkosi),
		binding: ProducedRuntimeBinding{BuildDigest: buildDigest, Platform: platform, Component: producer.Component, Digest: result.SHA256, Size: size,
			LayoutDigest: tree.Digest(), LayerDigest: layer.Digest(), TerminalDigest: terminal}}
	if err := state.require(ctx); err != nil {
		return nil, err
	}
	accepted = true
	return &ProducedRuntime{state: state}, nil
}

// require runs with the state's lock held (or before its first publication).
func (state *producedRuntimeState) require(ctx context.Context) error {
	if state.layer == nil || state.rootInfo == nil || state.binding.TerminalDigest == "" {
		return e.New(e.CodeGateUnmet, "runtime production receipt is absent or closed")
	}
	if err := state.layer.AssertUnchangedContext(ctx); err != nil {
		return mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "revalidating produced runtime layer"), "runtime layer revalidation")
	}
	directory, info, err := securepath.OpenDirectory(state.root)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reopening original runtime layout")
	}
	if err := directory.Close(); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "closing runtime directory inspection")
	}
	if !os.SameFile(state.rootInfo, info) {
		return e.New(e.CodeVerifyFailed, "produced runtime directory was replaced")
	}
	tree, err := ociclosure.Snapshot(ctx, state.root)
	if err != nil {
		return mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "revalidating produced runtime layout"), "runtime layout revalidation")
	}
	if tree.Digest() != state.binding.LayoutDigest {
		return e.New(e.CodeVerifyFailed, "produced runtime layout changed after capture")
	}
	return nil
}

func (tool *ProducedRuntime) lock(ctx context.Context) (*producedRuntimeState, error) {
	if err := requireRunContext(ctx, "produced runtime binding"); err != nil {
		return nil, err
	}
	if tool == nil || tool.state == nil {
		return nil, e.New(e.CodeGateUnmet, "runtime production receipt is absent")
	}
	if err := lockDiskInspection(ctx, &tool.state.mu); err != nil {
		return nil, err
	}
	if err := tool.state.require(ctx); err != nil {
		tool.state.mu.Unlock()
		return nil, err
	}
	return tool.state, nil
}

func (tool *ProducedRuntime) BindingContext(ctx context.Context) (ProducedRuntimeBinding, error) {
	state, err := tool.lock(ctx)
	if err != nil {
		return ProducedRuntimeBinding{}, err
	}
	defer state.mu.Unlock()
	return state.binding, nil
}

func (tool *ProducedRuntime) AuthenticateResultContext(ctx context.Context, result ArtifactResult) error {
	state, err := tool.lock(ctx)
	if err != nil {
		return err
	}
	defer state.mu.Unlock()
	want, err := canonical.Marshal(state.result)
	if err != nil {
		return err
	}
	got, err := canonical.Marshal(result)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return e.New(e.CodeVerifyFailed, "runtime result differs from original production")
	}
	return nil
}

// Close revokes every copy and waits for any current capability borrower.
func (tool *ProducedRuntime) Close() error {
	if tool == nil || tool.state == nil {
		return nil
	}
	state := tool.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.layer == nil {
		return nil
	}
	err := state.layer.Close()
	state.layer = nil
	return err
}
