// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestChildOutputsShareMemorySynchronization(t *testing.T) {
	var buffer bytes.Buffer
	stdout, stderr, err := Outputs(&buffer, &buffer)
	if err != nil {
		t.Fatal(err)
	}
	out, err := prepareChildOutput(stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer out.close()
	errout, err := prepareChildOutput(stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer errout.close()
	var writers sync.WaitGroup
	for _, output := range []*childOutput{out, errout} {
		writers.Go(func() {
			if _, err := output.file.Write(bytes.Repeat([]byte("x"), 64<<10)); err != nil {
				t.Error(err)
			}
			output.started()
		})
	}
	writers.Wait()
	for _, output := range []*childOutput{out, errout} {
		if err := output.finish(); err != nil {
			t.Fatal(err)
		}
	}
	if buffer.Len() != 128<<10 {
		t.Fatalf("lost shared output: %d", buffer.Len())
	}
}

// These parent-side pipe ownership paths also run under -race; unlike namespace
// admission they do not require executing a static test binary as PID 1.
func TestChildOutputCaptureLifetime(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "start failure", true: "complete"}[started], func(t *testing.T) {
			capture := NewCapture(100)
			output, err := prepareChildOutput(capture)
			if err != nil {
				t.Fatal(err)
			}
			defer output.close()
			if !started {
				return
			}
			raw := strings.Repeat("payload", 4096)
			if n, err := output.file.Write([]byte(raw)); err != nil || n != len(raw) {
				t.Fatalf("write: %d %v", n, err)
			}
			output.started()
			if err := output.finish(); err != nil {
				t.Fatal(err)
			}
			got, overflow := capture.Snapshot()
			if got != raw[:100] || !overflow {
				t.Fatalf("capture changed: %q, %v", got, overflow)
			}
		})
	}
}
