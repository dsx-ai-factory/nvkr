// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"reflect"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func validateGitHubRuntimeSourceDispatch(plan *Plan, raw json.RawMessage) error {
	overrides, err := DeclaredSourceOverrides(plan)
	if err != nil {
		return err
	}
	if plan.Environment.GitHubRuntimeSource == nil && len(overrides) == 0 {
		return nil
	}
	want := map[string]any{}
	if plan.Environment.GitHubRuntimeSource != nil {
		want["inputs"] = map[string]any{"source_commit": map[string]any{
			"description": "Exact source commit frozen by the coordinating main workflow", "type": "string", "required": true,
		}}
	} else {
		inputs := make(map[string]any, len(overrides))
		for _, input := range overrides {
			inputs[input.Name] = map[string]any{"description": githubSourceOverrideDescription(input), "type": "string", "required": false, "default": ""}
		}
		want["inputs"] = inputs
	}
	var actual map[string]any
	if err := json.Unmarshal(raw, &actual); err != nil || !reflect.DeepEqual(actual, want) {
		return e.New(e.CodeEngineCapability, "GitHub dispatch inputs differ from the declared runtime source selection")
	}
	return nil
}
