// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package heldfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPrivateModeAdmitsOpenedSourceBeforeCapture(t *testing.T) {
	for _, mode := range []os.FileMode{0400, 0600, 0640, 0620, 0604, 0602, 0666} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input")
			if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			held, err := OpenContext(t.Context(), path, Options{Label: "input", MaxBytes: 100, RequirePrivateMode: true})
			if mode&0077 != 0 {
				if err == nil || !strings.Contains(err.Error(), "owner-only") {
					t.Fatalf("private mode admission=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_ = held.Close()
			}
			// Privacy admission is additive; existing bounded regular-file callers keep
			// their established behavior when no private-source policy is requested.
			held, err = OpenContext(t.Context(), path, Options{Label: "input", MaxBytes: 100})
			if err != nil {
				t.Fatal(err)
			}
			_ = held.Close()
		})
	}
}
