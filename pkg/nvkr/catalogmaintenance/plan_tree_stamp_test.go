// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestPlanStampAcceptsIndentedRootAbovePackedBound(t *testing.T) {
	t.Chdir(t.TempDir())
	raw := `{"generatorVersion":"test","padding":"` + strings.Repeat("x", int(factory.MaxPlanBytes)) + `"}`
	if err := os.WriteFile("plan.json", []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	if err := admitPlanStamp(t.Context(), "plan.json", "test"); err != nil {
		t.Fatal(err)
	}
}
