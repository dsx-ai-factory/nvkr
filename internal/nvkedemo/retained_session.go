// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// RetainedSession is a private locator plus a live endpoint selection, not
// persistent ownership, current Kubernetes health or permission to signal PIDs.
// Every operation reconnects and authenticates the same kernel peer/session.
type RetainedSession struct {
	root        string
	description retainedControlDescription
	child       *retainedChild // Kept until actual join or acknowledged Release.
}

func CheckRetentionCapability(ctx context.Context) error {
	if err := networkPlatform(); err != nil {
		return err
	}
	return errors.Join(CheckSupervisorPeerCapability(ctx))
}

// Retain launches only this executable's fixed child entry. Run admission,
// service ownership, Ready checks and teardown stay in that actual owner.
// No os/exec pipe copier or foreground Wait goroutine is abandoned on handoff.
func Retain(ctx context.Context, cfg Config) (*RetainedSession, error) {
	if err := CheckRetentionCapability(ctx); err != nil {
		return nil, err
	}
	if _, err := normalizeNetwork(cfg.Network); err != nil {
		return nil, err
	}
	if cfg.Timeout < MinTenantTimeoutSeconds*time.Second || cfg.Timeout > MaxTenantTimeoutSeconds*time.Second {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained run requires a valid tenant budget before launch")
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "select fresh retained workspace")
	}
	executable, err := admitNetworkProbeExecutable(ctx, cfg.DemoExecutable)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(filepath.Dir(out), ".nvke-retained-")
	if err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "create private retained session locator")
	}
	cfg.Out = out
	var cached bool
	if presenter, ok := cfg.Progress.(SudoAuthorizer); ok {
		cached = presenter.SudoAuthentication() != nil
	}
	if err := privateJSON(filepath.Join(root, retainedRequestFile), retainedLaunch{Version: 1, Config: cfg, CachedSudo: cached}); err != nil {
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "could not retain private launch intent; no child started")
	}
	log, err := os.OpenFile(filepath.Join(root, "owner.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "could not open private owner diagnostics; no child started")
	}
	defer log.Close()
	command := exec.Command(executable, "retained-owner", "--session", root)
	command.Env = NativeEnvironment()
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, log, log
	child, err := startRetainedChild(ctx, command)
	if err != nil {
		if child != nil {
			stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
			defer cancel()
			err = errors.Join(err, child.stopBeforeStart(stop))
		}
		return nil, err
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	description, err := awaitRetainedDescription(startup, root, out, child)
	cancel()
	if err != nil {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		return nil, errors.Join(err, child.stopBeforeStart(stop))
	}
	session := &RetainedSession{root: root, description: description, child: child}
	// Once /start is sent, even a lost acknowledgment may have started work.
	// Never use pre-start signaling or silently restart after that boundary.
	_, startErr := session.request(ctx, http.MethodPost, "/start")
	var cursor uint64
	aborted := false
	primary := startErr
	for {
		observation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if (ctx.Err() != nil || primary != nil) && !aborted {
			_, abortErr := session.request(observation, http.MethodPost, "/abort")
			primary = errors.Join(primary, ctx.Err(), abortErr)
			aborted = true
		}
		state, observeErr := session.request(observation, http.MethodGet, "/state")
		if observeErr == nil {
			if relayErr := relayRetainedProgress(cfg.Progress, &cursor, state.Progress); relayErr != nil {
				primary = errors.Join(primary, relayErr)
			}
		}
		cancel()
		if observeErr != nil {
			gone, exitErr := child.exited(context.WithoutCancel(ctx))
			if gone {
				join, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				return session, errors.Join(primary, observeErr, child.wait(join))
			}
			if exitErr != nil {
				return session, errors.Join(ErrUnjoined, primary, observeErr, exitErr)
			}
			// A transient RPC failure is neither process termination nor cleanup.
			if err := pause(context.WithoutCancel(ctx), 200*time.Millisecond); err != nil {
				return session, err
			}
			continue
		}
		if state.State == retainedReady || state.State == retainedPartial {
			handoff, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			ack, err := session.request(handoff, http.MethodPost, "/detach")
			cancel()
			if err != nil || !ack.Detached {
				primary = errors.Join(primary, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained detachment was not acknowledged; do not infer safe handoff"))
				_ = pause(context.WithoutCancel(ctx), 200*time.Millisecond)
				continue // Keep the actual child obligation until detach or exit.
			}
			if err := child.release(); err != nil {
				if child.released {
					return session, errors.Join(primary, err)
				}
				primary = errors.Join(primary, err)
				_ = pause(context.WithoutCancel(ctx), 200*time.Millisecond)
				continue
			}
			if state.State == retainedPartial {
				code := state.FailureCode
				if code == "" {
					code = nvkrerr.CodePreflightFailed
				}
				return session, errors.Join(nvkrerr.New(code, "deployment stopped before Ready; partial environment retained; inspect private owner-error.log and owner-panic.log in the session directory"), primary)
			}
			reportProgress(cfg.Progress, ProgressEvent{Kind: DeploymentFinished, Name: "local-demo", Status: "retained"})
			return session, localDemoError(primary)
		}
		_ = pause(context.WithoutCancel(ctx), 100*time.Millisecond)
	}
}

func awaitRetainedDescription(ctx context.Context, root, work string, child *retainedChild) (retainedControlDescription, error) {
	for {
		if err := ctx.Err(); err != nil {
			return retainedControlDescription{}, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained owner did not publish its endpoint")
		}
		description, err := readRetainedJSON[retainedControlDescription](ctx, filepath.Join(root, retainedDescriptionFile))
		if err == nil {
			if description.PID != child.command.Process.Pid || description.Work != work || description.Selector != root {
				return retainedControlDescription{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained endpoint differs from its actual child/workspace")
			}
			client, err := connectRetained(ctx, description)
			if err == nil {
				_, err = client.request(ctx, http.MethodGet, "/state")
				err = errors.Join(err, client.close())
			}
			return description, err
		}
		if gone, exitErr := child.exited(ctx); gone || exitErr != nil {
			return retainedControlDescription{}, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, exitErr, "retained owner exited before startup; inspect its private owner log")
		}
		if err := pause(ctx, 20*time.Millisecond); err != nil {
			return retainedControlDescription{}, err
		}
	}
}

func (session *RetainedSession) request(ctx context.Context, method, operation string) (result retainedControlSnapshot, resultErr error) {
	if session == nil || ctx == nil {
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodeUsage, "retained operation requires its session")
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := connectRetained(bounded, session.description)
	if err != nil {
		return retainedControlSnapshot{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, client.close()) }()
	return client.request(bounded, method, operation)
}

func openRetainedSession(ctx context.Context, root string) (*RetainedSession, error) {
	if err := requireContext(ctx); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeUsage, err, "retained selection requires a context")
	}
	directory, root, err := privateNetworkDirectory(root)
	if err != nil {
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "select the original private retained session directory")
	}
	defer directory.Close()
	description, err := readRetainedJSON[retainedControlDescription](ctx, filepath.Join(root, retainedDescriptionFile))
	if err != nil {
		return nil, err
	}
	if description.Selector != root {
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained record belongs to a different original session; no request sent")
	}
	session := &RetainedSession{root: root, description: description}
	if _, err := session.request(ctx, http.MethodGet, "/state"); err != nil {
		return nil, err
	}
	return session, nil
}

// WriteInstructions rechecks the live owner before describing retention. It is
// called after terminal restoration, never through the dashboard activity ring.
func (session *RetainedSession) WriteInstructions(ctx context.Context, executable string, out io.Writer) error {
	if session == nil || ctx == nil || out == nil || !operatorArgument(executable) || !filepath.IsAbs(executable) || !operatorArgument(session.root) || !filepath.IsAbs(session.root) {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained instructions require their issued session, context, executable and output")
	}
	if err := writeOperatorText(out, fmt.Sprintf("\nSession locators (not proof of Ready, retention or cleanup):\nDEMO=%s\nSESSION=%s\nWORK=%s\n# Observe this exact retained owner and freshly check its clusters\n\"$DEMO\" status --session \"$SESSION\"\n# Explicitly delete its tenant, join services and restore owned networking\n\"$DEMO\" cleanup --session \"$SESSION\"\nIf joined services have exited but networking recovery failed:\n\"$DEMO\" restore --work \"$WORK\"\nRestore independently authenticates the original journal and idle producers; it does not delete tenants, VMs or management resources.\n", operatorQuote(executable), operatorQuote(session.root), operatorQuote(session.description.Work))); err != nil {
		return err
	}
	state, err := session.request(ctx, http.MethodGet, "/state")
	if err != nil {
		return err
	}
	if !state.Detached || !operatorArgument(executable) || !filepath.IsAbs(executable) || out == nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "usable handoff requires a live detached owner and actual executable")
	}
	if err := writeOperatorText(out, fmt.Sprintf("\nEnvironment retained; it has NOT been cleaned up.\nState: %s. Last completed Ready check: %s.\nPrivate owner diagnostics: %s\nFailure diagnostics: %s\nPanic stack (if present): %s\n", state.State, retainedReadyTime(state.CheckedAt), operatorQuote(filepath.Join(session.root, "owner.log")), operatorQuote(filepath.Join(session.root, "owner-error.log")), operatorQuote(filepath.Join(session.root, "owner-panic.log")))); err != nil {
		return err
	}
	if state.State != retainedReady {
		return writeOperatorText(out, "No Ready access is promised for this partial environment. Inspect the private deployment logs before cleanup.\n")
	}
	access := state.Access.operatorAccess()
	// Always keep usable commands visible even if a fresh health check fails.
	// Neither observation failure nor writer loss cleans the environment up.
	instructionsErr := access.WriteInstructions(out)
	liveErr := access.WriteLiveStatus(ctx, Runner{Work: session.root, Env: NativeEnvironment()}, out)
	return errors.Join(instructionsErr, liveErr)
}

func retainedReadyTime(checked time.Time) string {
	if checked.IsZero() {
		return "not observed"
	}
	return checked.UTC().Format(time.RFC3339) + " (historical observation)"
}

func (description retainedAccessDescription) operatorAccess() OperatorAccess {
	return OperatorAccess{kubectl: description.Kubectl, site: description.Site, tenant: description.Tenant, uiService: description.UIService, uiPort: description.UIPort, uiHTTPObserved: description.UIHTTPObserved}
}

func RetainedStatus(ctx context.Context, root string, out io.Writer) error {
	if ctx == nil || out == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained status requires a context and output writer")
	}
	session, err := openRetainedSession(ctx, root)
	if err != nil {
		return err
	}
	state, err := session.request(ctx, http.MethodGet, "/state")
	if err != nil {
		return err
	}
	if err := writeOperatorText(out, fmt.Sprintf("Owner connected; state=%s; detached=%t; services joined=%t.\nLast completed Ready check: %s. Owner connection is not Kubernetes health.\n", state.State, state.Detached, state.Joined, retainedReadyTime(state.CheckedAt))); err != nil {
		return err
	}
	if state.State != retainedReady {
		return writeOperatorText(out, "No current Ready access is promised in this owner state.\n")
	}
	return state.Access.operatorAccess().WriteLiveStatus(ctx, Runner{Work: session.root, Env: NativeEnvironment()}, out)
}

// CleanupRetained authenticates the existing live owner, asks its actual Run
// stack to delete/join, waits for that owner to exit, and only then delegates
// root restoration to the original network owner with fresh caller authorization.
func CleanupRetained(ctx context.Context, root string, progress io.Writer) (resultErr error) {
	if err := requireContext(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeUsage, err, "retained cleanup requires a context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained cleanup requires a deadline before requesting work")
	}
	session, err := openRetainedSession(ctx, root)
	if err != nil {
		return err
	}
	if _, err := session.request(ctx, http.MethodPost, "/cleanup"); err != nil {
		return err
	}
	var cursor uint64
	for {
		state, err := session.request(ctx, http.MethodGet, "/state")
		if err != nil {
			return err
		}
		if err := relayRetainedProgress(progress, &cursor, state.Progress); err != nil {
			return err
		}
		if state.TeardownReturned && state.CleanupFailed && !state.OwnerExitSafe {
			return retainedTeardownFailure(session.root)
		}
		if state.Joined || state.TeardownReturned && state.CleanupFailed && state.OwnerExitSafe {
			client, err := connectRetained(ctx, session.description)
			if err != nil {
				return err
			}
			defer func() { resultErr = errors.Join(resultErr, client.close()) }()
			if _, err := client.request(ctx, http.MethodPost, "/finish"); err != nil {
				return err
			}
			if err := client.peer.WaitExit(ctx); err != nil {
				return err
			}
			if state.CleanupFailed {
				return retainedTeardownFailure(session.root)
			}
			if state.NetworkStarted {
				return Restore(ctx, session.description.Work, progress)
			}
			return nil // Actual owner never admitted a host-network guard.
		}
		if err := pause(ctx, 200*time.Millisecond); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained cleanup continues in its owner; rerun cleanup --session with the same session; host controls remain, do not restore while producers are active")
		}
	}
}

func retainedTeardownFailure(root string) error {
	return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, ErrRetainedControls, "retained teardown returned with failure; cleanup and restored controls are NOT confirmed; inspect private diagnostics %s and %s; do not restore while producers remain", operatorQuote(filepath.Join(root, "owner-return.log")), operatorQuote(filepath.Join(root, "owner-error.log")))
}
