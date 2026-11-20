// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Outputs admits sinks whose writes cannot strand a namespace owner's cleanup.
// Files are inherited directly, so cancellation kills a child blocked on a pipe.
// Memory sinks are serialized together. Callers must not access them until the
// operation returns. Arbitrary callbacks cannot provide a bounded Write contract.
func Outputs(stdout, stderr io.Writer) (io.Writer, io.Writer, error) {
	mu := new(sync.Mutex)
	admit := func(writer io.Writer) (io.Writer, error) {
		if writer == nil || writer == io.Discard {
			return nil, nil
		}
		switch value := writer.(type) {
		case *os.File:
			if value != nil {
				return value, nil
			}
		case *Capture:
			if value != nil {
				return value, nil
			}
		case *memoryOutput:
			if value != nil {
				return value, nil
			}
		case *bytes.Buffer:
			if value != nil {
				return &memoryOutput{mu: mu, writer: value}, nil
			}
		case *strings.Builder:
			if value != nil {
				return &memoryOutput{mu: mu, writer: value}, nil
			}
		}
		return nil, e.New(e.CodeEngineCapability, "runtime output requires a file, memory buffer or bounded capture")
	}
	out, err := admit(stdout)
	if err != nil {
		return nil, nil, err
	}
	errout, err := admit(stderr)
	if err != nil {
		return nil, nil, err
	}
	return out, errout, nil
}

type memoryOutput struct {
	mu     *sync.Mutex
	writer io.Writer // Exact standard-library memory sink; never an arbitrary callback.
}

func (output *memoryOutput) Write(raw []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.writer.Write(raw)
}

// Capture retains a bounded prefix and records truncation without blocking the
// child or forwarding into a caller's writer. It is safe to share between streams.
type Capture struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func NewCapture(limit int) *Capture { return &Capture{limit: max(0, limit)} }

func (output *Capture) Write(raw []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - output.buffer.Len()
	_, _ = output.buffer.Write(raw[:min(len(raw), remaining)])
	output.overflow = output.overflow || len(raw) > remaining
	return len(raw), nil
}

func (output *Capture) Snapshot() (string, bool) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String(), output.overflow
}
