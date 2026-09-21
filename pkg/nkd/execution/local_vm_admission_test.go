package execution

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type fakeVMObserver struct {
	lease *fakeVMLease
	err   error
}

func (observer fakeVMObserver) Open(context.Context, *os.File) (localObservationLease, error) {
	if observer.err != nil {
		return nil, observer.err
	}
	return observer.lease, nil
}

type fakeVMLease struct {
	facts          ObservedHostFacts
	reobserveFacts *ObservedHostFacts
	workspacePath  string
	kvmPath        string
	observeErr     error
	reobserveErr   error
	closeErr       error
	holdStarted    chan struct{}
	holdRelease    chan struct{}
	observed       int
	reobserved     int
	closed         bool
}

func (lease *fakeVMLease) Observe(context.Context) (ObservedHostFacts, error) {
	lease.observed++
	return cloneObservedHostFacts(lease.facts), lease.observeErr
}

func (lease *fakeVMLease) reobserve(context.Context) (ObservedHostFacts, error) {
	lease.reobserved++
	if lease.reobserveErr != nil {
		return ObservedHostFacts{}, lease.reobserveErr
	}
	if lease.reobserveFacts != nil {
		return cloneObservedHostFacts(*lease.reobserveFacts), nil
	}
	return cloneObservedHostFacts(lease.facts), nil
}

func (lease *fakeVMLease) Close() error {
	lease.closed = true
	return lease.closeErr
}

func (lease *fakeVMLease) holdWorkspace() (*os.File, error) {
	if lease.holdStarted != nil {
		close(lease.holdStarted)
		<-lease.holdRelease
	}
	return os.Open(lease.workspacePath)
}

func (lease *fakeVMLease) holdKVM() (*os.File, error) {
	return os.Open(lease.kvmPath)
}

func testLocalVMRequest(t *testing.T, policy CapabilityPolicy, classKVM bool) LocalVMRequest {
	t.Helper()
	requirements := &Requirements{
		Resources: &ResourceRequest{
			CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
		},
		AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64},
		Capabilities:         []CapabilityRequirement{{Name: CapabilityKVM, Policy: policy}},
		TimeoutSeconds:       1,
	}
	class := Class{
		Name: "local-vm", RunsOn: "local-vm", HostPlatform: HostPlatformLinuxAMD64,
		Resources: ResourceCapacity{
			CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536,
		},
		MaxTimeoutSeconds: 2,
	}
	if classKVM {
		class.Capabilities = []CapabilityName{CapabilityKVM}
	}
	binding, err := Match(requirements, []Class{class}, MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return LocalVMRequest{Subject: "plan/node", Requirements: requirements, Binding: binding}
}

func testVMFacts(kvm bool) ObservedHostFacts {
	facts := ObservedHostFacts{
		Platform: HostPlatformLinuxAMD64,
		Resources: ResourceCapacity{
			CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536,
		},
	}
	if kvm {
		facts.Capabilities = []CapabilityName{CapabilityKVM}
	}
	return facts
}

func testVMLease(t *testing.T, facts ObservedHostFacts) *fakeVMLease {
	t.Helper()
	kvmPath := t.TempDir() + "/kvm"
	if err := os.WriteFile(kvmPath, []byte("kvm"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &fakeVMLease{facts: facts, workspacePath: t.TempDir(), kvmPath: kvmPath}
}

func cloneLocalVMRequest(request LocalVMRequest) LocalVMRequest {
	return LocalVMRequest{
		Subject: request.Subject, Requirements: Clone(request.Requirements), Binding: CloneBinding(request.Binding),
	}
}

func TestLocalVMAdmissionRejectsUnsatisfiedHost(t *testing.T) {
	base := testLocalVMRequest(t, CapabilityRequired, true)
	for _, test := range []struct {
		name   string
		facts  ObservedHostFacts
		mutate func(*ObservedHostFacts)
	}{
		{name: "platform", facts: testVMFacts(true), mutate: func(facts *ObservedHostFacts) {
			facts.Platform = HostPlatformLinuxARM64
		}},
		{name: "cpu", facts: testVMFacts(true), mutate: func(facts *ObservedHostFacts) {
			facts.Resources.CPUMillis--
		}},
		{name: "memory", facts: testVMFacts(true), mutate: func(facts *ObservedHostFacts) {
			facts.Resources.MemoryMiB--
		}},
		{name: "workspace storage", facts: testVMFacts(true), mutate: func(facts *ObservedHostFacts) {
			facts.Resources.EphemeralStorageMiB--
		}},
		{name: "required KVM", facts: testVMFacts(false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts := test.facts
			if test.mutate != nil {
				test.mutate(&facts)
			}
			lease := testVMLease(t, facts)
			admission, err := admitLocalVM(t.Context(), base, openLocalWorkspace(t), fakeVMObserver{lease: lease})
			if admission != nil || errorCode(err) != nkderr.CodeEngineCapability {
				t.Fatalf("admission = %#v, error = %v; want %s", admission, err, nkderr.CodeEngineCapability)
			}
			if !lease.closed {
				t.Fatal("failed admission retained host capabilities")
			}
		})
	}
}

func TestLocalVMAdmissionEnforcesResolvedKVMMode(t *testing.T) {
	for _, test := range []struct {
		name     string
		policy   CapabilityPolicy
		classKVM bool
		observed bool
		wantCode nkderr.Code
	}{
		{name: "required enabled", policy: CapabilityRequired, classKVM: true, observed: true},
		{name: "preferred enabled", policy: CapabilityPreferred, classKVM: true, observed: true},
		{name: "preferred disabled", policy: CapabilityPreferred, observed: false},
		{name: "forbidden disabled", policy: CapabilityForbidden, observed: false},
		{name: "required unavailable", policy: CapabilityRequired, classKVM: true, observed: false, wantCode: nkderr.CodeEngineCapability},
		{name: "preferred enabled unavailable", policy: CapabilityPreferred, classKVM: true, observed: false, wantCode: nkderr.CodeEngineCapability},
		{name: "preferred disabled with host KVM withheld", policy: CapabilityPreferred, observed: true},
		{name: "forbidden with host KVM withheld", policy: CapabilityForbidden, observed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := testLocalVMRequest(t, test.policy, test.classKVM)
			lease := testVMLease(t, testVMFacts(test.observed))
			admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
			if errorCode(err) != test.wantCode {
				t.Fatalf("admission error = %v, want %s", err, test.wantCode)
			}
			if test.wantCode != "" {
				if admission != nil || !lease.closed {
					t.Fatalf("failed admission = %#v, lease closed = %t", admission, lease.closed)
				}
				return
			}
			if admission == nil {
				t.Fatal("successful admission returned no capability")
			}
			if !test.classKVM {
				if err := admission.Execute(t.Context(), request, func(_ context.Context, runtime *LocalVMRuntime) error {
					_, holdErr := runtime.HoldKVM()
					return holdErr
				}); errorCode(err) != nkderr.CodeEngineCapability {
					t.Fatalf("disabled KVM handoff error = %v, want %s", err, nkderr.CodeEngineCapability)
				}
				return
			}
			if err := admission.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalVMAdmissionIsExactSingleUseAndDeadlineBound(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityRequired, true)
	lease := testVMLease(t, testVMFacts(true))
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*LocalVMRequest)
	}{
		{name: "subject", mutate: func(request *LocalVMRequest) { request.Subject = "other/node" }},
		{name: "requirements", mutate: func(request *LocalVMRequest) { request.Requirements.Resources.MemoryMiB++ }},
		{name: "binding", mutate: func(request *LocalVMRequest) { request.Binding.Class.RunsOn = "other-route" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneLocalVMRequest(request)
			test.mutate(&changed)
			called := false
			err := admission.Execute(t.Context(), changed, func(context.Context, *LocalVMRuntime) error {
				called = true
				return nil
			})
			if errorCode(err) != nkderr.CodeEngineCapability || called {
				t.Fatalf("error = %v, callback called = %t; want exact-subject rejection", err, called)
			}
		})
	}

	var workspace, kvm *os.File
	var retainedRuntime *LocalVMRuntime
	err = admission.Execute(t.Context(), request, func(ctx context.Context, runtime *LocalVMRuntime) error {
		retainedRuntime = runtime
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 1100*time.Millisecond {
			t.Fatalf("executor deadline = %v, present = %t", deadline, ok)
		}
		var holdErr error
		workspace, holdErr = runtime.HoldWorkspace()
		if holdErr != nil {
			return holdErr
		}
		kvm, holdErr = runtime.HoldKVM()
		return holdErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !lease.closed {
		t.Fatal("completed execution retained admission handles")
	}
	if _, err := workspace.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("borrowed workspace remained open: %v", err)
	}
	if _, err := kvm.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("borrowed KVM remained open: %v", err)
	}
	if _, err := retainedRuntime.HoldWorkspace(); errorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("closed runtime error = %v, want %s", err, nkderr.CodeEngineCapability)
	}
	if err := admission.Execute(t.Context(), request, func(context.Context, *LocalVMRuntime) error { return nil }); errorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("reused admission error = %v, want %s", err, nkderr.CodeEngineCapability)
	}
}

func TestLocalVMAdmissionCancellationCloseAndNoWorkBeforeAdmission(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	observerFailure := nkderr.New(nkderr.CodeEngineCapability, "workspace inaccessible")
	if admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{err: observerFailure}); admission != nil || !errors.Is(err, observerFailure) {
		t.Fatalf("inaccessible workspace admission = %#v, error = %v", admission, err)
	}

	lease := testVMLease(t, testVMFacts(false))
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	if err := admission.Execute(canceled, request, func(context.Context, *LocalVMRuntime) error {
		called = true
		return nil
	}); errorCode(err) != nkderr.CodeTimeout || called {
		t.Fatalf("canceled execution error = %v, callback called = %t", err, called)
	}
	if err := admission.Close(); err != nil {
		t.Fatal(err)
	}
	if !lease.closed {
		t.Fatal("closed admission retained capabilities")
	}
	if err := admission.Execute(t.Context(), request, func(context.Context, *LocalVMRuntime) error {
		called = true
		return nil
	}); errorCode(err) != nkderr.CodeEngineCapability || called {
		t.Fatalf("closed admission error = %v, callback called = %t", err, called)
	}
}

func TestLocalVMAdmissionReturnsAuthoredTimeout(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	lease := testVMLease(t, testVMFacts(false))
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	workFailure := nkderr.New(nkderr.CodeBuildFailed, "driver stopped at deadline")
	started := time.Now()
	err = admission.Execute(t.Context(), request, func(ctx context.Context, _ *LocalVMRuntime) error {
		<-ctx.Done()
		return workFailure
	})
	if errorCode(err) != nkderr.CodeTimeout || !errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, workFailure) {
		t.Fatalf("deadline error = %v, want %s wrapping deadline and work failure", err, nkderr.CodeTimeout)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("authored one-second timeout elapsed in %s", elapsed)
	}
}

func TestLocalVMAdmissionReobservesMutableCapacityBeforeWork(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	initial := testVMFacts(false)
	fresh := testVMFacts(false)
	fresh.Resources.MemoryMiB--
	lease := testVMLease(t, initial)
	lease.reobserveFacts = &fresh
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = admission.Execute(t.Context(), request, func(context.Context, *LocalVMRuntime) error {
		called = true
		return nil
	})
	if errorCode(err) != nkderr.CodeEngineCapability || called {
		t.Fatalf("fresh capacity error = %v, callback called = %t", err, called)
	}
	if lease.reobserved != 1 || !lease.closed {
		t.Fatalf("fresh observations = %d, lease closed = %t", lease.reobserved, lease.closed)
	}
}

func TestLocalVMAdmissionCodesCallbackFailure(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	lease := testVMLease(t, testVMFacts(false))
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("driver failed")
	err = admission.Execute(t.Context(), request, func(context.Context, *LocalVMRuntime) error { return cause })
	if errorCode(err) != nkderr.CodeBuildFailed || !errors.Is(err, cause) {
		t.Fatalf("callback error = %v, want %s wrapping cause", err, nkderr.CodeBuildFailed)
	}
}

func TestLocalVMAdmissionCoordinatesConcurrentExecuteCloseAndBorrow(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	lease := testVMLease(t, testVMFacts(false))
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}

	runtimeReady := make(chan *LocalVMRuntime)
	release := make(chan struct{})
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- admission.Execute(t.Context(), request, func(_ context.Context, runtime *LocalVMRuntime) error {
			runtimeReady <- runtime
			<-release
			return nil
		})
	}()

	runtime := <-runtimeReady
	workspace, err := runtime.HoldWorkspace()
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := admission.Close(); errorCode(err) != nkderr.CodeEngineCapability {
		close(release)
		t.Fatalf("concurrent close error = %v, want %s", err, nkderr.CodeEngineCapability)
	}
	called := false
	if err := admission.Execute(t.Context(), request, func(context.Context, *LocalVMRuntime) error {
		called = true
		return nil
	}); errorCode(err) != nkderr.CodeEngineCapability || called {
		close(release)
		t.Fatalf("concurrent reuse error = %v, callback called = %t", err, called)
	}
	close(release)
	if err := <-executeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("concurrently borrowed workspace remained open: %v", err)
	}
	if !lease.closed {
		t.Fatal("completed concurrent execution retained capabilities")
	}
}

func TestLocalVMAdmissionCoordinatesBorrowWithExecutionCleanup(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	lease := testVMLease(t, testVMFacts(false))
	lease.holdStarted = make(chan struct{})
	lease.holdRelease = make(chan struct{})
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}

	runtimeReady := make(chan *LocalVMRuntime)
	callbackReturning := make(chan struct{})
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- admission.Execute(t.Context(), request, func(_ context.Context, runtime *LocalVMRuntime) error {
			runtimeReady <- runtime
			<-callbackReturning
			return nil
		})
	}()
	runtime := <-runtimeReady
	borrowed := make(chan *os.File, 1)
	borrowError := make(chan error, 1)
	go func() {
		workspace, holdErr := runtime.HoldWorkspace()
		borrowed <- workspace
		borrowError <- holdErr
	}()
	<-lease.holdStarted
	close(callbackReturning)
	close(lease.holdRelease)
	workspace := <-borrowed
	if err := <-borrowError; err != nil {
		t.Fatal(err)
	}
	if err := <-executeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("borrow overlapping cleanup remained open: %v", err)
	}
	if !lease.closed {
		t.Fatal("execution cleanup did not close the capability lease")
	}
}

func TestLocalVMAdmissionCombinesCapabilityCloseErrors(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	closeFailure := errors.New("capability close failed")
	callbackFailure := errors.New("callback failed")
	for _, test := range []struct {
		name     string
		work     func(context.Context, *LocalVMRuntime) error
		wantCode nkderr.Code
		wantWork error
	}{
		{name: "successful work", work: func(context.Context, *LocalVMRuntime) error { return nil }, wantCode: nkderr.CodeEngineCapability},
		{name: "raw work error", work: func(context.Context, *LocalVMRuntime) error { return callbackFailure }, wantCode: nkderr.CodeBuildFailed, wantWork: callbackFailure},
		{name: "coded work error", work: func(context.Context, *LocalVMRuntime) error {
			return nkderr.Wrap(nkderr.CodeFetchFailed, callbackFailure, "fetching build input")
		}, wantCode: nkderr.CodeFetchFailed, wantWork: callbackFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := testVMLease(t, testVMFacts(false))
			lease.closeErr = closeFailure
			admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
			if err != nil {
				t.Fatal(err)
			}
			err = admission.Execute(t.Context(), request, test.work)
			if errorCode(err) != test.wantCode || !errors.Is(err, closeFailure) {
				t.Fatalf("execution error = %v, want %s wrapping close failure", err, test.wantCode)
			}
			if test.wantWork != nil && !errors.Is(err, test.wantWork) {
				t.Fatalf("execution error = %v, want wrapped callback failure", err)
			}
		})
	}
}

func TestLocalVMAdmissionCloseCodesCapabilityError(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	closeFailure := errors.New("capability close failed")
	lease := testVMLease(t, testVMFacts(false))
	lease.closeErr = closeFailure
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	err = admission.Close()
	if errorCode(err) != nkderr.CodeEngineCapability || !errors.Is(err, closeFailure) {
		t.Fatalf("close error = %v, want %s wrapping cause", err, nkderr.CodeEngineCapability)
	}
}

func TestLocalVMAdmissionPreservesPrimaryFailureWhenCleanupAlsoFails(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	primaryFailure := nkderr.New(nkderr.CodeUnavailable, "runtime observation unavailable")
	closeFailure := errors.New("capability close failed")
	for _, test := range []struct {
		name      string
		configure func(*fakeVMLease)
		execute   bool
		wantCode  nkderr.Code
	}{
		{name: "initial observation", configure: func(lease *fakeVMLease) {
			lease.observeErr = primaryFailure
		}, wantCode: nkderr.CodeUnavailable},
		{name: "fresh observation", configure: func(lease *fakeVMLease) {
			lease.reobserveErr = primaryFailure
		}, execute: true, wantCode: nkderr.CodeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := testVMLease(t, testVMFacts(false))
			lease.closeErr = closeFailure
			test.configure(lease)
			admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
			if test.execute && err == nil {
				err = admission.Execute(t.Context(), request, func(context.Context, *LocalVMRuntime) error {
					t.Fatal("work ran after failed fresh observation")
					return nil
				})
			}
			if errorCode(err) != test.wantCode || !errors.Is(err, primaryFailure) || !errors.Is(err, closeFailure) {
				t.Fatalf("error = %v, want %s wrapping primary and close failures", err, test.wantCode)
			}
		})
	}
}

func TestLocalVMAdmissionPreservesTimeoutWhenCleanupAlsoFails(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	closeFailure := errors.New("capability close failed")
	lease := testVMLease(t, testVMFacts(false))
	lease.closeErr = closeFailure
	admission, err := admitLocalVM(t.Context(), request, openLocalWorkspace(t), fakeVMObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err = admission.Execute(ctx, request, func(runCtx context.Context, _ *LocalVMRuntime) error {
		cancel()
		<-runCtx.Done()
		return runCtx.Err()
	})
	if errorCode(err) != nkderr.CodeTimeout || !errors.Is(err, context.Canceled) || !errors.Is(err, closeFailure) {
		t.Fatalf("error = %v, want %s wrapping cancellation and close failure", err, nkderr.CodeTimeout)
	}
}
