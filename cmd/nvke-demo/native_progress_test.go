// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestNativeProgressSelectsLinuxAuthenticationOwner(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			sessions, refreshes, work := 0, 0, 0
			directory := filepath.Join(t.TempDir(), "attempt")
			err := runNativeProgress(t.Context(), platform, directory, io.Discard, io.Discard, true, func(ctx context.Context, progress io.Writer) error {
				work++
				authenticate := progress.(nvkedemo.SudoAuthorizer).SudoAuthentication()
				if platform == "darwin" {
					if authenticate != nil {
						t.Fatal("Darwin inherited Linux authentication")
					}
					return nil
				}
				if progressRecoveryWork(ctx) != directory || authenticate == nil {
					t.Fatal("Linux lost recovery workspace or terminal callback")
				}
				return authenticate(ctx)
			}, func(ctx context.Context, _, _ io.Writer, action func(context.Context, func(context.Context) error) error) error {
				sessions++
				return action(ctx, func(context.Context) error { refreshes++; return nil })
			})
			if err != nil || work != 1 {
				t.Fatalf("native progress: work=%d error=%v", work, err)
			}
			if platform == "linux" && (sessions != 1 || refreshes != 1) || platform == "darwin" && (sessions != 0 || refreshes != 0) {
				t.Fatalf("wrong OS authentication dispatch: sessions=%d refreshes=%d", sessions, refreshes)
			}
		})
	}
}
