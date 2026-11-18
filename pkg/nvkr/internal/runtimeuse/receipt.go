// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package runtimeuse carries the factory's original-Run authorization into the
// pipeline executor without exposing a public constructor for execution rights.
// Issue is an internal integration boundary: the factory must authenticate live
// production and completed evidence, retain the receipt, and revoke it on Close.
package runtimeuse

import (
	"bytes"
	"context"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ProductionBinding is descriptive metadata for an original runtime production.
// Its bytes and JSON shape are also exposed by pipeline.ProducedRuntimeBinding.
type ProductionBinding struct {
	BuildDigest    string `json:"buildDigest"`
	Platform       string `json:"platform"`
	Component      string `json:"component"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	LayoutDigest   string `json:"layoutDigest"`
	LayerDigest    string `json:"layerDigest"`
	TerminalDigest string `json:"terminalDigest"`
}

// Binding is a record of the selected use, not a restorable capability. Subject
// includes the original Plan identity, relocated Plan digest and consumer ID.
// The original attempt itself is bound by the retained owner authenticator.
type Binding struct {
	PlanDigest  string              `json:"planDigest"`
	Subject     string              `json:"subject"`
	Purpose     string              `json:"purpose"`
	Runtime     ir.RuntimeSelection `json:"runtime"`
	Production  ProductionBinding   `json:"production"`
	Observation string              `json:"observation"`
	SBOM        string              `json:"sbom"`
}

// Receipt can be consumed once, only while its original Run remains live. Copies
// share consumption and revocation state. Close cancels and joins active work.
type Receipt struct{ state *state }

type state struct {
	mu           sync.Mutex
	binding      Binding
	node         []byte
	authenticate func(context.Context) error
	used         bool
	closed       bool
	succeeded    bool
	cancel       context.CancelFunc
	done         chan struct{}
}

// Issue must only be called by the factory after joining original production,
// Plan and evidence ownership. authenticate must repeat that ownership check;
// a callback that merely validates descriptions is not a valid issuer.
func Issue(ctx context.Context, binding Binding, node ir.Node, authenticate func(context.Context) error) (*Receipt, error) {
	if ctx == nil || authenticate == nil {
		return nil, e.New(e.CodeUsage, "runtime use requires context and original Run authentication")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "runtime use issuance canceled")
	}
	if err := ValidateBindingDescription(binding, node); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(node)
	if err != nil {
		return nil, err
	}
	if err := authenticate(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "runtime use issuance canceled")
	}
	return &Receipt{state: &state{binding: binding, node: raw, authenticate: authenticate}}, nil
}

// ValidateBindingDescription checks a recorded use against its exact producer
// selection. This admits descriptive evidence only; it cannot restore a receipt.
func ValidateBindingDescription(binding Binding, node ir.Node) error {
	if err := node.Validate(); err != nil {
		return err
	}
	subject := strings.SplitN(binding.Subject, "/", 3)
	if node.Runtime == nil || *node.Runtime != binding.Runtime || binding.Purpose != "packer-build" ||
		binding.Production.Size <= 0 || binding.Production.BuildDigest != binding.Runtime.BuildDigest ||
		binding.Production.Component != binding.Runtime.Component || binding.Production.Platform != binding.Runtime.Platform ||
		len(subject) != 3 || subject[0] != binding.PlanDigest || !canonical.IsDigest(subject[1]) ||
		!strings.HasSuffix(subject[2], "/"+node.ID) || strings.ContainsAny(binding.Subject, "\x00\r\n") {
		return e.New(e.CodeVerifyFailed, "runtime use changes its producer, subject or consumer purpose")
	}
	for _, digest := range []string{binding.PlanDigest, binding.Production.Digest, binding.Production.LayerDigest,
		binding.Production.LayoutDigest, binding.Production.TerminalDigest, binding.Observation, binding.SBOM} {
		if !canonical.IsDigest(digest) {
			return e.New(e.CodeVerifyFailed, "runtime use requires exact production and evidence digests")
		}
	}
	return nil
}

// Execute authenticates an exact consumer and production before work and again
// before reporting success. The callback owns stop/join of all child processes.
func (receipt *Receipt) Execute(ctx context.Context, subject string, node ir.Node, production ProductionBinding, work func(context.Context) error) error {
	if ctx == nil || work == nil {
		return e.New(e.CodeUsage, "runtime use requires context and bounded consumer work")
	}
	if _, ok := ctx.Deadline(); !ok {
		return e.New(e.CodeEngineCapability, "runtime use requires an admitted consumer deadline")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime use canceled")
	}
	if receipt == nil || receipt.state == nil {
		return e.New(e.CodeGateUnmet, "runtime use receipt is absent")
	}
	raw, err := canonical.Marshal(node)
	if err != nil {
		return err
	}
	state := receipt.state
	state.mu.Lock()
	if state.closed || state.used {
		state.mu.Unlock()
		return e.New(e.CodeGateUnmet, "runtime use receipt is revoked or already consumed")
	}
	if subject != state.binding.Subject || production != state.binding.Production || !bytes.Equal(raw, state.node) {
		state.mu.Unlock()
		return e.New(e.CodeVerifyFailed, "runtime use substitutes its consumer or production")
	}
	runCtx, cancel := context.WithCancel(ctx)
	state.used, state.cancel, state.done = true, cancel, make(chan struct{})
	state.mu.Unlock()
	defer func() {
		cancel()
		state.mu.Lock()
		state.cancel = nil
		close(state.done)
		state.mu.Unlock()
	}()
	if err := state.authenticate(runCtx); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime use revoked before execution")
	}
	if err := work(runCtx); err != nil {
		return err
	}
	if err := state.authenticate(runCtx); err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := runCtx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime use canceled during execution")
	}
	if state.closed {
		return e.New(e.CodeGateUnmet, "runtime use was revoked during execution")
	}
	state.succeeded = true
	return nil
}

// CompletedBinding is descriptive evidence available only after successful use.
// It cannot recreate the receipt or admit another consumer.
func (receipt *Receipt) CompletedBinding() (Binding, error) {
	if receipt == nil || receipt.state == nil {
		return Binding{}, e.New(e.CodeGateUnmet, "runtime use receipt is absent")
	}
	state := receipt.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || !state.succeeded || state.cancel != nil {
		return Binding{}, e.New(e.CodeGateUnmet, "runtime use has no live completed consumer")
	}
	return state.binding, nil
}

// Revoke cancels without waiting so an owner can revoke every consumer before
// joining any of them. Close must follow before releasing production resources.
func (receipt *Receipt) Revoke() {
	if receipt == nil || receipt.state == nil {
		return
	}
	state := receipt.state
	state.mu.Lock()
	defer state.mu.Unlock()
	state.closed = true
	if state.cancel != nil {
		state.cancel()
	}
}

func (receipt *Receipt) Close() {
	if receipt == nil || receipt.state == nil {
		return
	}
	receipt.Revoke()
	state := receipt.state
	state.mu.Lock()
	done := state.done
	state.mu.Unlock()
	if done != nil {
		<-done
	}
}
