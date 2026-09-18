//go:build !linux

package execution

import (
	"context"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestLocalHostObservationFailsClosedOutsideLinux(t *testing.T) {
	facts, err := ObserveLocalHost(context.Background(), openLocalWorkspace(t))
	if !isZeroObservedHostFacts(facts) || errorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("facts = %#v, error = %v; want %s", facts, err, nkderr.CodeEngineCapability)
	}
}
