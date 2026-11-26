// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validationplan

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Executor names. An executor is a workflow job and not NVKR code, so this
// file only records which names exist and which inputs each requires.
const (
	// ExecutorValidators runs a validator catalog entry against a release
	// output.
	ExecutorValidators = "validators"

	// ExecutorNVKEE2EGoTest runs NVKE's end-to-end suite from a checkout with
	// go test.
	ExecutorNVKEE2EGoTest = "nvke-e2e-gotest"
)

// executorInputs lists, for each known executor, the inputs a target must
// carry for the executor to run it.
var executorInputs = map[string][]string{
	ExecutorValidators:    {"suite", "platform", "components"},
	ExecutorNVKEE2EGoTest: {"suite", "testEnv", "components"},
}

func executorKnown(name string) bool {
	_, ok := executorInputs[name]
	return ok
}

// checkInputs fails when a target lacks an input its executor requires.
func checkInputs(t Target) error {
	required, ok := executorInputs[t.Executor]
	if !ok {
		return e.New(e.CodeUnsupported, "target %q names executor %q, which is not known", t.ID, t.Executor)
	}
	for _, key := range required {
		var present bool
		switch key {
		case "suite":
			present = t.Inputs.Suite != ""
		case "platform":
			present = t.Inputs.Platform != ""
		case "testEnv":
			present = t.Inputs.TestEnv != ""
		case "components":
			present = len(t.Inputs.Components) > 0
		}
		if !present {
			return e.New(e.CodeUsage, "target %q lacks %q, which executor %q requires", t.ID, key, t.Executor)
		}
	}
	return nil
}
