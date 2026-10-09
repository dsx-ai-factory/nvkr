// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"testing"
	"time"
)

func TestOnlyBuildLauncherStreamsOutput(t *testing.T) {
	for _, stream := range []bool{false, true} {
		var progress, output bytes.Buffer
		runner := Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: &progress}
		if stream {
			runner.output = &output
		}
		if err := runner.Run(t.Context(), []string{"sh", "-c", "printf safe-build-output"}, "stream-contract", 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(progress.Bytes(), []byte("safe-build-output")) {
			t.Fatal("private command diagnostics streamed through phasewriter")
		}
		if stream && output.String() != "safe-build-output" || !stream && output.Len() != 0 {
			t.Fatal("build stream opt-in was not respected")
		}
	}
}
