// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// SupervisorPeer holds an observation of the process connected to a Unix
// socket, obtained from the kernel rather than a serialized PID. It is not a
// readiness receipt, attempt-membership proof or permission to signal a process.
// A future supervisor protocol must authenticate its own exact session too.
// A SupervisorPeer owns a descriptor and mutex and must not be value-copied.
type SupervisorPeer struct {
	mu   sync.Mutex
	file *os.File
	pid  int
	uid  uint32
}

// ObserveSupervisorPeer checks the current user's actual connected peer against
// an independently owned child's expected PID. Linux SO_PEERPIDFD supplies the
// handle to that peer: no pidfd_open race or numeric-PID signaling is used.
// expectedPID is a selection description, not evidence of ownership by itself.
func ObserveSupervisorPeer(ctx context.Context, connection *net.UnixConn, expectedPID int) (*SupervisorPeer, error) {
	if ctx == nil || connection == nil || expectedPID <= 0 {
		return nil, nkderr.New(nkderr.CodeUsage, "supervisor peer observation requires a context, connected socket and expected child")
	}
	if err := ctx.Err(); err != nil {
		return nil, nkderr.Wrap(nkderr.CodeTimeout, err, "supervisor peer observation canceled")
	}
	return observeSupervisorPeer(ctx, connection, expectedPID)
}

// CheckSupervisorPeerCapability exercises the required native kernel mechanism
// before expensive work. It neither opens a deployment nor writes any output.
func CheckSupervisorPeerCapability(ctx context.Context) error {
	if ctx == nil {
		return nkderr.New(nkderr.CodeUsage, "supervisor capability check requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nkderr.Wrap(nkderr.CodeTimeout, err, "supervisor capability check canceled")
	}
	return checkSupervisorPeerCapability(ctx)
}

// PID and UID describe the kernel observation only. They cannot reconstruct a
// SupervisorPeer or confer deletion, signaling or restoration authority.
func (peer *SupervisorPeer) PID() int {
	if peer == nil {
		return 0
	}
	return peer.pid
}

func (peer *SupervisorPeer) UID() uint32 {
	if peer == nil {
		return 0
	}
	return peer.uid
}

// WaitExit observes this same process's termination. It does not establish zero
// exit, child/group joining, cleanup, readiness or artifact verification. The
// owning supervisor must retain and join its actual worker to obtain those facts.
func (peer *SupervisorPeer) WaitExit(ctx context.Context) error {
	if peer == nil || ctx == nil {
		return nkderr.New(nkderr.CodeUsage, "supervisor exit observation requires a peer and bounded context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nkderr.New(nkderr.CodeUsage, "supervisor exit observation requires a deadline")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nkderr.Wrap(nkderr.CodeTimeout, err, "supervisor exit not observed within the caller's budget")
		}
		peer.mu.Lock()
		if peer.file == nil {
			peer.mu.Unlock()
			return nkderr.New(nkderr.CodeUsage, "supervisor peer observation is closed or unissued")
		}
		// Close and poll serialize on the held descriptor. Closing during poll
		// cannot substitute an unrelated file that reuses its descriptor number.
		exited, err := pollSupervisorPeer(peer.file, 50*time.Millisecond)
		peer.mu.Unlock()
		if err != nil {
			return nkderr.Wrap(nkderr.CodePreflightFailed, err, "observe bound supervisor exit")
		}
		if err := ctx.Err(); err != nil {
			return nkderr.Wrap(nkderr.CodeTimeout, err, "supervisor exit not observed within the caller's budget")
		}
		if exited {
			return nil
		}
	}
}

// Close releases only this observation handle, never the observed process.
func (peer *SupervisorPeer) Close() error {
	if peer == nil {
		return nil
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.file == nil {
		return nil
	}
	err := peer.file.Close()
	peer.file = nil
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "close supervisor observation")
	}
	return nil
}
