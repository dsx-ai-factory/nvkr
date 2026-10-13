// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"io"
	"os"

	"golang.org/x/term"
)

// Eligible never opens /dev/tty behind a redirected or injected output writer.
// CI controls automatic CLI selection, not explicit terminal eligibility.
func Eligible(out io.Writer) bool {
	return eligibleTerminal(out, os.Getenv("TERM"), term.IsTerminal)
}

func eligibleTerminal(out io.Writer, terminal string, isTerminal func(int) bool) bool {
	return out == os.Stdout && terminal != "" && terminal != "dumb" &&
		isTerminal(int(os.Stdin.Fd())) && isTerminal(int(os.Stdout.Fd()))
}
