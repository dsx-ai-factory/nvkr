// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type forbiddenOutput struct{ t *testing.T }

func (output forbiddenOutput) Write([]byte) (int, error) {
	output.t.Fatal("arbitrary output callback was invoked")
	return 0, nil
}

func TestRuntimeOutputsAdmitOnlyOwnedWriteBehavior(t *testing.T) {
	var buffer bytes.Buffer
	var builder strings.Builder
	capture := NewCapture(4)
	for _, tc := range []struct {
		name   string
		writer io.Writer
		ok     bool
	}{
		{"absent", nil, true}, {"discard", io.Discard, true},
		{"file", os.Stderr, true}, {"buffer", &buffer, true},
		{"builder", &builder, true}, {"capture", capture, true},
		{"interactive diagnostics", new(rundiagnostics.Buffer), true},
		{"nil interactive diagnostics", (*rundiagnostics.Buffer)(nil), false},
		{"callback", forbiddenOutput{t}, false}, {"nil file", (*os.File)(nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := Outputs(tc.writer, tc.writer)
			if !tc.ok {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatalf("unsafe output admission: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, file := tc.writer.(*os.File); file && out != tc.writer {
				t.Fatal("file descriptor hidden behind a parent copy goroutine")
			}
		})
	}
	out, errout, err := Outputs(&buffer, &buffer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("out")); err != nil {
		t.Fatal(err)
	}
	if _, err := errout.Write([]byte("err")); err != nil {
		t.Fatal(err)
	}
	if buffer.String() != "outerr" {
		t.Fatal(buffer.String())
	}
}

func TestRuntimeOutputsPreserveInteractiveDiagnosticsTail(t *testing.T) {
	var diagnostics rundiagnostics.Buffer
	stdout, stderr, err := Outputs(&diagnostics, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != &diagnostics || stderr != &diagnostics {
		t.Fatal("runtime replaced the CLI's shared bounded collector")
	}
	if _, err := stdout.Write(bytes.Repeat([]byte("x"), 3*rundiagnostics.MaxBytes)); err != nil {
		t.Fatal(err)
	}
	const tail = "last runtime failure"
	if _, err := io.WriteString(stderr, tail); err != nil {
		t.Fatal(err)
	}
	raw, truncated := diagnostics.Snapshot()
	if len(raw) != rundiagnostics.MaxBytes || !truncated || !bytes.HasSuffix(raw, []byte(tail)) {
		t.Fatal("runtime lost the bounded interactive failure tail")
	}
}

func TestRuntimeCaptureBoundAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		limit    int
		want     string
		overflow bool
	}{
		{0, "", true}, {3, "abc", true}, {6, "abcdef", false},
	} {
		output := NewCapture(tc.limit)
		for _, raw := range []string{"ab", "cdef"} {
			if n, err := io.WriteString(output, raw); err != nil || n != len(raw) {
				t.Fatalf("capture stopped drainage: %d %v", n, err)
			}
		}
		got, overflow := output.Snapshot()
		if got != tc.want || overflow != tc.overflow {
			t.Fatalf("limit %d: %q overflow=%v", tc.limit, got, overflow)
		}
	}
}
