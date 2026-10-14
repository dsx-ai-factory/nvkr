// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// DebianProducerDescription is an inspectable description of a retained
// invocation. Serialization preserves descriptions, never the issuing archive,
// executor, original session, Run or final-output evidence authority.
type DebianProducerDescription struct {
	BuildDigest string  `json:"buildDigest"`
	Subject     string  `json:"subject"`
	InputDigest string  `json:"inputDigest"`
	Digest      string  `json:"digest"`
	Size        int64   `json:"size"`
	Instruction ir.Node `json:"instruction"`
}

// DebianProducerBinding is a process-local exclusive subject binding. It can
// neither create an executor nor restore a Run, inventory or evidence receipt.
type DebianProducerBinding struct{ state *debianProducerBindingState }

type debianProducerBindingState struct {
	executor *debianProducerExecutorState
	subject  string
}

// BindSelectionContext reserves an uninvoked subject while the owning factory
// installs its original Run binding. Invocation cannot interleave or claim that
// subject for another Run. Failed installation rolls the reservation back.
// The callback must not reenter this executor or close its session; it may only
// install the caller's already authenticated local binding and must return.
// Neither the callback nor this capability supplies final-output authority.
func (executor *DebianProducerExecutor) BindSelectionContext(ctx context.Context, subject string, instruction ir.Node,
	install func(context.Context, *DebianProducerBinding) error,
) error {
	owned, finish, err := executor.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if install == nil {
		return e.New(e.CodeUsage, "Debian selection binding requires an installation callback")
	}
	raw, err := canonical.Marshal(instruction)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "encoding Debian selection binding")
	}
	executor.state.mu.Lock()
	defer executor.state.mu.Unlock()
	want, exists := executor.state.nodes[subject]
	if !exists || !bytes.Equal(raw, want) || executor.state.consumed[subject] || executor.state.bindings[subject] != nil {
		return e.New(e.CodeVerifyFailed, "Debian subject is absent, changed, invoked or already bound")
	}
	if err := owned.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "binding original Debian subject")
	}
	binding := &DebianProducerBinding{state: &debianProducerBindingState{executor: executor.state, subject: subject}}
	if executor.state.bindings == nil {
		executor.state.bindings = make(map[string]*debianProducerBindingState)
	}
	executor.state.bindings[subject] = binding.state
	if err := install(owned, binding); err != nil {
		delete(executor.state.bindings, subject)
		if canceled := owned.Err(); canceled != nil {
			return e.Wrap(e.CodeTimeout, errors.Join(err, canceled), "installing canceled Debian subject binding")
		}
		var coded *e.Error
		if errors.As(err, &coded) {
			return err
		}
		return e.Wrap(e.CodeInternal, err, "installing original Debian subject binding")
	}
	if err := owned.Err(); err != nil {
		delete(executor.state.bindings, subject)
		return e.Wrap(e.CodeTimeout, err, "completing original Debian subject binding")
	}
	return nil
}

// RequireSelectionContext compares an uninvoked original subject/instruction.
// This read-only comparison cannot reserve the selection against concurrent
// invocation. Factory installation must use BindSelectionContext instead.
// This comparison issues no execution capability.
func (executor *DebianProducerExecutor) RequireSelectionContext(ctx context.Context, subject string, instruction ir.Node) error {
	owned, finish, err := executor.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if err := executor.matchesInstruction(subject, instruction); err != nil {
		return err
	}
	executor.state.mu.Lock()
	consumed := executor.state.consumed[subject]
	executor.state.mu.Unlock()
	if consumed {
		return e.New(e.CodeVerifyFailed, "Debian invocation must join its original Run before producer work")
	}
	if err := owned.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "joining original Debian selection")
	}
	return nil
}

// RequireBuildContext compares the complete original graph and concrete session
// before a future factory join. Equal logical Build identity alone is insufficient
// to replace the frozen instruction/transport description or original owner.
// This comparison neither issues an executor nor executes a producer.
func (executor *DebianProducerExecutor) RequireBuildContext(ctx context.Context, session *NodeSession, graph *ir.Graph, buildDigest string) error {
	owned, finish, err := executor.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	original := &NodeSession{state: executor.state.session}
	if err := original.RequireOwnerContext(owned, session); err != nil {
		return err
	}
	if graph == nil {
		return e.New(e.CodeProvenanceMissing, "complete original Debian Build graph is required")
	}
	if err := graph.Validate(); err != nil {
		return err
	}
	digest, err := graph.Digest()
	if err != nil {
		return err
	}
	raw, err := canonical.Marshal(graph)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "encoding complete Debian producer graph")
	}
	if digest != buildDigest || buildDigest != executor.state.build || !bytes.Equal(raw, executor.state.graph) {
		return e.New(e.CodeVerifyFailed, "Debian producer changes its original complete graph or Build")
	}
	if err := owned.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "joining original Debian Build")
	}
	return nil
}

// DescribeContext reads a completed selected invocation through its issuing
// archive lifetime. The returned instruction owns its nested data. This supplies
// no observed Debian control fields, inspected inventory or factory Run receipt.
func (archive *DebianProducerArchive) DescribeContext(ctx context.Context) (*DebianProducerDescription, error) {
	if err := requireRunContext(ctx, "describing retained Debian producer"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "describing retained Debian producer")
	}
	if archive == nil || archive.lease == nil || archive.lease.executor == nil {
		return nil, e.New(e.CodeProvenanceMissing, "original selected Debian archive is required")
	}
	owned, finish, err := archive.lease.begin(ctx, debianRead)
	if err != nil {
		return nil, err
	}
	defer finish()
	lease := archive.lease
	executor := lease.executor
	executor.mu.Lock()
	raw, selected := executor.nodes[lease.subject]
	consumed := executor.consumed[lease.subject]
	executor.mu.Unlock()
	if !selected || !consumed || executor.build == "" || !bytes.Equal(raw, lease.instruction) {
		return nil, e.New(e.CodeVerifyFailed, "Debian archive lacks its original selected invocation")
	}
	var instruction ir.Node
	if err := canonical.UnmarshalExact(lease.instruction, &instruction); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading original Debian instruction")
	}
	if err := owned.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "describing original Debian completion")
	}
	return &DebianProducerDescription{BuildDigest: executor.build, Subject: lease.subject,
		InputDigest: lease.input, Digest: lease.digest, Size: lease.size, Instruction: instruction}, nil
}
