// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// This is an actual directly spawned, unreaped child, not a serialized PID.
// The creation-time pidfd observes termination without a Wait goroutine that
// would be abandoned on handoff. Only a live authenticated detach permits Release.
type retainedChild struct {
	command  *exec.Cmd
	exit     *os.File
	released bool
}

func (child *retainedChild) exited(ctx context.Context) (bool, error) {
	if child == nil || child.exit == nil || child.command == nil || child.command.Process == nil {
		return false, nkderr.New(nkderr.CodeUsage, "retained child observation requires its actual spawn")
	}
	if err := requireContext(ctx); err != nil {
		return false, err
	}
	exited, err := pollSupervisorPeer(child.exit, 0)
	return exited, errors.Join(err, ctx.Err())
}

func (child *retainedChild) wait(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return nkderr.Wrap(nkderr.CodeUsage, err, "retained child join requires a context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nkderr.New(nkderr.CodeUsage, "retained child join requires a deadline")
	}
	for {
		exited, err := child.exited(ctx)
		if err != nil {
			return errors.Join(ErrUnjoined, err)
		}
		if exited {
			// Stdio consists only of actual files; no exec copy goroutine or
			// pipe can delay this sole Wait after kernel-confirmed termination.
			err := child.command.Wait()
			return errors.Join(err, child.exit.Close())
		}
		if err := pause(ctx, 20*time.Millisecond); err != nil {
			return errors.Join(ErrUnjoined, err)
		}
	}
}

func (child *retainedChild) stopBeforeStart(ctx context.Context) error {
	if ctx == nil || child == nil || child.command == nil || child.command.Process == nil || child.released {
		return nkderr.New(nkderr.CodeUsage, "pre-start stop requires its actual unreleased child and context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nkderr.New(nkderr.CodeUsage, "pre-start stop requires a deadline before signaling")
	}
	// Caller must not have sent /start. The fixed child entry starts no service
	// before that request. This actual exec handle is never recovered from a PID.
	signalErr := child.command.Process.Signal(os.Interrupt)
	if errors.Is(signalErr, os.ErrProcessDone) {
		signalErr = nil
	}
	if child.exit == nil {
		return errors.Join(signalErr, joinRetainedPrestart(ctx, child.command))
	}
	return errors.Join(signalErr, child.wait(ctx))
}
