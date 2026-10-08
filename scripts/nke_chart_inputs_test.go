// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import "testing"

func TestNKEChartInputsAdapter(t *testing.T) {
	runPythonContract(t, "nke_prepare_chart_inputs_test")
}
