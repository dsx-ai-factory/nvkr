package catalogmaintenance

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

func TestRefreshPreservesTimeoutAfterRealPinCommit(t *testing.T) {
	cfg, promote := maintenanceFixture(t)
	promote("1.1.0")
	beforeLock := readFixture(t, "catalog/pins.lock.yaml")
	beforePlan := readFixture(t, ".github/nkd-run/plan.json")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := refresh(ctx, cfg, "test (deadbeef)", false, func(ctx context.Context, request catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		update, err := catalog.UpdateSourcePins(ctx, request)
		if err == nil {
			cancel()
		}
		return update, err
	})
	assertCode(t, err, e.CodeTimeout)
	if e.ExitCode(err) != 31 || !e.Retryable(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation cause/classification: %v", err)
	}
	if !strings.Contains(err.Error(), "pins committed but regeneration failed") || !strings.Contains(err.Error(), "retain the disposable checkout") {
		t.Fatalf("missing recovery context: %v", err)
	}
	if !result.Changed || result.Update.Changed != 3 || bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nkd-run/plan.json")) {
		t.Fatalf("cancellation lost the committed lock boundary: %+v", result)
	}
	lock, err := catalog.LoadLockContext(t.Context(), "catalog")
	if err != nil || lock.Pins["root"].Tag != "release/v1.1.0" {
		t.Fatalf("complete committed lock missing: %+v %v", lock, err)
	}
}

func TestRegenerationCheckPreservesOwnerError(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"canceled", e.CodeTimeout},
		{"drift", e.CodePipelineDrift},
		{"invalid definition", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, promote := maintenanceFixture(t)
			promote("1.1.0")
			beforeLock := readFixture(t, "catalog/pins.lock.yaml")
			beforePlan := readFixture(t, ".github/nkd-run/plan.json")
			update, err := catalog.UpdateSourcePins(t.Context(), catalog.SourcePinRequest{Directory: cfg.Catalog, Sources: cfg.Sources, SameRelease: cfg.SameRelease, RejectRepublishedVersions: true, Bump: true})
			if err != nil || update.Changed != 3 {
				t.Fatalf("pin transaction: %+v %v", update, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			digest, err := regenerate(ctx, cfg, "test (deadbeef)", func(ctx context.Context, definition, environment, version, engine, root string, check bool) (string, error) {
				calls++
				if check != (calls == 2) {
					t.Fatalf("unexpected generation/check order: call %d, check %v", calls, check)
				}
				if check {
					switch tc.name {
					case "canceled":
						cancel()
					case "drift":
						writeFixture(t, ".github/nkd-run/plan.json", []byte("tampered"))
					case "invalid definition":
						writeFixture(t, cfg.Definition, []byte("invalid Definition"))
					}
				}
				return lifecycle.GenerateFiles(ctx, definition, environment, version, engine, root, check)
			})
			assertCode(t, err, tc.code)
			if calls != 2 || digest != "" || !strings.Contains(err.Error(), "candidate regeneration did not reproduce") || !strings.Contains(err.Error(), "retain the disposable checkout") {
				t.Fatalf("failed final check became a candidate: digest=%s calls=%d error=%v", digest, calls, err)
			}
			if tc.code == e.CodeTimeout && (e.ExitCode(err) != 31 || !e.Retryable(err) || !errors.Is(err, context.Canceled)) {
				t.Fatalf("lost second-check cancellation classification: %v", err)
			}
			if tc.code == e.CodePipelineDrift && (e.ExitCode(err) != 51 || e.Retryable(err)) {
				t.Fatalf("drift was reclassified: %v", err)
			}
			if bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || bytes.Equal(beforePlan, readFixture(t, ".github/nkd-run/plan.json")) {
				t.Fatal("lost the separate committed pin/generated-file transactions")
			}
		})
	}
}
