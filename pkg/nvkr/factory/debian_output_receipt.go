// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// DebianOutputObservation describes one original private Run's inspected raw
// archive. Its serialization cannot restore the receipt or grant publication.
type DebianOutputObservation struct {
	Digest      string        `json:"digest"`
	Size        int64         `json:"size"`
	BuildDigest string        `json:"buildDigest"`
	PlanDigest  string        `json:"planDigest"`
	Subject     string        `json:"subject"`
	Component   string        `json:"component"`
	InputDigest string        `json:"inputDigest"`
	Target      target.Debian `json:"target"`
	SBOMDigest  string        `json:"sbomDigest"`
}

// DebianOutputReceipt is process-local terminal byte/content authority for the
// original private Run. It grants neither qualification nor release/publication.
// Only observeBoundOutput can issue it, after original producer/inspection joins.
type DebianOutputReceipt struct{ state *debianOutputReceiptState }
type debianOutputReceiptState struct {
	owner       *debianRunState
	output      []byte
	observation DebianOutputObservation
}

func (state *debianRunState) observeBoundOutput(ctx context.Context, result *LocalRunResult, plan *Plan, output LifecycleOutputPlan) (*DebianOutputReceipt, error) {
	owned, finish, err := state.begin(ctx, result, plan, output)
	if err != nil {
		return nil, err
	}
	defer finish()
	// Use the originally owned output description before any runtime/byte I/O;
	// caller-owned component slices cannot redefine the receipt after begin.
	var required LifecycleOutputPlan
	if err := canonical.UnmarshalExact(state.outputs[output.ArtifactID], &required); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading original terminal output")
	}
	output = required
	state.mu.Lock()
	if state.receipts[output.ArtifactID] != nil || state.receiptClaims[output.ArtifactID] {
		state.mu.Unlock()
		return nil, e.New(e.CodeVerifyFailed, "named Debian terminal observation already claimed")
	}
	if state.receiptClaims == nil {
		state.receiptClaims = make(map[string]bool)
	}
	state.receiptClaims[output.ArtifactID] = true
	state.mu.Unlock()
	defer func() { state.mu.Lock(); delete(state.receiptClaims, output.ArtifactID); state.mu.Unlock() }()
	observation, err := state.recheckInspectedOutput(owned, output, io.Discard)
	if err != nil {
		if canceled := owned.Err(); canceled != nil {
			return nil, e.Wrap(e.CodeTimeout, errors.Join(err, canceled), "observing canceled original Debian terminal output")
		}
		return nil, err
	}
	encoded, err := canonical.Marshal(output)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "encoding original named Debian output")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := owned.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "issuing original Debian terminal receipt")
	}
	if state.closed {
		return nil, e.New(e.CodeEngineCapability, "original Debian Run closed before terminal receipt")
	}
	if state.receipts == nil {
		state.receipts = make(map[string]*debianOutputReceiptState)
	}
	if state.receipts[output.ArtifactID] != nil {
		return nil, e.New(e.CodeVerifyFailed, "named Debian terminal receipt already exists")
	}
	receipt := &debianOutputReceiptState{owner: state, output: encoded, observation: observation}
	state.receipts[output.ArtifactID] = receipt
	return &DebianOutputReceipt{state: receipt}, nil
}

func (state *debianRunState) recheckInspectedOutput(ctx context.Context, output LifecycleOutputPlan, destination io.Writer) (DebianOutputObservation, error) {
	var zero DebianOutputObservation
	state.mu.Lock()
	completion, completed := state.completions[output.ArtifactID]
	binding, bound := state.executors[output.ArtifactID]
	content, inspected := state.content[output.ArtifactID]
	state.mu.Unlock()
	if !completed || !bound || !inspected {
		return zero, e.New(e.CodeProvenanceMissing, "original Debian producer/content completion is absent")
	}
	description, err := completion.archive.DescribeContext(ctx)
	if err != nil {
		return zero, err
	}
	if err := binding.executor.RequireArchiveContext(ctx, output.ArtifactID, &completion.archive, description.InputDigest); err != nil {
		return zero, err
	}
	raw, err := canonical.Marshal(description)
	if err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "encoding Debian completion")
	}
	recorded, err := canonical.Marshal(completion.description)
	if err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "encoding recorded Debian completion")
	}
	if !bytes.Equal(raw, recorded) || description.BuildDigest != output.BuildDigest || description.Subject != output.ArtifactID || description.Instruction.Debian == nil {
		return zero, e.New(e.CodeVerifyFailed, "Debian terminal subject changes producer")
	}
	if len(content.bytes) == 0 || len(content.bytes) > sbom.MaxDocumentBytes {
		return zero, e.New(e.CodeVerifyFailed, "Debian terminal document exceeds complete-document policy")
	}
	var document sbom.BOM
	if err := canonical.UnmarshalExact(content.bytes, &document); err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "reading original Debian document")
	}
	canonicalDocument, err := document.Marshal()
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(canonicalDocument, content.bytes) || document.Metadata.Component.Name != description.Instruction.Component || len(document.Metadata.Component.Hashes) != 1 || document.Metadata.Component.Hashes[0].Content != strings.TrimPrefix(description.Digest, "sha256:") {
		return zero, e.New(e.CodeVerifyFailed, "Debian terminal document changes archive subject")
	}
	properties := map[string]string{}
	for _, property := range document.Metadata.Props {
		properties[property.Name] = property.Value
	}
	if properties["nvkr:buildDigest"] != output.BuildDigest || properties["nvkr:artifact"] != description.Instruction.Component || properties["nvkr:dependencyInspection"] != "disk-content/v1" || properties["nvkr:dependencyScannerDigest"] != content.profile.ScannerDigest {
		return zero, e.New(e.CodeVerifyFailed, "Debian terminal document changes Build/inspection bindings")
	}
	var original Plan
	if err := canonical.UnmarshalExact(state.plan, &original); err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "reading original Debian terminal Plan")
	}
	profile, err := diskEvidenceProfile(ctx, &original, &content.inspector)
	if err != nil {
		return zero, err
	}
	runtimeID, _, _, err := content.inspector.BindingContext(ctx)
	if err != nil {
		return zero, err
	}
	if profile != content.profile || runtimeID != content.runtime {
		return zero, e.New(e.CodeVerifyFailed, "Debian terminal inspection profile changed")
	}
	stream := &diskObservationWriter{ctx: ctx, destination: destination, digest: sha256.New()}
	count, err := completion.archive.WriteToContext(ctx, stream)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return zero, e.Wrap(e.CodeTimeout, errors.Join(err, canceled), "exporting canceled original Debian terminal output")
		}
		return zero, err
	}
	if count != description.Size || stream.size != description.Size || "sha256:"+hex.EncodeToString(stream.digest.Sum(nil)) != description.Digest {
		return zero, e.New(e.CodeVerifyFailed, "Debian terminal stream changes exact archive bytes")
	}
	if err := binding.executor.RequireArchiveContext(ctx, output.ArtifactID, &completion.archive, description.InputDigest); err != nil {
		return zero, err
	}
	finalRuntime, _, _, err := content.inspector.BindingContext(ctx)
	if err != nil {
		return zero, err
	}
	if finalRuntime != runtimeID {
		return zero, e.New(e.CodeVerifyFailed, "Debian terminal inspection runtime changed")
	}
	if err := ctx.Err(); err != nil {
		return zero, e.Wrap(e.CodeTimeout, err, "rechecking Debian terminal output")
	}
	digest, err := document.Digest()
	if err != nil {
		return zero, err
	}
	var originalDigest string
	originalDigest, err = original.Digest()
	if err != nil {
		return zero, err
	}
	return DebianOutputObservation{Digest: description.Digest, Size: description.Size, BuildDigest: description.BuildDigest, PlanDigest: originalDigest, Subject: description.Subject, Component: description.Instruction.Component, InputDigest: description.InputDigest, Target: *description.Instruction.Debian, SBOMDigest: digest}, nil
}

func (receipt *DebianOutputReceipt) use(ctx context.Context, destination io.Writer) (DebianOutputObservation, error) {
	var zero DebianOutputObservation
	if ctx == nil {
		return zero, e.New(e.CodeUsage, "Debian output receipt requires context")
	}
	if receipt == nil || receipt.state == nil || receipt.state.owner == nil {
		return zero, e.New(e.CodeProvenanceMissing, "original Debian output receipt is required")
	}
	frozen := receipt.state
	state := frozen.owner
	var plan Plan
	if err := canonical.UnmarshalExact(state.plan, &plan); err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "reading original receipt Plan")
	}
	var output LifecycleOutputPlan
	if err := canonical.UnmarshalExact(frozen.output, &output); err != nil {
		return zero, e.Wrap(e.CodeVerifyFailed, err, "reading original receipt output")
	}
	owned, finish, err := state.begin(ctx, state.original, &plan, output)
	if err != nil {
		return zero, err
	}
	defer finish()
	state.mu.Lock()
	current := state.receipts[output.ArtifactID]
	state.mu.Unlock()
	if current != frozen {
		return zero, e.New(e.CodeVerifyFailed, "Debian output receipt changed original Run membership")
	}
	got, err := state.recheckInspectedOutput(owned, output, destination)
	if err != nil {
		if canceled := owned.Err(); canceled != nil {
			return zero, e.Wrap(e.CodeTimeout, errors.Join(err, canceled), "using canceled original Debian terminal output")
		}
		return zero, err
	}
	if got != frozen.observation {
		return zero, e.New(e.CodeVerifyFailed, "Debian output receipt changes its issued terminal binding")
	}
	return got, nil
}

// DescribeContext independently repeats the byte/content/profile checks.
func (receipt *DebianOutputReceipt) DescribeContext(ctx context.Context) (DebianOutputObservation, error) {
	return receipt.use(ctx, io.Discard)
}

// WriteToContext streams exact original bytes to caller staging; it commits no
// pathname or publication. An arbitrary blocking writer must return for Close to join.
func (receipt *DebianOutputReceipt) WriteToContext(ctx context.Context, destination io.Writer) (DebianOutputObservation, error) {
	if destination == nil {
		return DebianOutputObservation{}, e.New(e.CodeUsage, "Debian output export requires staging writer")
	}
	return receipt.use(ctx, destination)
}
