// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCatalogMaintenanceRejectsBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"refresh timeout", []string{"refresh", "--config", "absent", "--timeout", "0s"}, e.CodeUsage},
		{"refresh positional", []string{"refresh", "--config", "absent", "extra"}, e.CodeUsage},
		{"refresh missing input", []string{"refresh", "--config", "absent"}, e.CodeInvalidCatalog},
		{"workflow engine", []string{"update-ci", "--config", "absent", "--engine", "gitlab"}, e.CodeEngineCapability},
		{"workflow timeout", []string{"update-ci", "--config", "absent", "--timeout", "-1s"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runCatalogCommand(t, append([]string{"catalog"}, tc.args...)...)
			if got := cliErrorCode(err); got != tc.code {
				t.Fatalf("code %s, want %s: %v", got, tc.code, err)
			}
			if output != "" {
				t.Fatalf("failure emitted output: %q", output)
			}
		})
	}
}

func TestCatalogMaintenanceRejectsTrailingPolicyBeforeWork(t *testing.T) {
	const valid = `apiVersion: nkd.dev/catalog-maintenance/v1
name: test
catalog: unopened-catalog
sources: [source]
definition: unopened-definition.yaml
environment: unopened-environment.yaml
validation: static
`
	for _, command := range []string{"refresh", "update-ci"} {
		for _, trailing := range []struct {
			name string
			raw  string
		}{
			{"unknown", "---\nunknownPolicy: true\n"},
			{"conflicting", "---\n" + valid + "schedule: '0 6 * * 1'\n"},
			{"malformed", "---\ninvalid: [\n"},
		} {
			t.Run(command+"/"+trailing.name, func(t *testing.T) {
				t.Chdir(t.TempDir())
				raw := valid + trailing.raw
				if err := os.WriteFile("maintenance.yaml", []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				output, err := runCatalogCommand(t, "catalog", command, "--config", "maintenance.yaml")
				if cliErrorCode(err) != e.CodeInvalidCatalog || !strings.Contains(err.Error(), "decoding maintenance config") {
					t.Fatalf("expected policy decoding rejection before opening selected inputs: %v", err)
				}
				if output != "" {
					t.Fatalf("invalid policy emitted output: %q", output)
				}
				entries, err := os.ReadDir(".")
				if err != nil || len(entries) != 1 || entries[0].Name() != "maintenance.yaml" {
					t.Fatalf("invalid policy changed workspace: entries=%v error=%v", entries, err)
				}
				retained, err := os.ReadFile("maintenance.yaml")
				if err != nil || string(retained) != raw {
					t.Fatalf("invalid policy file changed: error=%v", err)
				}
			})
		}
	}
}
