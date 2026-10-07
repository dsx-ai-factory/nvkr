// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// LocalVMRequest binds one compiler-selected VM build to its exact runtime
// request. Subject is an opaque identifier owned by the Plan adapter; this
// package neither derives nor interprets it.
type LocalVMRequest struct {
	Subject      string
	Requirements *Requirements
	Binding      *Binding
}

// LocalVMAdmission is an opaque, process-local capability for one VM build. It
// retains the observed workspace and KVM handles until its single execution
// finishes or Close invalidates it. It is not provider allocation, reservation,
// qualification, or serializable evidence.
type LocalVMAdmission struct {
	mu      sync.Mutex
	request LocalVMRequest
	lease   localVMCapabilityLease
	state   localVMAdmissionState
}

type localVMAdmissionState uint8

const (
	localVMAdmissionReady localVMAdmissionState = iota + 1
	localVMAdmissionRunning
	localVMAdmissionClosed
)

type localVMCapabilityLease interface {
	localObservationLease
	reobserve(context.Context) (ObservedHostFacts, error)
	holdWorkspace() (*os.File, error)
	holdKVM() (*os.File, error)
}

// LocalVMRuntime exposes only held descriptor capabilities while Execute owns
// their lifetime. Callers must not resolve the original workspace or KVM paths.
// Handles returned here are closed when Execute returns, even if the callback
// forgets to close them.
type LocalVMRuntime struct {
	mu       sync.Mutex
	lease    localVMCapabilityLease
	kvm      bool
	active   bool
	borrowed []*os.File
}

// AdmitLocalVM authenticates one Linux host observation against the exact
// adapter-owned subject, request, and binding supplied by its caller. It does
// not authenticate a Plan by itself; factory owns that join. It performs no
// source, subprocess, output, or workspace mutation.
func AdmitLocalVM(ctx context.Context, request LocalVMRequest, workspace *os.File) (*LocalVMAdmission, error) {
	return admitLocalVM(ctx, request, workspace, systemLocalObserver{})
}

func admitLocalVM(
	ctx context.Context,
	request LocalVMRequest,
	workspace *os.File,
	observer localObserver,
) (*LocalVMAdmission, error) {
	if err := localObservationContextError(ctx, "local VM runtime admission"); err != nil {
		return nil, err
	}
	validated, err := validateLocalVMRequest(request)
	if err != nil {
		return nil, err
	}
	if workspace == nil {
		return nil, e.New(e.CodeUsage,
			"local VM runtime admission requires a held workspace directory")
	}
	if observer == nil {
		return nil, e.New(e.CodeInternal, "local VM runtime admission has no observer")
	}

	lease, err := observer.Open(ctx, workspace)
	if err != nil {
		return nil, err
	}
	capability, ok := lease.(localVMCapabilityLease)
	if !ok || capability == nil {
		cause := e.New(e.CodeEngineCapability,
			"local host observer cannot retain VM runtime capabilities")
		if lease != nil {
			if closeErr := lease.Close(); closeErr != nil {
				return nil, e.Wrap(e.CodeEngineCapability, errors.Join(cause, closeErr),
					"rejecting incomplete local VM runtime capabilities")
			}
		}
		return nil, cause
	}

	observed, observeErr := capability.Observe(ctx)
	if observeErr != nil {
		return nil, closeLocalVMAdmissionError(capability, observeErr)
	}
	if err := validateObservedHostFacts(observed); err != nil {
		return nil, closeLocalVMAdmissionError(capability, err)
	}
	if err := observedHostSatisfiesVM(validated, observed); err != nil {
		return nil, closeLocalVMAdmissionError(capability, err)
	}
	if err := localObservationContextError(ctx, "local VM runtime admission"); err != nil {
		return nil, closeLocalVMAdmissionError(capability, err)
	}
	return &LocalVMAdmission{
		request: validated,
		lease:   capability,
		state:   localVMAdmissionReady,
	}, nil
}

// Execute consumes the capability exactly once, rechecks the bound request, and
// invokes work under the authored executor deadline. The callback receives held
// descriptor capabilities, never caller-provided paths. All held and borrowed
// handles are closed before Execute returns.
func (admission *LocalVMAdmission) Execute(
	ctx context.Context,
	request LocalVMRequest,
	work func(context.Context, *LocalVMRuntime) error,
) (resultErr error) {
	if admission == nil {
		return e.New(e.CodeEngineCapability, "local VM runtime admission is absent")
	}
	if err := localObservationContextError(ctx, "local VM execution"); err != nil {
		return err
	}
	validated, err := validateLocalVMRequest(request)
	if err != nil {
		return err
	}
	if work == nil {
		return e.New(e.CodeUsage, "local VM execution requires work")
	}

	admission.mu.Lock()
	if admission.state != localVMAdmissionReady || admission.lease == nil {
		admission.mu.Unlock()
		return e.New(e.CodeEngineCapability,
			"local VM runtime admission is closed or has already been used")
	}
	if !equalLocalVMRequest(admission.request, validated) {
		admission.mu.Unlock()
		return e.New(e.CodeEngineCapability,
			"local VM runtime admission does not authorize the selected Plan node")
	}
	admission.state = localVMAdmissionRunning
	lease := admission.lease
	admission.mu.Unlock()

	runtime := &LocalVMRuntime{
		lease:  lease,
		kvm:    resolvedCapabilityEnabled(validated.Binding, CapabilityKVM),
		active: true,
	}
	defer func() {
		closeErr := runtime.close()
		admission.mu.Lock()
		admission.lease = nil
		admission.state = localVMAdmissionClosed
		admission.mu.Unlock()
		if closeErr != nil {
			if resultErr == nil {
				resultErr = e.Wrap(e.CodeEngineCapability, closeErr,
					"closing local VM execution capabilities")
			} else {
				var coded *e.Error
				if errors.As(resultErr, &coded) {
					resultErr = e.Wrap(coded.Code, errors.Join(resultErr, closeErr),
						"local VM execution failed and closing capabilities also failed")
				} else {
					resultErr = e.Wrap(e.CodeInternal, errors.Join(resultErr, closeErr),
						"local VM execution failed and closing capabilities also failed")
				}
			}
		}
	}()

	deadlineCtx, cancel := context.WithTimeout(ctx,
		time.Duration(validated.Requirements.TimeoutSeconds)*time.Second)
	defer cancel()
	observed, err := lease.reobserve(deadlineCtx)
	if err != nil {
		return err
	}
	if err := validateObservedHostFacts(observed); err != nil {
		return err
	}
	if err := observedHostSatisfiesVM(validated, observed); err != nil {
		return err
	}
	workErr := work(deadlineCtx, runtime)
	if deadlineErr := deadlineCtx.Err(); deadlineErr != nil {
		cause := deadlineErr
		if workErr != nil {
			cause = errors.Join(workErr, deadlineErr)
		}
		return e.Wrap(e.CodeTimeout, cause, "local VM execution exceeded its deadline")
	}
	if workErr != nil {
		var coded *e.Error
		if !errors.As(workErr, &coded) {
			return e.Wrap(e.CodeBuildFailed, workErr, "local VM execution failed")
		}
	}
	return workErr
}

// HoldWorkspace duplicates the already-authenticated workspace directory. The
// handle is valid only during the Execute callback and is closed on return.
func (runtime *LocalVMRuntime) HoldWorkspace() (*os.File, error) {
	return runtime.borrow(func(lease localVMCapabilityLease) (*os.File, error) {
		return lease.holdWorkspace()
	})
}

// HoldKVM duplicates the already-open, API-validated KVM device selected by the
// Plan. Disabled resolutions fail instead of falling back opportunistically.
// The handle is valid only during the Execute callback and is closed on return.
func (runtime *LocalVMRuntime) HoldKVM() (*os.File, error) {
	if runtime == nil || !runtime.kvm {
		return nil, e.New(e.CodeEngineCapability,
			"local VM execution did not select KVM")
	}
	return runtime.borrow(func(lease localVMCapabilityLease) (*os.File, error) {
		return lease.holdKVM()
	})
}

// Close invalidates an unused admission and releases every held capability.
func (admission *LocalVMAdmission) Close() error {
	if admission == nil {
		return nil
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	switch admission.state {
	case localVMAdmissionRunning:
		return e.New(e.CodeEngineCapability,
			"local VM runtime admission cannot close while execution is active")
	case localVMAdmissionClosed:
		return nil
	case localVMAdmissionReady:
		lease := admission.lease
		admission.lease = nil
		admission.state = localVMAdmissionClosed
		if lease != nil {
			if err := lease.Close(); err != nil {
				return e.Wrap(e.CodeEngineCapability, err,
					"closing local VM runtime admission")
			}
		}
		return nil
	default:
		return e.New(e.CodeEngineCapability, "local VM runtime admission is invalid")
	}
}

func (runtime *LocalVMRuntime) borrow(
	open func(localVMCapabilityLease) (*os.File, error),
) (*os.File, error) {
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "local VM runtime is absent")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.active || runtime.lease == nil {
		return nil, e.New(e.CodeEngineCapability, "local VM runtime is closed")
	}
	held, err := open(runtime.lease)
	if err != nil {
		return nil, err
	}
	runtime.borrowed = append(runtime.borrowed, held)
	return held, nil
}

func (runtime *LocalVMRuntime) close() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	runtime.active = false
	borrowed := runtime.borrowed
	runtime.borrowed = nil
	lease := runtime.lease
	runtime.lease = nil
	runtime.mu.Unlock()
	var closeErrors []error
	for _, file := range borrowed {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			closeErrors = append(closeErrors, err)
		}
	}
	if lease != nil {
		if err := lease.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func validateLocalVMRequest(request LocalVMRequest) (LocalVMRequest, error) {
	if strings.TrimSpace(request.Subject) == "" || request.Subject != strings.TrimSpace(request.Subject) ||
		strings.IndexFunc(request.Subject, unicode.IsControl) >= 0 {
		return LocalVMRequest{}, e.New(e.CodeEngineCapability,
			"local VM runtime admission requires a safe exact subject")
	}
	if request.Requirements == nil || request.Binding == nil {
		return LocalVMRequest{}, e.New(e.CodeEngineCapability,
			"local VM runtime admission requires execution requirements and a selected binding")
	}
	if err := request.Requirements.Validate(); err != nil {
		return LocalVMRequest{}, e.Wrap(e.CodeEngineCapability, err,
			"local VM runtime requirements are invalid")
	}
	if request.Requirements.Resources == nil || request.Requirements.Resources.CPUMillis == 0 ||
		request.Requirements.Resources.MemoryMiB == 0 ||
		request.Requirements.Resources.EphemeralStorageMiB == 0 ||
		request.Requirements.TimeoutSeconds == 0 {
		return LocalVMRequest{}, e.New(e.CodeEngineCapability,
			"local VM runtime admission requires complete resources and timeout")
	}
	if err := request.Binding.ValidateFor(request.Requirements); err != nil {
		return LocalVMRequest{}, e.Wrap(e.CodeEngineCapability, err,
			"local VM runtime binding is invalid")
	}
	return LocalVMRequest{
		Subject:      request.Subject,
		Requirements: Clone(request.Requirements),
		Binding:      CloneBinding(request.Binding),
	}, nil
}

func observedHostSatisfiesVM(request LocalVMRequest, observed ObservedHostFacts) error {
	if observed.Platform != request.Binding.Class.HostPlatform {
		return e.New(e.CodeEngineCapability,
			"local VM host platform %q does not realize selected execution class platform %q",
			observed.Platform, request.Binding.Class.HostPlatform)
	}
	want := request.Binding.Class.Resources
	want.CPUMillis = max(want.CPUMillis, request.Requirements.Resources.CPUMillis)
	want.MemoryMiB = max(want.MemoryMiB, request.Requirements.Resources.MemoryMiB)
	want.EphemeralStorageMiB = max(want.EphemeralStorageMiB,
		request.Requirements.Resources.EphemeralStorageMiB)
	if observed.Resources.CPUMillis < want.CPUMillis ||
		observed.Resources.MemoryMiB < want.MemoryMiB ||
		observed.Resources.EphemeralStorageMiB < want.EphemeralStorageMiB {
		return e.New(e.CodeEngineCapability,
			"local VM host does not realize selected execution class CPU, memory, and workspace capacity")
	}
	for _, resolution := range request.Binding.Capabilities {
		observedEnabled := slices.Contains(observed.Capabilities, resolution.Name)
		if resolution.Enabled && !observedEnabled {
			return e.New(e.CodeEngineCapability,
				"local VM host does not expose selected capability %q",
				resolution.Name)
		}
	}
	return nil
}

func resolvedCapabilityEnabled(binding *Binding, name CapabilityName) bool {
	if binding == nil {
		return false
	}
	for _, resolution := range binding.Capabilities {
		if resolution.Name == name {
			return resolution.Enabled
		}
	}
	return false
}

func equalLocalVMRequest(left, right LocalVMRequest) bool {
	return left.Subject == right.Subject && Equal(left.Requirements, right.Requirements) &&
		EqualBinding(left.Binding, right.Binding)
}

func closeLocalVMAdmissionError(lease localVMCapabilityLease, cause error) error {
	if lease == nil {
		return cause
	}
	if closeErr := lease.Close(); closeErr != nil {
		var coded *e.Error
		if errors.As(cause, &coded) {
			return e.Wrap(coded.Code, errors.Join(cause, closeErr),
				"local VM runtime admission failed and closing capabilities also failed")
		}
		return e.Wrap(e.CodeInternal, errors.Join(cause, closeErr),
			"local VM runtime admission failed and closing capabilities also failed")
	}
	return cause
}
