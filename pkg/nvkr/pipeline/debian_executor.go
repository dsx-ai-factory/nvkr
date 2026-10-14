// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// DebianProducerExecutor binds exact selected instructions and their separate
// invocation subjects to one original session. The native producer adapter
// supplies unqualified byte retention; public lifecycle execution remains gated
// pending the complete content/final-output evidence join.
// No path, decoded result or matching Build digest can restore this capability.
type DebianProducerExecutor struct{ state *debianProducerExecutorState }

type debianProducerExecutorState struct {
	mu       sync.Mutex
	session  *nodeSessionState
	graph    []byte
	build    string
	nodes    map[string][]byte
	consumed map[string]bool
	bindings map[string]*debianProducerBindingState
	runtime  *heldRuntimeExecutable
}

type debianProducerSelection struct{ subject, nodeID string }

func (session *NodeSession) admitDebianProducers(ctx context.Context, graph *ir.Graph, buildDigest string,
	selections []debianProducerSelection,
) (*DebianProducerExecutor, error) {
	if err := requireRunContext(ctx, "selecting Debian producers"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "selecting Debian producers")
	}
	if session == nil || session.state == nil || graph == nil || len(selections) == 0 {
		return nil, e.New(e.CodeEngineCapability, "Debian producer selection requires an open session and complete graph")
	}
	raw, err := canonical.Marshal(graph)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "freezing Debian producer graph")
	}
	var frozen ir.Graph
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "copying Debian producer graph")
	}
	if err := frozen.Validate(); err != nil {
		return nil, err
	}
	digest, err := frozen.Digest()
	if err != nil {
		return nil, err
	}
	if digest != buildDigest {
		return nil, e.New(e.CodeVerifyFailed, "Debian selection changes its complete Build graph")
	}
	builds := make(map[string]ir.Node)
	for _, node := range frozen.Nodes {
		if node.Requirements != nil || node.ExecutionBinding != nil {
			return nil, e.New(e.CodeEngineCapability, "Debian selection cannot ignore execution requirements")
		}
		switch node.Kind {
		case ir.KindBuild:
			if err := validateDebianArchiveProducer(node); err != nil {
				return nil, err
			}
			builds[node.ID] = node
		case ir.KindFetch, ir.KindVerify:
		default:
			return nil, e.New(e.CodeEngineCapability, "Debian selection contains an unsupported producer")
		}
	}
	if len(builds) != 1 {
		return nil, e.New(e.CodeEngineCapability, "standalone Debian selection requires one explicit source producer")
	}
	nodes := make(map[string][]byte, len(selections))
	previous := ""
	for _, selection := range selections {
		if !safetext.Valid(selection.subject, 4096, false) || selection.subject <= previous || strings.TrimSpace(selection.subject) != selection.subject {
			return nil, e.New(e.CodeInvalidCatalog, "Debian producer subjects must be safe, sorted and unique")
		}
		node, ok := builds[selection.nodeID]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "Debian subject selects an absent source producer")
		}
		nodes[selection.subject], err = canonical.Marshal(node)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "freezing selected Debian instruction")
		}
		previous = selection.subject
	}
	session.state.mu.Lock()
	defer session.state.mu.Unlock()
	if session.state.closed || session.state.ctx.Err() != nil {
		return nil, e.New(e.CodeEngineCapability, "Debian producer session is closed or canceled")
	}
	return &DebianProducerExecutor{state: &debianProducerExecutorState{session: session.state, graph: raw, build: digest, nodes: nodes, consumed: make(map[string]bool)}}, nil
}

func (executor *DebianProducerExecutor) matchesInstruction(subject string, node ir.Node) error {
	if executor == nil || executor.state == nil || executor.state.session == nil {
		return e.New(e.CodeProvenanceMissing, "original Debian executor is required")
	}
	executor.state.mu.Lock()
	want, ok := executor.state.nodes[subject]
	executor.state.mu.Unlock()
	if !ok {
		return e.New(e.CodeVerifyFailed, "Debian producer subject was not originally selected")
	}
	raw, err := canonical.Marshal(node)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "encoding original Debian instruction")
	}
	if !bytes.Equal(raw, want) {
		return e.New(e.CodeVerifyFailed, "Debian producer instruction changed after selection")
	}
	return nil
}

// run selects the frozen instruction internally. RunBoundContext narrows entry
// to an original exclusive binding and admitted native runtime; nil-runtime
// fixtures remain private and supply no runtime or final-output authority.
func (executor *DebianProducerExecutor) run(ctx context.Context, subject string, options RunOptions) (*DebianProducerArchive, *ArtifactResult, error) {
	if err := requireRunContext(ctx, "selected Debian producer"); err != nil {
		return nil, nil, err
	}
	if executor == nil || executor.state == nil || executor.state.session == nil {
		return nil, nil, e.New(e.CodeEngineCapability, "original Debian executor is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "invoking selected Debian producer")
	}
	owned, finish, err := executor.begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer finish()
	ctx = owned
	executor.state.mu.Lock()
	raw, ok := executor.state.nodes[subject]
	if !ok || executor.state.consumed[subject] || executor.state.bindings[subject] != options.debianBinding ||
		(options.debianBinding != nil && (options.debianBinding.executor != executor.state || options.debianBinding.subject != subject)) {
		executor.state.mu.Unlock()
		return nil, nil, e.New(e.CodeVerifyFailed, "Debian subject is absent or already invoked; retry requires a new attempt")
	}
	executor.state.consumed[subject] = true
	executor.state.mu.Unlock()
	var node ir.Node
	if err := canonical.UnmarshalExact(raw, &node); err != nil {
		return nil, nil, e.Wrap(e.CodeInvalidCatalog, err, "encoding original Debian instruction")
	}
	executor.state.mu.Lock()
	options.debianRuntime = executor.state.runtime
	executor.state.mu.Unlock()
	if err := revalidateDebianRuntime(ctx, options.debianRuntime, node); err != nil {
		return nil, nil, err
	}
	options.Ctx, options.debianExecutor, options.debianSubject = ctx, executor, subject
	// The original state is frozen at admission; replacing a caller's facade
	// cannot redirect execution into a different session.
	handle := &NodeSession{state: executor.state.session}
	result, err := handle.execDebianProducer(node, options)
	if err != nil {
		return nil, nil, err
	}
	if err := revalidateDebianRuntime(ctx, options.debianRuntime, node); err != nil {
		return nil, nil, errors.Join(err, result.debian.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, errors.Join(e.Wrap(e.CodeTimeout, err, "completing selected Debian producer"), result.debian.Close())
	}
	return result.debian, result, nil
}

// RequireArchiveContext compares the concrete handle with this original executor
// and invocation subject before matching its exact instruction and source input.
// A successful comparison issues no Run, content, provenance or release receipt.
func (executor *DebianProducerExecutor) RequireArchiveContext(ctx context.Context, subject string, archive *DebianProducerArchive, inputDigest string) error {
	if err := requireRunContext(ctx, "joining Debian producer archive"); err != nil {
		return err
	}
	if executor == nil || executor.state == nil || executor.state.session == nil || archive == nil {
		return e.New(e.CodeProvenanceMissing, "original Debian executor and archive are required")
	}
	owned, finish, err := archive.lease.begin(ctx, debianRead)
	if err != nil {
		return err
	}
	defer finish()
	if archive.lease.executor != executor.state || archive.lease.subject != subject {
		return e.New(e.CodeVerifyFailed, "Debian archive belongs to another original executor or named subject")
	}
	executor.state.mu.Lock()
	raw, ok := executor.state.nodes[subject]
	executor.state.mu.Unlock()
	if !ok || !bytes.Equal(raw, archive.lease.instruction) || inputDigest != archive.lease.input {
		return e.New(e.CodeVerifyFailed, "Debian archive changed its original instruction or source input")
	}
	if err := owned.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "joining Debian producer archive")
	}
	return nil
}
