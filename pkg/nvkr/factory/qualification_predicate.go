// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ValidateProductionPredicate compares a descriptive predicate with the exact
// admitted Plan and externally authenticated producing workflow invocation.
// Only a subsequent signature verification authenticates these predicate bytes.
func ValidateProductionPredicate(raw []byte, plan *Plan, builder, invocation string) error {
	if plan == nil || plan.Environment.Signing == nil || builder == "" || invocation == "" {
		return e.New(e.CodeProvenanceMissing, "qualification requires an exact Plan, builder and invocation")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	return validateProductionPredicateOrigin(raw, plan, plan.Environment.Signing, builder, invocation)
}
