// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalToolchainPreparationCommand(t *testing.T) {
	raw, _, _ := githubActionDataCommandFixture(t)
	root := t.TempDir()
	plan := filepath.Join(root, "plan.json")
	bindings := filepath.Join(root, "bindings.json")
	if err := os.WriteFile(plan, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindings, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		flags []string
		code  e.Code
	}{
		{name: "public consumers need no substitutions"},
		{name: "verify unchanged public consumers", flags: []string{"--image", "sha256:" + strings.Repeat("a", 64), "--selected-plan", plan}},
		{name: "reject tag", flags: []string{"--image", "local:latest"}, code: e.CodeVerifyFailed},
		{name: "reject missing measured image", flags: []string{"--selected-plan", plan}, code: e.CodeUsage},
		{name: "reject positional input", flags: []string{"unexpected"}, code: e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newApp()
			var output bytes.Buffer
			app.Writer = &output
			args := []string{"nvkr", "factory", "local-toolchain-scopes", "--plan", plan, "--bindings", bindings, "--arch", "amd64"}
			err := app.RunContext(t.Context(), append(args, tc.flags...))
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code || output.Len() != 0 {
					t.Fatalf("error=%v output=%q", err, &output)
				}
			} else if err != nil || strings.TrimSpace(output.String()) != "[]" {
				t.Fatalf("error=%v output=%q", err, &output)
			}
		})
	}
}
