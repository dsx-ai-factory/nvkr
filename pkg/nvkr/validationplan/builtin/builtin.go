// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package builtin lists the planners compiled into nvkr. It is a closed set,
// not a plugin mechanism, so a planner exists only when this repository
// builds it.
package builtin

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan/nvkequalify"
)

// Planners returns the built-in planners by the name a planners
// configuration uses for them.
func Planners() map[string]validationplan.Planner {
	return map[string]validationplan.Planner{
		nvkequalify.Name: nvkequalify.Planner{},
	}
}
