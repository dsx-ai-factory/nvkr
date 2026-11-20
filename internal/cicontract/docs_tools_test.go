// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"strings"
	"testing"
)

func TestDocsCheckerHasPinnedGoBeforeExecution(t *testing.T) {
	installed, checked := false, false
	for _, step := range readMergeGateJobs(t)["docs"].Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-go@") && step.With["go-version-file"] == ".go-version" {
			installed = true
		}
		if step.Run == "make docs-check" {
			if !installed {
				t.Fatal("the Go docs checker needs the pinned compiler in its own job before execution")
			}
			checked = true
		}
	}
	if !checked {
		t.Fatal("docs job no longer executes docs-check")
	}
}
