package execution

import (
	"context"
	"errors"
	"os"
	"slices"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// ObservedHostFacts is point-in-time diagnostic data. It is not provider
// allocation proof: CPU, memory, and workspace capacity are not reserved by
// observing them. It cannot authorize execution. Its JSON tags are only for
// diagnostic rendering; this value is not canonical identity, manifest, or
// receipt input.
type ObservedHostFacts struct {
	Platform     HostPlatform     `json:"platform"`
	Resources    ResourceCapacity `json:"resources"`
	Capabilities []CapabilityName `json:"capabilities,omitempty"`
}

// ObserveLocalHost returns one point-in-time Linux host observation relative to
// the caller's already-open workspace directory. It duplicates that directory
// only for the duration of this call and closes every retained handle before
// returning. The result is diagnostic: it does not authenticate an Environment
// class or Plan binding and cannot authorize local, hosted, or qualified work.
func ObserveLocalHost(ctx context.Context, workspace *os.File) (ObservedHostFacts, error) {
	return observeLocalHost(ctx, workspace, systemLocalObserver{})
}

type localObservationLease interface {
	// Observe is single-use, regardless of whether the observation succeeds.
	Observe(context.Context) (ObservedHostFacts, error)
	Close() error
}

type localObserver interface {
	Open(context.Context, *os.File) (localObservationLease, error)
}

func observeLocalHost(
	ctx context.Context,
	workspace *os.File,
	observer localObserver,
) (ObservedHostFacts, error) {
	if err := localObservationContextError(ctx, "local host observation"); err != nil {
		return ObservedHostFacts{}, err
	}
	if workspace == nil {
		return ObservedHostFacts{}, e.New(e.CodeUsage,
			"local host observation requires a held workspace directory")
	}
	if observer == nil {
		return ObservedHostFacts{}, e.New(e.CodeInternal, "local host observation has no observer")
	}

	lease, err := observer.Open(ctx, workspace)
	if err != nil {
		return ObservedHostFacts{}, err
	}
	if lease == nil {
		return ObservedHostFacts{}, e.New(e.CodeInternal,
			"local host observer returned no observation lease")
	}

	observed, observeErr := lease.Observe(ctx)
	closeErr := lease.Close()
	if observeErr != nil {
		if closeErr != nil {
			var coded *e.Error
			if errors.As(observeErr, &coded) {
				return ObservedHostFacts{}, e.Wrap(coded.Code,
					errors.Join(observeErr, closeErr),
					"local host observation failed and closing its handles also failed")
			}
			return ObservedHostFacts{}, e.Wrap(e.CodeInternal,
				errors.Join(observeErr, closeErr),
				"local host observation failed and closing its handles also failed")
		}
		return ObservedHostFacts{}, observeErr
	}
	if closeErr != nil {
		return ObservedHostFacts{}, e.Wrap(e.CodeEngineCapability, closeErr,
			"closing local host observation handles")
	}
	if err := validateObservedHostFacts(observed); err != nil {
		return ObservedHostFacts{}, err
	}
	return cloneObservedHostFacts(observed), nil
}

func validateObservedHostFacts(observed ObservedHostFacts) error {
	if !supportedHostPlatform(observed.Platform) {
		return e.New(e.CodeEngineCapability,
			"local observer reported unsupported host platform %q", observed.Platform)
	}
	previous := CapabilityName("")
	for _, capability := range observed.Capabilities {
		if capability != CapabilityKVM {
			return e.New(e.CodeEngineCapability,
				"local observer reported unsupported capability %q", capability)
		}
		if previous != "" && capability <= previous {
			return e.New(e.CodeEngineCapability,
				"local observer capabilities are not sorted and duplicate-free")
		}
		previous = capability
	}
	return nil
}

func cloneObservedHostFacts(observed ObservedHostFacts) ObservedHostFacts {
	observed.Capabilities = slices.Clone(observed.Capabilities)
	return observed
}

func localObservationContextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}
