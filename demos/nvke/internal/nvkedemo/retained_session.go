// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

func retainedReadyTime(checked time.Time) string {
	if checked.IsZero() {
		return "not observed"
	}
	return checked.UTC().Format(time.RFC3339) + " (historical observation)"
}

func (description retainedAccessDescription) operatorAccess() OperatorAccess {
	return OperatorAccess{kubectl: description.Kubectl, site: description.Site, tenant: description.Tenant, uiService: description.UIService, uiPort: description.UIPort, uiHTTPObserved: description.UIHTTPObserved, siteQCOW: description.SiteQCOW}
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
	before, err := session.request(ctx, http.MethodGet, "/state")
	if err != nil {
		return err
	}
	// Select the presentation boundary before requesting cleanup, so deployment
	// history is skipped but even cleanup frames emitted before its acknowledgment
	// are relayed. This cursor grants no cleanup or completion authority.
	cursor := retainedCleanupCursor(before.Progress)
	cleanup, err := session.request(ctx, http.MethodPost, "/cleanup")
	if err != nil {
		return err
	}
	if err := relayRetainedProgress(progress, &cursor, cleanup.Progress); err != nil {
		return err
	}
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

func retainedCleanupCursor(frames []retainedProgressFrame) uint64 {
	if len(frames) == 0 {
		return 0
	}
	return frames[len(frames)-1].Sequence
}

func retainedTeardownFailure(root string) error {
	return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, ErrRetainedControls, "retained teardown returned with failure; cleanup and restored controls are NOT confirmed; inspect private diagnostics %s and %s; do not restore while producers remain", operatorQuote(filepath.Join(root, "owner-return.log")), operatorQuote(filepath.Join(root, "owner-error.log")))
}
