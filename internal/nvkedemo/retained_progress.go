// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"sync"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const retainedProgressLimit = 32 << 10

// Frames contain only the existing public progress projection, never captured
// subprocess diagnostics. Sequence gaps are visible errors, not hidden stalls.
type retainedProgressFrame struct {
	Sequence    uint64         `json:"sequence"`
	Text        string         `json:"text,omitempty"`
	Event       *ProgressEvent `json:"event,omitempty"`
	Status      string         `json:"operatorStatus,omitempty"`
	Walkthrough bool           `json:"walkthrough,omitempty"`
}

type retainedProgress struct {
	mu       sync.Mutex
	sequence uint64
	queue    []retainedProgressFrame
	sizes    []int
	bytes    int
	cached   bool
}

func (progress *retainedProgress) append(frame retainedProgressFrame) bool {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.sequence++
	frame.Sequence = progress.sequence
	raw, err := json.Marshal(frame)
	if err != nil || len(raw)+1 > retainedProgressLimit {
		// Deliberately leave a detectable sequence gap. Presentation cannot
		// silently skip an observation or substitute a health claim.
		return false
	}
	progress.queue = append(progress.queue, frame)
	progress.sizes = append(progress.sizes, len(raw)+1)
	progress.bytes += len(raw) + 1
	for progress.bytes > retainedProgressLimit {
		progress.bytes -= progress.sizes[0]
		progress.queue[0] = retainedProgressFrame{}
		progress.queue, progress.sizes = progress.queue[1:], progress.sizes[1:]
	}
	return true
}

// Preserve the owner's public projection as one typed result across the
// authenticated private relay, not as generic subprocess-log fragments.
// This remains presentation only; neither a frame nor its sequence grants
// readiness, retention, artifact or cleanup authority.
func (progress *retainedProgress) WriteOperatorStatus(status OperatorStatus) error {
	if status.Text() == "" {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained cluster presentation requires an owner-generated observation")
	}
	if !progress.append(retainedProgressFrame{Status: status.Text(), Walkthrough: status.Walkthrough()}) {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained cluster observation exceeds its bounded presentation envelope")
	}
	return nil
}

func (*retainedProgress) OperatorStatusPageDuration() time.Duration { return 3 * time.Second }

func (progress *retainedProgress) Write(raw []byte) (int, error) {
	n := len(raw)
	for len(raw) != 0 {
		part := min(len(raw), 1024)
		progress.append(retainedProgressFrame{Text: string(raw[:part])})
		raw = raw[part:]
	}
	return n, nil
}

func (progress *retainedProgress) Report(event ProgressEvent) {
	progress.append(retainedProgressFrame{Event: &event})
}

func (progress *retainedProgress) frames() []retainedProgressFrame {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	return slices.Clone(progress.queue)
}

// The foreground authenticated before launch. This bridge can only validate its
// existing cache while attached; it cannot prompt, change policy or supply a
// password. Each protected command still independently passes sudo policy.
func (progress *retainedProgress) SudoAuthentication() func(context.Context) error {
	if !progress.cached {
		return nil
	}
	return func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(bounded, "sudo", "-n", "-v")
		cmd.Env = NativeEnvironment()
		if err := RunAuthenticationCommand(bounded, cmd); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "attached networking authorization unavailable; no password prompt started")
		}
		return nil
	}
}

func relayRetainedProgress(out io.Writer, cursor *uint64, frames []retainedProgressFrame) error {
	for _, frame := range frames {
		if frame.Walkthrough && frame.Status == "" {
			return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained walkthrough requires its typed observation")
		}
		if frame.Sequence <= *cursor {
			continue
		}
		if frame.Sequence != *cursor+1 {
			// The authenticated owner's bounded ring may evict old frames. This
			// is missing presentation, never permission to abort its deployment.
			if out != nil {
				const notice = "Warning: foreground missed bounded progress; resynchronized to available observations. This progress relay requested no restart or additional cleanup.\n"
				written, err := io.WriteString(out, notice)
				if err != nil || written != len(notice) {
					return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(err, io.ErrShortWrite), "present retained progress gap")
				}
			}
			*cursor = frame.Sequence - 1
		}
		if frame.Status != "" {
			if frame.Text != "" || frame.Event != nil || len(frame.Status) > retainedProgressLimit {
				return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained cluster observation has an invalid presentation envelope")
			}
			if out != nil {
				if err := writeOperatorStatus(out, OperatorStatus{text: frame.Status, walkthrough: frame.Walkthrough}); err != nil {
					return err
				}
			}
		}
		if frame.Event != nil {
			reportProgress(out, *frame.Event)
		}
		if frame.Text != "" && out != nil {
			written, err := io.WriteString(out, frame.Text)
			if err != nil || written != len(frame.Text) {
				return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(err, io.ErrShortWrite), "present retained public progress")
			}
		}
		*cursor = frame.Sequence
	}
	return nil
}
