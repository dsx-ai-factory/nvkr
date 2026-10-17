// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
)

// LocalDiagnosticDescription describes a complete retained local Run and its held
// ordinary outputs. It establishes no signatures, producer identity, registry
// availability, qualification or release authority. Output references and
// publication origins are deliberately absent; deployment locators are separate.
type LocalDiagnosticDescription struct {
	Lane              string                       `json:"lane"`
	Attempt           string                       `json:"attempt"`
	PlanDigest        string                       `json:"planDigest"`
	DefinitionDigest  string                       `json:"definitionDigest"`
	EnvironmentDigest string                       `json:"environmentDigest"`
	ContentSubject    string                       `json:"contentSubject"`
	CatalogDigest     string                       `json:"catalogDigest"`
	OrdinaryRoot      string                       `json:"ordinaryRoot,omitempty"`
	Outputs           []delivery.PublicationOutput `json:"outputs"`
}

// LocalDiagnosticArtifacts owns checked, held payloads from one complete local
// Run. It cannot be restored from descriptions or used as an ArtifactCheckReceipt.
// Shallow copies share revocation and cleanup ownership.
type LocalDiagnosticArtifacts struct{ state *localDiagnosticArtifactState }

type localDiagnosticArtifactState struct {
	mu         sync.RWMutex
	closed     bool
	raw        string
	capture    *localQualificationCapture
	context    context.Context
	cancel     context.CancelFunc
	operations sync.WaitGroup
	closeDone  chan struct{}
	closeErr   error
}

// CaptureLocalDiagnosticArtifacts replays the original compiler reads, requires
// a committed complete local Run, and captures/checks every named payload before
// issuing a diagnostic owner. attemptDirectory is the canonical repository-relative
// factory workspace, not a current authored Definition or caller-provided digest.
func CaptureLocalDiagnosticArtifacts(ctx context.Context, attemptDirectory string) (*LocalDiagnosticArtifacts, error) {
	inputs, err := loadLocalQualificationInputs(ctx, attemptDirectory)
	if err != nil {
		return nil, err
	}
	capture, err := captureLocalQualificationPayloads(ctx, attemptDirectory, inputs)
	if err != nil {
		return nil, err
	}
	description := LocalDiagnosticDescription{Lane: inputs.run.Lane, Attempt: inputs.run.Attempt,
		PlanDigest: inputs.run.PlanDigest, DefinitionDigest: inputs.run.DefinitionDigest,
		EnvironmentDigest: inputs.run.EnvironmentDigest, ContentSubject: inputs.run.ContentSubject,
		CatalogDigest: inputs.plan.CatalogDigest, OrdinaryRoot: capture.ordinaryRoot,
		Outputs: []delivery.PublicationOutput{}}
	if capture.ordinary != nil {
		// Delivery requires a locator to derive references. This private placeholder
		// is not published or exposed as a usable registry location.
		ordinary, readErr := capture.ordinary.Description("localhost/nvkr-local-diagnostic@" + capture.ordinaryRoot)
		if readErr != nil {
			return nil, errors.Join(readErr, capture.Close())
		}
		description.Outputs = ordinary.Outputs
		for i := range description.Outputs {
			description.Outputs[i].Reference = ""
			description.Outputs[i].Publication = nil
		}
	}
	raw, err := canonical.Marshal(description)
	if err != nil {
		return nil, errors.Join(err, capture.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(e.Wrap(e.CodeTimeout, err, "local diagnostic capture canceled"), capture.Close())
	}
	operationContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &LocalDiagnosticArtifacts{state: &localDiagnosticArtifactState{
		raw: string(raw), capture: capture, context: operationContext, cancel: cancel,
	}}, nil
}

// Description returns detached descriptions of held bytes, without granting
// production or qualification authority. Closed and zero owners are rejected.
func (owner *LocalDiagnosticArtifacts) Description() (LocalDiagnosticDescription, error) {
	if owner == nil || owner.state == nil {
		return LocalDiagnosticDescription{}, e.New(e.CodeGateUnmet, "local diagnostic artifacts have not been captured")
	}
	state := owner.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.closed || state.capture == nil || state.raw == "" {
		return LocalDiagnosticDescription{}, e.New(e.CodeGateUnmet, "local diagnostic artifacts are closed")
	}
	var description LocalDiagnosticDescription
	if err := canonical.UnmarshalExact([]byte(state.raw), &description); err != nil {
		return LocalDiagnosticDescription{}, e.Wrap(e.CodeGateUnmet, err, "reading local diagnostic description")
	}
	return description, nil
}

func (owner *LocalDiagnosticArtifacts) beginOperation(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "local diagnostic operation requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "local diagnostic operation canceled")
	}
	if owner == nil || owner.state == nil {
		return nil, nil, e.New(e.CodeGateUnmet, "local diagnostic artifacts have not been captured")
	}
	state := owner.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.capture == nil || state.raw == "" {
		return nil, nil, e.New(e.CodeGateUnmet, "local diagnostic artifacts are closed")
	}
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(state.context, cancel)
	state.operations.Add(1)
	var once sync.Once
	return operation, func() { once.Do(func() { stop(); cancel(); state.operations.Done() }) }, nil
}

// WithDiagnosticOperation borrows payload lifetime for the complete bounded
// diagnostic operation, including joined child and resource cleanup. Close
// cancels and waits for it. The callback may stream members through this owner,
// but MUST NOT call Close on this owner or a shallow copy: Close joins callbacks.
func (owner *LocalDiagnosticArtifacts) WithDiagnosticOperation(ctx context.Context, observe func(context.Context, LocalDiagnosticDescription) error) error {
	if observe == nil {
		return e.New(e.CodeUsage, "local diagnostic callback is required")
	}
	operation, done, err := owner.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer done()
	description, err := owner.Description()
	if err != nil {
		return err
	}
	if err := operation.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "local diagnostic operation canceled before observation")
	}
	if err := observe(operation, description); err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return err
		}
		return e.Wrap(e.CodeVerifyFailed, err, "local diagnostic operation failed")
	}
	if err := operation.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "local diagnostic operation canceled")
	}
	_, err = owner.Description()
	return err
}

// WriteExecutableMember streams an exact native Linux executable from held local
// bytes through delivery's existing complete archive/member checks. It grants
// payload access only. The caller bounds destination writes and discards partial
// bytes on error; complete executable use belongs inside WithDiagnosticOperation.
func (owner *LocalDiagnosticArtifacts) WriteExecutableMember(ctx context.Context, output, member, platform string, maxBytes int64, destination io.Writer) (ExecutableMemberDescription, error) {
	if destination == nil || maxBytes <= 0 {
		return ExecutableMemberDescription{}, e.New(e.CodeUsage, "local executable streaming requires bounded destination and positive byte limit")
	}
	for _, value := range []string{output, member} {
		if !safetext.Valid(value, 4096, false) || strings.TrimSpace(value) != value {
			return ExecutableMemberDescription{}, e.New(e.CodeUsage, "local executable output and member require explicit safe text")
		}
	}
	if platform != "linux/arm64" && platform != "linux/amd64" {
		return ExecutableMemberDescription{}, e.New(e.CodeEngineCapability, "local executable requires native Linux platform")
	}
	operation, done, err := owner.beginOperation(ctx)
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	defer done()
	description, err := owner.Description()
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	var selected *delivery.PublicationOutput
	for i := range description.Outputs {
		if description.Outputs[i].Name == output {
			if selected != nil {
				return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "local executable output is ambiguous")
			}
			selected = &description.Outputs[i]
		}
	}
	if selected == nil {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "local executable requires one exact named output")
	}
	if selected.Kind != OutputExecutable || selected.OS+"/"+selected.Arch != platform {
		return ExecutableMemberDescription{}, e.New(e.CodeEngineCapability, "local executable output kind or platform differs")
	}
	view := ArtifactCheckOutput{Name: selected.Name, Kind: selected.Kind, Platform: platform,
		BuildSubjects: selected.BuildSubjects}
	result, err := streamExecutableMember(operation, owner.state.capture.ordinary, view, member, platform, maxBytes, destination)
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	if _, err := owner.Description(); err != nil {
		return ExecutableMemberDescription{}, err
	}
	return result, nil
}

// Close revokes all shallow copies, cancels active work and joins its cleanup
// before releasing held payloads. Repeated/concurrent closes return the same result.
func (owner *LocalDiagnosticArtifacts) Close() error {
	if owner == nil || owner.state == nil {
		return nil
	}
	state := owner.state
	state.mu.Lock()
	if state.closed {
		done := state.closeDone
		state.mu.Unlock()
		<-done
		state.mu.RLock()
		defer state.mu.RUnlock()
		return state.closeErr
	}
	state.closed = true
	state.closeDone = make(chan struct{})
	state.cancel()
	state.mu.Unlock()
	state.operations.Wait()
	err := state.capture.Close()
	state.mu.Lock()
	state.closeErr = err
	state.capture = nil
	close(state.closeDone)
	state.mu.Unlock()
	return err
}
