// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"
)

func TestPackerActivityOperationDoesNotNarrowProducerAdmission(t *testing.T) {
	for _, row := range []struct {
		name, nodeID, want string
		enabled            bool
	}{
		{"ordinary operation", "os/build-disk", "os/build-disk", true},
		{"disabled observation", "os/build-disk", "", false},
		{"maximum operation", strings.Repeat("a", 256), strings.Repeat("a", 256), true},
		{"overlength valid producer ID", "build-" + strings.Repeat("a", 251), "", true},
		{"unsupported label", "os/private operation", "", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := packerActivityOperation(row.nodeID, row.enabled); got != row.want {
				t.Fatalf("operation=%q, want %q", got, row.want)
			}
		})
	}
}
