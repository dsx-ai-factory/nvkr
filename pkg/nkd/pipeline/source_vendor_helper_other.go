// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pipeline

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func admitSourceVendorPodmanHelpers(ctx context.Context, held *heldRuntimeExecutable) error {
	return e.New(e.CodeEngineCapability, "source vendor network helper admission requires Linux")
}
