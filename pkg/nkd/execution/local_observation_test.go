package execution

import (
	"context"
	"errors"
	"os"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type fakeLocalObserver struct {
	lease localObservationLease
	err   error
}

func (observer fakeLocalObserver) Open(context.Context, *os.File) (localObservationLease, error) {
	if observer.err != nil {
		return nil, observer.err
	}
	return observer.lease, nil
}

type fakeLocalLease struct {
	facts      ObservedHostFacts
	observeErr error
	closeErr   error
	reads      int
	closed     bool
}

func (lease *fakeLocalLease) Observe(context.Context) (ObservedHostFacts, error) {
	lease.reads++
	return lease.facts, lease.observeErr
}

func (lease *fakeLocalLease) Close() error {
	lease.closed = true
	return lease.closeErr
}

func localObservationFacts(kvm bool) ObservedHostFacts {
	facts := ObservedHostFacts{
		Platform: HostPlatformLinuxAMD64,
		Resources: ResourceCapacity{
			CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
		},
	}
	if kvm {
		facts.Capabilities = []CapabilityName{CapabilityKVM}
	}
	return facts
}

func openLocalWorkspace(t *testing.T) *os.File {
	t.Helper()
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := workspace.Close(); err != nil {
			t.Error(err)
		}
	})
	return workspace
}

func isZeroObservedHostFacts(facts ObservedHostFacts) bool {
	return facts.Platform == "" && facts.Resources == (ResourceCapacity{}) && facts.Capabilities == nil
}

func TestObserveLocalHostReturnsDiagnosticFactsAndClosesHandles(t *testing.T) {
	lease := &fakeLocalLease{facts: localObservationFacts(true)}
	facts, err := observeLocalHost(context.Background(), openLocalWorkspace(t),
		fakeLocalObserver{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	if lease.reads != 1 || !lease.closed {
		t.Fatalf("reads = %d, closed = %t; want one read and closed handles", lease.reads, lease.closed)
	}
	if facts.Platform != HostPlatformLinuxAMD64 || facts.Resources.MemoryMiB != 8192 ||
		len(facts.Capabilities) != 1 || facts.Capabilities[0] != CapabilityKVM {
		t.Fatalf("unexpected observed facts: %#v", facts)
	}
	facts.Capabilities[0] = "changed"
	if lease.facts.Capabilities[0] != CapabilityKVM {
		t.Fatal("returned diagnostic facts alias observer-owned data")
	}
}

func TestObserveLocalHostRejectsInvalidFacts(t *testing.T) {
	for _, test := range []struct {
		name  string
		facts ObservedHostFacts
	}{
		{name: "missing platform", facts: ObservedHostFacts{}},
		{name: "unknown capability", facts: ObservedHostFacts{
			Platform: HostPlatformLinuxAMD64, Capabilities: []CapabilityName{"unknown"},
		}},
		{name: "duplicate capability", facts: ObservedHostFacts{
			Platform: HostPlatformLinuxAMD64, Capabilities: []CapabilityName{CapabilityKVM, CapabilityKVM},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := &fakeLocalLease{facts: test.facts}
			facts, err := observeLocalHost(context.Background(), openLocalWorkspace(t),
				fakeLocalObserver{lease: lease})
			if !isZeroObservedHostFacts(facts) || errorCode(err) != nkderr.CodeEngineCapability {
				t.Fatalf("facts = %#v, error = %v; want %s", facts, err, nkderr.CodeEngineCapability)
			}
			if !lease.closed {
				t.Fatal("invalid observation did not close its handles")
			}
		})
	}
}

func TestObserveLocalHostFailsClosed(t *testing.T) {
	observerError := nkderr.New(nkderr.CodeEngineCapability, "observer unavailable")
	observeError := nkderr.New(nkderr.CodeTimeout, "observation canceled")
	rawObserveError := errors.New("raw observation failed")
	closeError := errors.New("close failed")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, test := range []struct {
		name      string
		ctx       context.Context
		workspace *os.File
		observer  localObserver
		wantCode  nkderr.Code
		wantErr   error
		wantAlso  error
	}{
		{name: "nil context", workspace: openLocalWorkspace(t), observer: fakeLocalObserver{}, wantCode: nkderr.CodeUsage},
		{name: "canceled context", ctx: canceled, workspace: openLocalWorkspace(t), observer: fakeLocalObserver{}, wantCode: nkderr.CodeTimeout},
		{name: "nil workspace", ctx: context.Background(), observer: fakeLocalObserver{}, wantCode: nkderr.CodeUsage},
		{name: "nil observer", ctx: context.Background(), workspace: openLocalWorkspace(t), wantCode: nkderr.CodeInternal},
		{name: "observer failure", ctx: context.Background(), workspace: openLocalWorkspace(t), observer: fakeLocalObserver{err: observerError}, wantCode: nkderr.CodeEngineCapability, wantErr: observerError},
		{name: "nil lease", ctx: context.Background(), workspace: openLocalWorkspace(t), observer: fakeLocalObserver{}, wantCode: nkderr.CodeInternal},
		{name: "observation failure", ctx: context.Background(), workspace: openLocalWorkspace(t), observer: fakeLocalObserver{lease: &fakeLocalLease{observeErr: observeError}}, wantCode: nkderr.CodeTimeout, wantErr: observeError},
		{name: "close failure", ctx: context.Background(), workspace: openLocalWorkspace(t), observer: fakeLocalObserver{lease: &fakeLocalLease{facts: localObservationFacts(false), closeErr: closeError}}, wantCode: nkderr.CodeEngineCapability, wantErr: closeError},
		{name: "observation and close failure", ctx: context.Background(), workspace: openLocalWorkspace(t), observer: fakeLocalObserver{lease: &fakeLocalLease{observeErr: observeError, closeErr: closeError}}, wantCode: nkderr.CodeTimeout, wantErr: observeError, wantAlso: closeError},
		{name: "uncoded observation and close failure", ctx: context.Background(), workspace: openLocalWorkspace(t), observer: fakeLocalObserver{lease: &fakeLocalLease{observeErr: rawObserveError, closeErr: closeError}}, wantCode: nkderr.CodeInternal, wantErr: rawObserveError, wantAlso: closeError},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := observeLocalHost(test.ctx, test.workspace, test.observer)
			if !isZeroObservedHostFacts(facts) || errorCode(err) != test.wantCode {
				t.Fatalf("facts = %#v, error = %v; want %s", facts, err, test.wantCode)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want wrapped %v", err, test.wantErr)
			}
			if test.wantAlso != nil && !errors.Is(err, test.wantAlso) {
				t.Fatalf("error = %v, want also wrapped %v", err, test.wantAlso)
			}
		})
	}
}
