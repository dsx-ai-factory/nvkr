// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package qemutpm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPreparationAdmitsBeforeCreatingRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, report string
		accelerator  Accelerator
		ok           bool
	}{
		{"supported HVF", `{"type":"swtpm","features":["tpm-2.0","ctrl-opt-terminate"]}`, HVF, true},
		{"supported TCG", `{"type":"swtpm","features":["tpm-2.0","ctrl-opt-terminate"]}`, TCG, true},
		{"missing TPM2", `{"type":"swtpm","features":["tpm-1.2","ctrl-opt-terminate"]}`, TCG, false},
		{"missing disconnect termination", `{"type":"swtpm","features":["tpm-2.0"]}`, TCG, false},
		{"misleading output", `not JSON but mentions "tpm-2.0"`, TCG, false},
		{"missing accelerator", `{"type":"swtpm","features":["tpm-2.0","ctrl-opt-terminate"]}`, "", false},
		{"fallback accelerator", `{"type":"swtpm","features":["tpm-2.0","ctrl-opt-terminate"]}`, "hvf:tcg", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			qemu := filepath.Join(root, "qemu")
			swtpm := filepath.Join(root, "swtpm")
			if err := os.WriteFile(qemu, []byte("#!/bin/sh\nprintf '%s\\n' hvf tcg 'emulator' 'name \"tpm-tis-device\"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(swtpm, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.report+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, "adapter")
			adapter, err := prepare(t.Context(), Options{self, directory, filepath.Join(root, "simulator", "instances"), qemu, swtpm, os.Environ(), tc.accelerator})
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if !tc.ok {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatal("uncoded admission failure", err)
				}
				if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
					t.Fatal("capability failure left partial runtime", statErr)
				}
				return
			}
			if !strings.Contains(filepath.Base(adapter.Executable()), "aarch64") {
				t.Fatal("upstream would select the wrong machine")
			}
			if _, err := load(adapter.Environment()[ConfigEnvironment]); err != nil {
				t.Fatal(err)
			}
			if err := adapter.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
