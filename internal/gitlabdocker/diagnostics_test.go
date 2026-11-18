//go:build integration

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gitlabdocker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The failure reporter may emit static source text, never dynamic trace values.
// An absolute source path also guards the actual failure-path regression where
// a second root was incorrectly joined to an already absolute inventory entry.
func TestFailureDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.go")
	must(t, os.WriteFile(path, []byte(`package fixture
func rejected() { e.New(e.CodeVerifyFailed, "fixed static refusal"); e.Wrap(e.CodePipelineDrift, cause, "dynamic field %q is foreign", privateValue) }
`), 0600))
	for _, tc := range []struct {
		name, trace string
		want        []string
	}{
		{"fixed", "[NVKR-E-VERIFY-FAILED] fixed static refusal", []string{"owner.go: CodeVerifyFailed: fixed static refusal"}},
		{"dynamic secret", `[NVKR-E-PIPELINE-DRIFT] dynamic field "synthetic-private-token" is foreign`, []string{"owner.go: CodePipelineDrift: dynamic field "}},
		{"unrecognized private text", "synthetic-private-token", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sourceOwnedFailureMessages(t.Context(), []string{path}, []byte(tc.trace))
			must(t, err)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("source-owned diagnostics %q, want %q", got, tc.want)
			}
			if strings.Contains(strings.Join(got, ""), "synthetic-private-token") {
				t.Fatal("diagnostic disclosed a dynamic trace value")
			}
		})
	}
	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := sourceOwnedFailureMessages(ctx, []string{path}, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled diagnostic read returned %v", err)
		}
	})
}

func TestContainerCleanupInventory(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
		invalid   bool
	}{
		{"already absent", "", nil, false},
		{"unrelated container", "\"unrelated\"\n", nil, false},
		{"lost seed removal acknowledgement", "\"fixture-seed\"\n", []string{"fixture-seed"}, false},
		{"invalid JSON", "not-json", nil, true},
		{"missing name", "null\n", nil, true},
		{"empty name", "\"\"\n", nil, true},
		{"ambiguous names", "\"fixture-seed,alias\"\n", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remainingKnownContainers([]byte(tc.raw), []string{"fixture-server", "fixture-seed"})
			if (err != nil) != tc.invalid || !slices.Equal(got, tc.want) {
				t.Fatalf("inventory result %q error %v; want %q invalid %t", got, err, tc.want, tc.invalid)
			}
		})
	}
}
