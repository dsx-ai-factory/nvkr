// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// The local native scheduler keeps the original session, retained producer bytes,
// inspection and terminal bindings under one Run lifetime. Custody alone supplies
// neither inspected-content nor release authority; each transition authenticates
// its original owner and exact named output.
type debianRunState struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	original            *LocalRunResult
	workspace           string
	ownedInspection     *sbom.DiskInspectionRuntime
	inspectionWorkspace *os.File
	directory           *os.Root
	sourceWorkspace     *pipeline.SourceWorkspace
	session             pipeline.NodeSession
	plan                []byte
	outputs             map[string][]byte
	executors           map[string]debianRunExecutorBinding
	completions         map[string]debianRunProducerCompletion
	content             map[string]debianRunContentDocument
	receipts            map[string]*debianOutputReceiptState
	receiptClaims       map[string]bool
	closed              bool
	done                chan struct{}
	closeErr            error
	active              sync.WaitGroup
	calls               map[context.Context]context.CancelFunc
}

func bindDebianRunSession(ctx context.Context, plan *Plan, result *LocalRunResult, session *pipeline.NodeSession) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "Debian Run custody requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "binding Debian Run custody")
	}
	if plan == nil || result == nil || session == nil {
		return e.New(e.CodeProvenanceMissing, "original Debian Run, Plan and producer session are required")
	}
	if err := session.RequireOpenContext(ctx); err != nil {
		return err
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "freezing Debian Run Plan")
	}
	var frozen Plan
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "copying Debian Run Plan")
	}
	digest, err := frozen.Digest()
	if err != nil {
		return err
	}
	if result.SchemaVersion != LocalRunSchemaVersion || result.PlanDigest != digest || result.Qualification != TierLocalExecution {
		return e.New(e.CodeVerifyFailed, "Debian custody requires the original local Run description")
	}
	outputs := make(map[string][]byte)
	for _, output := range frozen.LifecycleOutputs {
		if output.Kind != authored.OutputDebianPackage {
			continue
		}
		encoded, err := canonical.Marshal(output)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "freezing named Debian output")
		}
		outputs[output.ArtifactID] = encoded
	}
	if len(outputs) == 0 {
		return e.New(e.CodeEngineCapability, "Run has no explicit Debian output")
	}
	result.diskMu.Lock()
	defer result.diskMu.Unlock()
	if result.diskClosed || result.diskClosing || result.debian != nil {
		return e.New(e.CodeVerifyFailed, "Debian Run custody is closed or already bound")
	}
	workspace := result.Workspace
	if workspace == "" {
		workspace = ".nvkr/factory"
	}
	if err := validateLocalWorkspace(workspace, &frozen); err != nil {
		return err
	}
	owned, cancel := context.WithCancel(ctx)
	result.debian = &debianRunState{ctx: owned, cancel: cancel, original: result,
		session: *session, plan: raw, outputs: outputs, workspace: workspace, done: make(chan struct{}),
		calls: make(map[context.Context]context.CancelFunc)}
	return nil
}

func (state *debianRunState) requireOriginalSession(result *LocalRunResult, session localProducerSessionCloser) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.ctx.Err() != nil || result != state.original {
		return e.New(e.CodeVerifyFailed, "Debian custody handoff requires its original live Run and producer session")
	}
	concrete, ok := session.(*pipeline.NodeSession)
	if !ok {
		return e.New(e.CodeVerifyFailed, "Debian custody handoff has no original concrete session")
	}
	return state.session.RequireOwnerContext(state.ctx, concrete)
}

// Register the original live Run before any future archive/content/evidence
// operation; a saved description or equal-byte sibling cannot recreate it.
func (state *debianRunState) begin(ctx context.Context, result *LocalRunResult, plan *Plan, output LifecycleOutputPlan) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "Debian Run operation requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "using Debian Run custody")
	}
	if state == nil || result == nil || plan == nil {
		return nil, nil, e.New(e.CodeProvenanceMissing, "original Debian Run custody is required")
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeInvalidCatalog, err, "encoding Debian Run Plan")
	}
	encoded, err := canonical.Marshal(output)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeInvalidCatalog, err, "encoding named Debian output")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.ctx.Err() != nil {
		return nil, nil, e.New(e.CodeEngineCapability, "Debian Run custody is closed or canceled")
	}
	if err := state.session.RequireOpenContext(ctx); err != nil {
		return nil, nil, err
	}
	if result != state.original || result.debian != state || !bytes.Equal(raw, state.plan) || !bytes.Equal(encoded, state.outputs[output.ArtifactID]) {
		return nil, nil, e.New(e.CodeVerifyFailed, "Debian operation changes its original Run, complete Plan or named output")
	}
	owned, cancel := context.WithCancel(ctx)
	state.calls[owned] = cancel
	state.active.Add(1)
	stop := context.AfterFunc(state.ctx, cancel)
	return owned, func() {
		stop()
		cancel()
		state.mu.Lock()
		delete(state.calls, owned)
		state.mu.Unlock()
		state.active.Done()
	}, nil
}

func (state *debianRunState) closeOwned() error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		<-state.done
		return state.closeErr
	}
	state.closed = true
	for _, cancel := range state.calls {
		cancel()
	}
	state.cancel()
	state.mu.Unlock()
	// Stop and join original producer/archive operations before joining the
	// outer custody operations. The frozen value cannot follow facade replacement.
	err := state.session.CloseChecked()
	state.active.Wait()
	if state.sourceWorkspace != nil {
		err = errors.Join(err, state.sourceWorkspace.Close())
	}
	if state.ownedInspection != nil {
		err = errors.Join(err, state.ownedInspection.Close())
	}
	if state.inspectionWorkspace != nil {
		err = errors.Join(err, state.inspectionWorkspace.Close())
	}
	if state.directory != nil {
		err = errors.Join(err, state.directory.Close())
	}
	state.mu.Lock()
	state.closeErr = err
	close(state.done)
	state.mu.Unlock()
	return err
}
