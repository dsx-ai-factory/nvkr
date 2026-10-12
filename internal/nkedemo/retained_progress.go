// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"sync"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const retainedProgressLimit = 32 << 10

// Frames contain only the existing public progress projection, never captured
// subprocess diagnostics. Sequence gaps are visible errors, not hidden stalls.
type retainedProgressFrame struct {
	Sequence uint64         `json:"sequence"`
	Text     string         `json:"text,omitempty"`
	Event    *ProgressEvent `json:"event,omitempty"`
}

type retainedProgress struct {
	mu       sync.Mutex
	sequence uint64
	queue    []retainedProgressFrame
	sizes    []int
	bytes    int
	cached   bool
}

func (progress *retainedProgress) append(frame retainedProgressFrame) {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.sequence++
	frame.Sequence = progress.sequence
	raw, err := json.Marshal(frame)
	if err != nil || len(raw) > retainedProgressLimit {
		// Deliberately leave a detectable sequence gap. Presentation cannot
		// silently skip an observation or substitute a health claim.
		return
	}
	progress.queue = append(progress.queue, frame)
	progress.sizes = append(progress.sizes, len(raw)+1)
	progress.bytes += len(raw) + 1
	for progress.bytes > retainedProgressLimit {
		progress.bytes -= progress.sizes[0]
		progress.queue[0] = retainedProgressFrame{}
		progress.queue, progress.sizes = progress.queue[1:], progress.sizes[1:]
	}
}

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
			return nkderr.Wrap(nkderr.CodePreflightFailed, err, "attached networking authorization unavailable; no password prompt started")
		}
		return nil
	}
}

func relayRetainedProgress(out io.Writer, cursor *uint64, frames []retainedProgressFrame) error {
	for _, frame := range frames {
		if frame.Sequence <= *cursor {
			continue
		}
		if frame.Sequence != *cursor+1 {
			return nkderr.New(nkderr.CodePreflightFailed, "foreground missed bounded progress; services remain owned, not silently restarted")
		}
		if frame.Event != nil {
			reportProgress(out, *frame.Event)
		}
		if frame.Text != "" && out != nil {
			written, err := io.WriteString(out, frame.Text)
			if err != nil || written != len(frame.Text) {
				return nkderr.Wrap(nkderr.CodePreflightFailed, errors.Join(err, io.ErrShortWrite), "present retained public progress")
			}
		}
		*cursor = frame.Sequence
	}
	return nil
}
