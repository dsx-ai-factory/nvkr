// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"strings"
	"testing"
)

func TestRenderEscapesUntrustedDisplayControls(t *testing.T) {
	for _, value := range []string{"suite\x1b[2J", "output\u202ehidden", "platform\u2028Trust: forged"} {
		t.Run(value, func(t *testing.T) {
			o := observationFixture()
			o.Suite.Name = value
			o.Subjects[0].Output = value
			o.Subjects[0].Platform = value
			rendered := Render(o, Verification{})
			if strings.Contains(rendered, value) {
				t.Fatal("unsafe display value rendered literally")
			}
			if !strings.HasPrefix(rendered, "Trust: unsigned self-consistency\n") {
				t.Fatal("untrusted text changed trust presentation")
			}
		})
	}
}
