// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type retainedCleanupProgressBuffer struct {
	retainedTypedStatusBuffer
	events []ProgressEvent
}

func (out *retainedCleanupProgressBuffer) Report(event ProgressEvent) {
	out.events = append(out.events, event)
}

func TestRetainedCleanupBoundarySkipsHistoryAndRelaysSubsequentFrames(t *testing.T) {
	for _, history := range []string{"empty", "evicted deployment", "repeated cleanup wait"} {
		t.Run(history, func(t *testing.T) {
			progress := &retainedProgress{}
			if history != "empty" {
				for range 400 {
					_, _ = progress.Write([]byte(strings.Repeat("deployment-history ", 40)))
				}
			}
			if history == "repeated cleanup wait" {
				progress.Report(ProgressEvent{Kind: PhaseStarted, Name: "previous-cleanup-wait"})
				_, _ = progress.Write([]byte("previous cleanup wait\n"))
			}
			before := progress.frames()
			cursor := retainedCleanupCursor(before)
			var out retainedCleanupProgressBuffer
			if err := relayRetainedProgress(&out, &cursor, before); err != nil || out.Len() != 0 || out.statuses != 0 || len(out.events) != 0 {
				t.Fatal("cleanup replayed its pre-request history", err)
			}
			// Cleanup may emit text, typed status and events before /cleanup
			// snapshots its acknowledgment. The pre-request cursor keeps all three.
			progress.Report(ProgressEvent{Kind: PhaseStarted, Name: "cleanup-start"})
			if err := progress.WriteOperatorStatus(OperatorStatus{text: "cleanup status\n"}); err != nil {
				t.Fatal(err)
			}
			_, _ = progress.Write([]byte("initial cleanup observation\n"))
			ack := progress.frames()
			if err := relayRetainedProgress(&out, &cursor, ack); err != nil || out.String() != "cleanup status\ninitial cleanup observation\n" || out.statuses != 1 || len(out.events) != 1 || out.events[0].Name != "cleanup-start" {
				t.Fatal("early cleanup acknowledgment lost activity or replayed deployment", err, out.String())
			}
			_, _ = progress.Write([]byte("new cleanup observation\n"))
			want := "cleanup status\ninitial cleanup observation\nnew cleanup observation\n"
			if err := relayRetainedProgress(&out, &cursor, progress.frames()); err != nil || out.String() != want {
				t.Fatal("post-boundary cleanup activity was lost or history replayed", err, out.String())
			}
			if err := relayRetainedProgress(&out, &cursor, progress.frames()); err != nil || out.String() != want || out.statuses != 1 || len(out.events) != 1 {
				t.Fatal("repeat cleanup poll replayed activity", err)
			}
		})
	}
}

func TestRetainedCleanupOverflowNoticeIsNeutralAndWriteFailuresSurvive(t *testing.T) {
	progress := &retainedProgress{}
	_, _ = progress.Write([]byte("old deployment\n"))
	cursor := retainedCleanupCursor(progress.frames())
	for range 400 {
		_, _ = progress.Write([]byte(strings.Repeat("cleanup activity ", 40)))
	}
	frames := progress.frames()
	var out bytes.Buffer
	if err := relayRetainedProgress(&out, &cursor, frames); err != nil || strings.Count(out.String(), "Warning: foreground missed") != 1 || strings.Contains(out.String(), "no restart or cleanup requested") || !strings.Contains(out.String(), "no restart or additional cleanup") {
		t.Fatal("overflow contradicted the cleanup request", err)
	}
	if err := relayRetainedProgress(&out, &cursor, frames); err != nil || strings.Count(out.String(), "Warning: foreground missed") != 1 {
		t.Fatal("cleanup gap warning repeated", err)
	}
	cursor = 1
	err := relayRetainedProgress(operatorShortWriter{}, &cursor, frames)
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || !errors.Is(err, io.ErrShortWrite) || cursor != 1 {
		t.Fatal("failed gap presentation was suppressed or advanced the cursor", err)
	}
}

func TestRetainedPartialFailurePreservesActualCausePrecedence(t *testing.T) {
	foreground := nvkrerr.New(nvkrerr.CodePreflightFailed, "foreground acknowledgment or writer failure")
	for _, row := range []struct {
		name          string
		primary       error
		ownerCode     nvkrerr.Code
		abort, cancel bool
		want          nvkrerr.Code
	}{
		{"start or plain writer failure", foreground, nvkrerr.CodeTimeout, true, false, nvkrerr.CodePreflightFailed},
		{"unacknowledged abort", foreground, nvkrerr.CodeTimeout, false, false, nvkrerr.CodeTimeout},
		{"genuine owner internal failure", foreground, nvkrerr.CodeInternal, true, false, nvkrerr.CodeInternal},
		{"genuine owner preflight failure", nvkrerr.New(nvkrerr.CodeInternal, "foreground defect"), nvkrerr.CodePreflightFailed, true, false, nvkrerr.CodePreflightFailed},
		{"owner budget deadline", nil, nvkrerr.CodeTimeout, false, false, nvkrerr.CodeTimeout},
		{"caller canceled", foreground, nvkrerr.CodeTimeout, true, true, nvkrerr.CodeTimeout},
		{"primary canceled", nvkrerr.Wrap(nvkrerr.CodeTimeout, context.Canceled, "foreground canceled"), nvkrerr.CodeTimeout, true, false, nvkrerr.CodeTimeout},
		{"primary deadline", nvkrerr.Wrap(nvkrerr.CodePreflightFailed, context.DeadlineExceeded, "foreground deadline"), nvkrerr.CodeTimeout, true, false, nvkrerr.CodeTimeout},
		{"uncoded foreground cause", errors.New("foreground failure"), nvkrerr.CodeTimeout, true, false, nvkrerr.CodeTimeout},
		{"unjoined foreground cause", errors.Join(foreground, ErrUnjoined), nvkrerr.CodeTimeout, true, false, nvkrerr.CodePreflightFailed},
		{"owner omitted code", foreground, "", false, false, nvkrerr.CodePreflightFailed},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if row.cancel {
				cancel()
			}
			err := retainedPartialFailure(ctx, row.primary, row.ownerCode, row.abort)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != row.want || nvkrerr.ExitCode(err) != nvkrerr.ExitCode(nvkrerr.New(row.want, "expected")) {
				t.Fatal("partial handoff changed the actual failure code", err)
			}
			if row.primary != nil && !errors.Is(err, row.primary) || row.cancel && !errors.Is(err, context.Canceled) || errors.Is(row.primary, ErrUnjoined) && !errors.Is(err, ErrUnjoined) {
				t.Fatal("partial handoff lost original cause or ownership obligation", err)
			}
		})
	}
}
