// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
)

type nativeSudoSession func(context.Context, io.Writer, io.Writer, func(context.Context, func(context.Context) error) error) error

// Linux uses the existing bounded terminal authentication/session owner before
// starting presentation. Darwin never enters Linux sudo or network recovery.
func runNativeProgress(ctx context.Context, platform, work string, out, diagnostics io.Writer, plain bool, action func(context.Context, io.Writer) error, sudo nativeSudoSession) error {
	if platform != "linux" {
		return runProgress(ctx, out, plain, action)
	}
	recovery, err := progressRecoveryContext(ctx, work)
	if err != nil {
		return err
	}
	return sudo(recovery, out, diagnostics, func(session context.Context, refresh func(context.Context) error) error {
		return runProgress(session, out, plain, action, refresh)
	})
}
