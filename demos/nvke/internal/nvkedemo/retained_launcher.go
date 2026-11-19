// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
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

// RetainedInstructions is an owner-issued presentation, not an ownership or
// readiness receipt. Presenters keep it separate from evictable activity logs.
type RetainedInstructions struct{ text string }

func (instructions RetainedInstructions) Text() string { return instructions.text }

// RetainedInstructionsWriter receives complete commands for replay after the
// terminal is restored. The opaque value is issued only by this launcher.
type RetainedInstructionsWriter interface {
	WriteRetainedInstructions(RetainedInstructions) error
}

// launchRetainedNative transfers the deployment stack to the existing fixed
// service owner, not to an unobserved shell background job. Only its connected
// peer's detached Ready response permits a successful foreground return.
func launchRetainedNative(ctx context.Context, cfg Config) error {
	return launchRetainedNativeWithCommand(ctx, cfg, func(executable, session string, environment []string) *exec.Cmd {
		command := exec.Command(executable, "retained-owner", "--session", session)
		command.Env = environment
		return command
	})
}

func launchRetainedNativeWithCommand(ctx context.Context, cfg Config, commandFor func(string, string, []string) *exec.Cmd) (result error) {
	if ctx == nil || cfg.Cleanup || commandFor == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained native launch requires a context and the default retaining lifetime")
	}
	if err := checkRetainedPeerCapability(ctx); err != nil {
		return err
	}
	executable, err := admitNetworkProbeExecutable(ctx, cfg.DemoExecutable)
	if err != nil {
		return err
	}
	out, err := freshDestination(cfg.Out)
	if err != nil || !operatorArgument(out) {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained deployment requires a fresh absolute output selection")
	}
	cfg.Out, cfg.DemoExecutable = out, executable
	cached, err := (Runner{Progress: cfg.Progress}).authenticateSudo(ctx)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "retained owner requires the existing foreground authentication")
	}
	root, err := os.MkdirTemp(filepath.Dir(out), ".nvke-retained-")
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "allocate private retained owner selection")
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		deadline = time.Now().Add(8 * time.Hour)
	}
	if err := privateJSON(filepath.Join(root, retainedRequestFile), retainedLaunch{Version: 1, Config: cfg, CachedSudo: cached, WorkDeadline: deadline}); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "retain private native launch intent")
	}
	started := false
	released := false
	log, err := os.OpenFile(filepath.Join(root, "owner.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "open private retained owner diagnostics")
	}
	defer func() {
		if err := log.Close(); err != nil {
			result = errors.Join(result, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "close retained owner diagnostics"))
			if started {
				result = errors.Join(result, ErrRetainedControls)
			}
		}
	}()
	command := commandFor(executable, root, NativeEnvironment())
	isolateRetainedCommand(command, cached)
	command.Stdin = nil
	command.Stdout, command.Stderr = log, log // Direct files; no copy or Wait goroutine survives this frontend.
	if err := command.Start(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "start the fixed retained service owner")
	}
	var access OperatorAccess
	defer func() {
		if !started {
			return
		}
		result = errors.Join(result, writeRetainedNativeInstructions(cfg.Progress, executable, root, access))
		if result != nil {
			result = errors.Join(result, ErrRetainedControls)
		}
	}()
	defer func() {
		if started {
			// Even an unknown acknowledgement cannot authorize killing work that
			// may have started. Release is only local descriptor disposal, never
			// a claim of detached ownership, successful deployment or joined exit.
			if !released {
				if err := command.Process.Release(); err != nil {
					result = errors.Join(result, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "release retained owner's local process descriptor"))
				}
			}
			return
		}
		// No /start was dispatched. This exact unreaped child cannot have
		// admitted services; only here may the frontend kill and reap it.
		killErr := command.Process.Kill()
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			result = errors.Join(result, ErrUnjoined, killErr, command.Process.Release())
			return
		}
		waitErr := command.Wait()
		var exit *exec.ExitError
		if waitErr != nil && !errors.As(waitErr, &exit) {
			result = errors.Join(result, ErrUnjoined, waitErr)
		}
	}()
	handshake, cancel := context.WithTimeout(ctx, 30*time.Second)
	session, err := awaitRetainedNativeOwner(handshake, root, filepath.Join(out, "runtime"), command.Process.Pid)
	cancel()
	if err != nil {
		return err
	}
	// Set this before dispatch, not after an acknowledgement: a lost response
	// may follow admitted service work. Every subsequent error retains it.
	started = true
	state, err := session.request(ctx, http.MethodPost, "/start")
	if err == nil {
		// Interactive authentication keeps its terminal through privileged
		// startup; unattended owners already have an independent session.
		_, err = awaitRetainedNativeHeld(ctx, cfg.Progress, session, state)
	}
	if err != nil {
		// Frontend cancellation only aborts work. It never requests cleanup.
		// A bounded reconciliation may still authenticate a settled held owner;
		// otherwise returning unknown cannot justify killing or waiting on it.
		_, reconcileErr := reconcileRetainedNative(ctx, session, ctx.Err() != nil)
		return errors.Join(err, reconcileErr)
	}
	detachContext, stopDetach := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	state, err = session.request(detachContext, http.MethodPost, "/detach")
	stopDetach()
	if err != nil || !state.Detached {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(err, ErrRetainedControls), "retained owner acknowledgement is unknown; use the printed original session, do not restart or delete its resources")
	}
	if err := command.Process.Release(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "release authenticated detached owner's local process descriptor")
	}
	released = true
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "frontend ended; detached services remain owned, use the printed status or explicit cleanup command")
	}
	if state.FailureCode != "" {
		return nvkrerr.New(state.FailureCode, "native deployment failed; the original retained owner and resources remain available through the printed session commands")
	}
	if state.State != retainedReady {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "native deployment stopped; the original retained owner and resources remain available through the printed session commands")
	}
	access = state.Access.operatorAccess()
	if cfg.Progress != nil {
		if _, err := fmt.Fprintln(cfg.Progress, "PASS (retained): native site and tenant checks completed; the environment remains running until explicit cleanup."); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "present retained native result")
		}
	}
	return nil
}

func awaitRetainedNativeOwner(ctx context.Context, root, work string, pid int) (*RetainedSession, error) {
	for {
		if _, err := os.Lstat(filepath.Join(root, retainedDescriptionFile)); err == nil {
			description, err := readRetainedJSON[retainedControlDescription](ctx, filepath.Join(root, retainedDescriptionFile))
			// The owner creates this bounded file exclusively before writing it.
			// An incomplete publication never admits work; wait for a complete
			// schema within the same startup budget instead of failing a race.
			if err == nil && (description.PID != pid || description.Selector != root || description.Work != work) {
				return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained description differs from the actual unstarted child; no start request sent")
			}
			if err == nil {
				session := &RetainedSession{root: root, description: description}
				if _, err := session.request(ctx, http.MethodGet, "/state"); err != nil {
					return nil, err
				}
				return session, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained owner selection is unavailable; no start request sent")
		}
		if err := pause(ctx, 25*time.Millisecond); err != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained owner did not publish its authenticated endpoint before startup deadline")
		}
	}
}

func awaitRetainedNativeHeld(ctx context.Context, out io.Writer, session *RetainedSession, state retainedControlSnapshot) (retainedControlSnapshot, error) {
	var cursor uint64
	for {
		if err := relayRetainedProgress(out, &cursor, state.Progress); err != nil {
			return retainedControlSnapshot{}, err
		}
		if err := ctx.Err(); err != nil {
			return retainedControlSnapshot{}, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "foreground ended; the original owner retains its services")
		}
		switch state.State {
		case retainedReady, retainedPartial:
			return state, nil
		case retainedCleanupRequested:
			return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained owner received explicit cleanup before successful frontend completion")
		case retainedStarting:
		default:
			return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained owner supplied an unknown deployment state")
		}
		if err := pause(ctx, 200*time.Millisecond); err != nil {
			return retainedControlSnapshot{}, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "foreground wait ended; use the printed original session for status or explicit cleanup")
		}
		var err error
		state, err = session.request(ctx, http.MethodGet, "/state")
		if err != nil {
			return retainedControlSnapshot{}, err
		}
	}
}

func reconcileRetainedNative(ctx context.Context, session *RetainedSession, abort bool) (retainedControlSnapshot, error) {
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if abort {
		// A lost abort acknowledgement still leaves ownership unknown. The
		// subsequent authenticated held/detached observation is decisive.
		_, _ = session.request(bounded, http.MethodPost, "/abort")
	}
	state, err := session.request(bounded, http.MethodGet, "/state")
	if err == nil {
		state, err = awaitRetainedNativeHeld(bounded, nil, session, state)
	}
	if err == nil {
		state, err = session.request(bounded, http.MethodPost, "/detach")
		if err == nil && state.Detached {
			return state, nil
		}
	}
	return retainedControlSnapshot{}, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(err, ErrRetainedControls), "retained owner acknowledgement remains unknown; preserve the printed original session and its resources")
}

func writeRetainedNativeInstructions(out io.Writer, executable, root string, access OperatorAccess) error {
	if out == nil {
		return nil
	}
	var text bytes.Buffer
	fmt.Fprintf(&text, "Retained session: %s\nRetained status: %s status --session %s\nRetained cleanup: %s cleanup --session %s\n", root, operatorQuote(executable), operatorQuote(root), operatorQuote(executable), operatorQuote(root))
	if access.kubectl != "" {
		if err := access.WriteInstructions(&text); err != nil {
			return err
		}
	}
	if text.Len() > retainedControlLimit {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained instructions exceed their bounded presentation envelope")
	}
	if writer, ok := out.(RetainedInstructionsWriter); ok {
		if err := writer.WriteRetainedInstructions(RetainedInstructions{text: text.String()}); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "present complete retained session instructions")
		}
		return nil
	}
	return writeOperatorText(out, text.String())
}
