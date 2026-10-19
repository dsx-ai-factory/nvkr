// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSameReleaseCohortAdmissionAndAtomicity(t *testing.T) {
	for _, tc := range []struct {
		name           string
		groups         [][]string
		selection      []string
		registryChange func(string) string
		mismatch       bool
		code           e.Code
		resolves       int
	}{
		{name: "aligned", groups: [][]string{{"source", "second"}}, resolves: 2},
		{name: "different bytes", groups: [][]string{{"source", "second"}}, mismatch: true, code: e.CodeUnverifiedRef, resolves: 2},
		{name: "unselected", groups: [][]string{{"source", "second"}}, selection: []string{"source"}, code: e.CodeUsage},
		{name: "duplicate", groups: [][]string{{"source", "source"}}, code: e.CodeUsage},
		{name: "singleton", groups: [][]string{{"source"}}, code: e.CodeUsage},
		{name: "unknown", groups: [][]string{{"source", "absent"}}, code: e.CodeUsage},
		{name: "different repository", groups: [][]string{{"source", "second"}}, registryChange: func(s string) string {
			return strings.Replace(s, "second: {repo: https://example.invalid/source", "second: {repo: https://example.invalid/different", 1)
		}, code: e.CodeInvalidCatalog},
		{name: "different constraint", groups: [][]string{{"source", "second"}}, registryChange: func(s string) string {
			return strings.Replace(s, "second: {repo: https://example.invalid/source, constraint: 1.x}", "second: {repo: https://example.invalid/source, constraint: 2.x}", 1)
		}, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sourceUpdateFixture(t)
			registryPath := filepath.Join(dir, RegistryFile)
			raw, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			registry := strings.ReplaceAll(string(raw), "https://example.invalid/second", "https://example.invalid/source")
			if tc.registryChange != nil {
				registry = tc.registryChange(registry)
			}
			if err := os.WriteFile(registryPath, []byte(registry), 0600); err != nil {
				t.Fatal(err)
			}
			before := readPinFixtureLock(t, dir)
			selection := tc.selection
			if selection == nil {
				selection = []string{"source", "second"}
			}
			resolves := 0
			update, err := updateSourcePins(t.Context(), SourcePinRequest{Directory: dir, Sources: selection, Bump: true, SameRelease: tc.groups},
				func(context.Context, string) ([]string, error) { return []string{"v1.1.0"}, nil },
				func(context.Context, Source, string) (Pin, error) {
					resolves++
					pin := sourceUpdatePin("v1.1.0")
					if tc.mismatch && resolves == 2 {
						pin.Commit = strings.Repeat("c", 40)
					}
					return pin, nil
				})
			if tc.code != "" {
				assertCatalogContractCode(t, err, tc.code)
				if !bytes.Equal(before, readPinFixtureLock(t, dir)) {
					t.Fatal("failed cohort changed lock")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if resolves != tc.resolves {
				t.Fatalf("resolution calls=%d want=%d", resolves, tc.resolves)
			}
			if tc.code == "" && update.Changed != 2 {
				t.Fatal("aligned transaction did not update both sources")
			}
		})
	}
}
