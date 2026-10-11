// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"golang.org/x/sys/unix"
)

const tenantCleanupObservationLimit = 256 << 10

// These private descriptions are diagnostics, not deletion receipts. Keeping
// three slots retains the last poll without accumulating files during teardown.
type tenantCleanupObservation struct {
	tenantCleanupCommandStatus
	Truncated bool   `json:"truncated"`
	Output    []byte `json:"output"`
}

type tenantCleanupCommandStatus struct {
	Sequence int    `json:"sequence"`
	Phase    string `json:"phase"`
	ExitCode int    `json:"exitCode"`
	Status   string `json:"status"`
}

type tenantCleanupObservations struct {
	Initial     *tenantCleanupObservation   `json:"initialInventory,omitempty"`
	Delete      *tenantCleanupObservation   `json:"delete,omitempty"`
	Latest      *tenantCleanupObservation   `json:"latestInventory,omitempty"`
	LastCommand *tenantCleanupCommandStatus `json:"lastCommand,omitempty"`
}

func (o *tenantCleanupObservations) capture(ctx context.Context, runner Runner, args []string, phase string, sequence int, budget time.Duration) ([]byte, error) {
	// Persistence must never prevent withdrawal when the disk is full or the
	// workspace is unavailable. Reuse the existing bounded, joined command owner.
	runner.Work, runner.Progress = "", nil
	sink := &boundedDiagnostic{remaining: tenantCleanupObservationLimit, capture: true}
	runner.output = sink
	raw, err := runner.Capture(ctx, args, fmt.Sprintf("tenant-%03d-%s", sequence, phase), budget)
	combined, retentionErr := sink.result()
	status := tenantCleanupCommandStatus{Sequence: sequence, Phase: phase, ExitCode: CommandExitCode(err), Status: "failed"}
	switch {
	case errors.Is(err, ErrUnjoined):
		status.Status = "unjoined"
	case errors.Is(err, context.DeadlineExceeded):
		status.Status = "deadline-exceeded"
	case errors.Is(err, context.Canceled):
		status.Status = "canceled"
	case err == nil:
		status.Status, status.ExitCode = "complete", 0
	}
	o.LastCommand = &status
	observation := &tenantCleanupObservation{tenantCleanupCommandStatus: status, Truncated: retentionErr != nil, Output: combined}
	switch phase {
	case "inventory":
		if o.Initial == nil {
			o.Initial = observation
		}
		// An expired context before command launch has no newer inventory bytes.
		// Retain the preceding response and the separate final-command status.
		if len(combined) > 0 || o.Latest == nil {
			o.Latest = observation
		}
	case "delete":
		o.Delete = observation
	}
	return raw, err
}

// preserve is best-effort and exclusive: it neither creates a missing workspace
// nor replaces another observation. The held private owner prevents path swaps
// from redirecting the write. Raw command bytes never reach the progress writer.
func (o *tenantCleanupObservations) preserve(ctx context.Context, work string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if o.Initial == nil && o.Delete == nil && o.Latest == nil {
		return nil
	}
	directory, info, err := securepath.OpenDirectory(work)
	if err != nil {
		return err
	}
	defer directory.Close()
	if info.Mode().Perm() != 0o700 || !currentOwner(info) {
		return errors.New("cleanup observations require a private owned workspace")
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if err = requireContext(ctx); err != nil {
		return err
	}
	const name = "tenant-cleanup-observations.json"
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := file.Write(raw)
	return errors.Join(writeErr, file.Close())
}
