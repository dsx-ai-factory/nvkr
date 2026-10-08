// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pipeline

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func snapshotMappedRunnerExecutableContext(ctx context.Context) (*heldfile.File, func(), error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "holding the mapped runner requires a non-nil context")
	}
	return nil, nil, e.New(e.CodeEngineCapability,
		"qualified local execution requires a platform adapter that can snapshot the process's mapped runner executable; this platform has none")
}
