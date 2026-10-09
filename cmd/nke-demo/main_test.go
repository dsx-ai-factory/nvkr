// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestFlagsDoNotExecuteOnHelpOrInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		exit int
	}{{"empty", nil, 2}, {"unknown", []string{"unknown"}, 2}, {"run-help", []string{"run", "--help"}, 0}, {"build-help", []string{"build", "--help"}, 0}, {"restore-help", []string{"restore", "--help"}, 0}, {"badflag", []string{"run", "--wrong"}, 2}, {"position", []string{"run", "unexpected"}, 2}, {"restore-missing", []string{"restore"}, 2}, {"invalid-budget", []string{"run", "--timeout", "1"}, 17}} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			err := execute(t.Context(), test.args, &out, &diagnostics)
			if nkderr.ExitCode(err) != test.exit {
				t.Fatalf("error=%v exit=%d", err, nkderr.ExitCode(err))
			}
			if bytes.Contains(out.Bytes(), []byte("PASS:")) {
				t.Fatal("flag handling falselyreportedacceptance")
			}
		})
	}
}
