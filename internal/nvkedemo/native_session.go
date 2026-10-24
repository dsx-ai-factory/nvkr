// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// NativeSite is a live, callback-scoped native site. Its zero value and copies
// retained after WithNativeSite returns cannot operate on resources. Operations
// are serialized; only one tenant may be active on this private site at a time.
type NativeSite struct{ scope *nativeSiteScope }

// NativeTenant identifies one tenant created by its NativeSite. Copies share
// its lifetime; Delete invalidates all of them without stopping the site.
type NativeTenant struct{ state *nativeTenantState }

type nativeTenantState struct {
	scope *nativeSiteScope
	out   string
	ready bool
}

type nativeSiteScope struct {
	// A single permit serializes operations while allowing a caller waiting
	// behind a long suite to cancel without waiting for that suite to finish.
	gate    chan struct{}
	ctx     context.Context
	attempt *nativeSiteAttempt
	runner  Runner
	tools   string
	out     string
	active  *nativeTenantState
	closed  bool
}

// WithNativeSite verifies the completed Run outputs selected by cfg, prepares the
// source-owned site once, then calls use. CreateTenant, Go tests and conformance
// can share that site. Scope exit invokes the existing cleanup owner even on
// failure or cancellation; it preserves dependent resources when child join
// cannot be confirmed. cfg.Cleanup does not detach this library scope.
//
// This internal downstream API grants no Release or qualification authority.
// Host prerequisites, privilege admission and DemoExecutable are the same as
// DeployArtifacts; tests need the documented dedicated native host controls.
func WithNativeSite(ctx context.Context, cfg Config, use func(context.Context, *NativeSite) error) error {
	if ctx == nil || use == nil {
		return configUsage("native site requires a context and lifecycle callback")
	}
	selected, err := nativeDeploymentConfig(ctx, cfg, runtime.GOOS+"/"+runtime.GOARCH)
	if err == nil {
		err = runNativeSite(ctx, selected, func(ctx context.Context, site *NativeSite) error {
			return errors.Join(use(ctx, site), ctx.Err())
		})
	}
	return nativeSessionError(err, "native site scope")
}

func withNativeSiteScope(ctx context.Context, attempt *nativeSiteAttempt, runner Runner, tools, out string, use func(context.Context, *NativeSite) error) (result error) {
	scopeCtx, cancel := context.WithCancel(ctx)
	scope := &nativeSiteScope{gate: make(chan struct{}, 1), ctx: scopeCtx, attempt: attempt, runner: runner, tools: tools, out: out}
	defer func() {
		if holdErr := waitRetainedCleanup(ctx, result); holdErr != nil {
			result = errors.Join(result, holdErr)
			return
		}
		// Cancel calls that use an independent caller context before waiting for
		// their subprocess owners to join. Outer guest cleanup starts afterward.
		cancel()
		scope.gate <- struct{}{}
		scope.closed = true
		if attempt.unjoinedWork != nil && !errors.Is(result, attempt.unjoinedWork) {
			result = errors.Join(result, attempt.unjoinedWork)
		}
		<-scope.gate
	}()
	defer recoverRetainedPanic(ctx, &result)
	return use(scopeCtx, &NativeSite{scope: scope})
}

// Kubeconfig returns a private locator for tests, not a readiness receipt.
func (s *NativeSite) Kubeconfig(ctx context.Context) (string, error) {
	var path string
	err := s.operate(ctx, nil, func(context.Context) error {
		path = filepath.Join(s.scope.out, "site.kubeconfig")
		return nil
	})
	return path, err
}

// CreateTenant runs the existing source-owned tenant creation, real node and
// cloud-init readiness, and single workload/DNS checks. It leaves the tenant
// available for further Go tests or explicit RunConformance until Delete or
// scope exit. Every tenant has a fresh private diagnostic directory.
//
// cfg.TenantWorkers on WithNativeSite selects one or two workers independently
// of conformance. Zero keeps one worker ordinarily or two for conformance.
// A failed creation can return a handle for explicit deletion; it never
// authorizes a second creation until deletion completes. NVKE owns tenant-name/schema validation.
func (s *NativeSite) CreateTenant(ctx context.Context, name string) (*NativeTenant, error) {
	return s.createTenant(ctx, name, "")
}

func (s *NativeSite) createTenant(ctx context.Context, name, out string) (*NativeTenant, error) {
	var selected *NativeTenant
	err := s.operate(ctx, nil, func(ctx context.Context) error {
		if !configString(name) || strings.HasPrefix(name, "-") {
			return configUsage("tenant requires a non-option name")
		}
		scope := s.scope
		if scope.active != nil || scope.attempt.tenantOwner != nil {
			return configUsage("delete the active tenant before creating another on this site")
		}
		request, err := scope.attempt.namedTenantRequest(ctx, name)
		if err != nil {
			return err
		}
		if out == "" {
			out, err = os.MkdirTemp(scope.out, "tenant-")
			if err != nil {
				return err
			}
			if err := copyPrivate(ctx, filepath.Join(scope.out, "site.kubeconfig"), filepath.Join(out, "site.kubeconfig"), 0o600); err != nil {
				return err
			}
		}
		selected = &NativeTenant{state: &nativeTenantState{scope: scope, out: out}}
		scope.active = selected.state
		runner := scope.runner
		runner.Work = out
		stage := beginProgress(runner.Progress, "tenant-provisioning", DeploymentStarted, DeploymentFinished, DeploymentFailed)
		err = scope.attempt.runTenantRequest(ctx, runner, scope.attempt.assets.CLI, scope.tools, out, filepath.Join(scope.out, "site.kubeconfig"), request)
		stage.finish(err)
		selected.state.ready = err == nil
		return err
	})
	return selected, err
}

// Kubeconfig returns the accepted tenant's private access locator. It does not
// continuously guarantee cluster health; subsequent tests query the actual API.
func (t *NativeTenant) Kubeconfig(ctx context.Context) (string, error) {
	var path string
	err := t.operate(ctx, true, func(context.Context) error {
		path = filepath.Join(t.state.out, "tenant.kubeconfig")
		return nil
	})
	return path, err
}

// RunConformance invokes the existing NVKR validation owner against this tenant.
// out must be fresh so each explicit attempt preserves its original results.
// Failure never deletes the tenant; callers may inspect it and rerun the suite
// with a different output directory while the same site scope remains open.
func (t *NativeTenant) RunConformance(ctx context.Context, out string) error {
	return t.operate(ctx, true, func(ctx context.Context) error {
		if t.state.scope.attempt.conformance == nil {
			return configUsage("site configuration must select conformance artifacts before deployment")
		}
		root, err := freshDestination(out)
		if err != nil {
			return err
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			return err
		}
		if err := copyPrivate(ctx, filepath.Join(t.state.out, "tenant.kubeconfig"), filepath.Join(root, "tenant.kubeconfig"), 0o600); err != nil {
			return err
		}
		return t.conformance(ctx, root)
	})
}

// CLI presentation retains its existing report location; both entry points
// delegate to the same validation owner without rebuilding or deleting guests.
func (t *NativeTenant) runConformance(ctx context.Context, out string) error {
	return t.operate(ctx, true, func(ctx context.Context) error { return t.conformance(ctx, out) })
}

func (t *NativeTenant) conformance(ctx context.Context, out string) error {
	scope := t.state.scope
	runner := scope.runner
	runner.Work = out
	return scope.attempt.runConformance(ctx, runner, scope.tools, out)
}

// Delete waits for NVKE's owned tenant deletion and preserves private cleanup
// evidence. The site stays available for another tenant. Failure preserves the
// handle/ownership for inspection or another explicitly requested deletion;
// scope exit still performs the existing final cleanup.
func (t *NativeTenant) Delete(ctx context.Context) error {
	return t.operate(ctx, false, func(ctx context.Context) error {
		if err := t.state.scope.attempt.closeTenant(ctx); err != nil {
			return err
		}
		t.state.scope.attempt.tenantOwner = nil
		t.state.scope.attempt.nodes = nil
		t.state.scope.attempt.workload = nil
		t.state.scope.active = nil
		return nil
	})
}

func (t *NativeTenant) operate(ctx context.Context, ready bool, operation func(context.Context) error) error {
	if t == nil || t.state == nil || t.state.scope == nil {
		return configUsage("tenant requires a live site-issued handle")
	}
	return (&NativeSite{scope: t.state.scope}).operate(ctx, t.state, func(ctx context.Context) error {
		if ready && !t.state.ready {
			return configUsage("tenant creation and workload/DNS checks have not succeeded")
		}
		return operation(ctx)
	})
}

func (s *NativeSite) operate(ctx context.Context, tenant *nativeTenantState, operation func(context.Context) error) error {
	if ctx == nil || s == nil || s.scope == nil {
		return configUsage("operation requires a context and live site-issued handle")
	}
	scope := s.scope
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(scope.ctx, cancel)
	defer func() { stop(); cancel() }()
	select {
	case <-call.Done():
		return nativeSessionError(call.Err(), "native site operation")
	case scope.gate <- struct{}{}:
	}
	defer func() { <-scope.gate }()
	if scope.closed || tenant != nil && scope.active != tenant {
		return configUsage("native site or tenant scope has ended")
	}
	if scope.attempt.unjoinedWork != nil {
		return nativeSessionError(scope.attempt.unjoinedWork, "native site has unjoined work; preserve resources")
	}
	if err := scope.ctx.Err(); err != nil {
		return nativeSessionError(err, "native site operation")
	}
	if err := requireContext(call); err != nil {
		return nativeSessionError(err, "native site operation")
	}
	err := operation(call)
	if errors.Is(err, ErrUnjoined) && scope.attempt.unjoinedWork == nil {
		// This lifecycle failure cannot be swallowed by test callbacks. Outer
		// guest cleanup must retain dependent resources after lost child join.
		scope.attempt.unjoinedWork = err
	}
	return nativeSessionError(err, "native site operation")
}

func nativeSessionError(err error, operation string) error {
	if err == nil {
		return nil
	}
	code := nvkrerr.CodePreflightFailed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = nvkrerr.CodeTimeout
	} else {
		var coded *nvkrerr.Error
		if errors.As(err, &coded) {
			return err
		}
	}
	return nvkrerr.Wrap(code, err, "%s", operation)
}
