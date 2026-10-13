// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"bytes"
	"io"
	"os"
	"slices"
	"testing"
)

func TestTerminalEligibility(t *testing.T) {
	// Go's JSON test runner aliases os.Stderr to os.Stdout. An owned, distinct
	// file keeps the non-stdout fixture valid under the same runner used by CI.
	otherOutput, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer otherOutput.Close()
	stdinFD, stdoutFD := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	for _, test := range []struct {
		name, terminal string
		out            io.Writer
		stdin, stdout  bool
		want           bool
		calls          []int
	}{
		{"terminal", "xterm-256color", os.Stdout, true, true, true, []int{stdinFD, stdoutFD}},
		{"unset TERM", "", os.Stdout, true, true, false, nil},
		{"dumb TERM", "dumb", os.Stdout, true, true, false, nil},
		{"injected writer", "xterm", &bytes.Buffer{}, true, true, false, nil},
		{"other file writer", "xterm", otherOutput, true, true, false, nil},
		{"redirected stdin", "xterm", os.Stdout, false, true, false, []int{stdinFD}},
		{"redirected stdout", "xterm", os.Stdout, true, false, false, []int{stdinFD, stdoutFD}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []int
			got := eligibleTerminal(test.out, test.terminal, func(fd int) bool {
				calls = append(calls, fd)
				if fd == int(os.Stdin.Fd()) {
					return test.stdin
				}
				return test.stdout
			})
			if got != test.want || !slices.Equal(calls, test.calls) {
				t.Fatalf("eligibility=%t calls=%v, want %t %v", got, calls, test.want, test.calls)
			}
		})
	}
}

func TestEligibleRejectsInjectedWriterWithRealServices(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	if Eligible(&bytes.Buffer{}) {
		t.Fatal("injected writer admitted a controlling terminal")
	}
}
