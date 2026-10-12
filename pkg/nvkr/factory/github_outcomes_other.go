// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package factory

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func admitGitHubRecoveryPlatform() error {
	return e.New(e.CodeEngineCapability, "GitHub artifact recovery is supported only on Linux and Darwin")
}

func writeGitHubRestoreOutcome(context.Context, string, string) error {
	return admitGitHubRecoveryPlatform()
}
