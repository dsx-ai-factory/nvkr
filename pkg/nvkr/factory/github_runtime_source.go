// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"reflect"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func validateGitHubRuntimeSourceDispatch(plan *Plan, raw json.RawMessage) error {
	if plan.Environment.GitHubRuntimeSource == nil {
		return nil
	}
	want := map[string]any{}
	want["inputs"] = map[string]any{"source_commit": map[string]any{
		"description": "Exact source commit frozen by the coordinating main workflow", "type": "string", "required": true,
	}}
	var actual map[string]any
	if err := json.Unmarshal(raw, &actual); err != nil || !reflect.DeepEqual(actual, want) {
		return e.New(e.CodeEngineCapability, "GitHub dispatch inputs differ from the exact runtime source policy")
	}
	return nil
}
