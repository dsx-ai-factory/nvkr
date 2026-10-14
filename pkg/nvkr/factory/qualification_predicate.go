// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
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
	if err := validateHostedProductionPredicate(raw, plan, plan.Environment.Signing); err != nil {
		return err
	}
	var predicate provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &predicate); err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "decoding production predicate")
	}
	if predicate.RunDetails.Builder.ID != builder || predicate.RunDetails.Metadata["invocationId"] != invocation {
		return e.New(e.CodeVerifyFailed, "production provenance belongs to another workflow or Run attempt")
	}
	return nil
}
