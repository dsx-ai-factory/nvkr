// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"strconv"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// githubSourceRequest selects the original transport request, never rewrites a
// locator. The GitHub acquisition owner separately authenticates the provider's
// same-Run repository/head and ZIP digest. Restoration still verifies the exact
// source binding and payload; consumers retain Plan/evidence authorization.
func githubSourceRequest(request Request, locator Locator, allowEarlier bool) (Request, error) {
	if err := request.validate(); err != nil {
		return Request{}, err
	}
	if err := locator.Validate(locator.Request); err != nil {
		return Request{}, err
	}
	selected := request
	if allowEarlier {
		// Both IDs have already passed canonical positive uint64 validation.
		source, _ := strconv.ParseUint(locator.Request.Attempt, 10, 64)
		current, _ := strconv.ParseUint(request.Attempt, 10, 64)
		if source > current {
			return Request{}, e.New(e.CodeVerifyFailed, "CI locator names a future source attempt")
		}
		selected.Attempt = locator.Request.Attempt
	}
	if locator.Request != selected {
		return Request{}, e.New(e.CodeVerifyFailed, "CI locator differs from the selected source scope")
	}
	return selected, nil
}
