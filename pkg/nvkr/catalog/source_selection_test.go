// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestFreezeSourceHeadDoesNotAcquireObjectsUnderFileCap(t *testing.T) {
	blob := make([]byte, 256<<10)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range blob {
		blob[i] = byte(rng.Uint32())
	}
	repo, pin, run := pinTreeFixture(t, map[string][]byte{"large.bin": blob})
	run("branch", "-M", "main")
	const repository = "https://github.com/example/source"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", repository)
	selection, err := freezeSourceHeadWithLimit(t.Context(), repository, "refs/heads/main", 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	want := SourceSelection{Repository: repository, Ref: "refs/heads/main", Commit: pin.Commit, TreeHash: pin.TreeHash, SourceDateEpoch: pin.SourceDateEpoch}
	if selection != want {
		t.Fatalf("selection = %+v, want %+v", selection, want)
	}
}

func TestFreezeSourceHeadAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name, ref string
		ctx       context.Context
		code      e.Code
	}{
		{"missing context", "refs/heads/main", nil, e.CodeUsage},
		{"canceled", "refs/heads/main", canceled, e.CodeTimeout},
		{"short ref", "main", t.Context(), e.CodeInvalidCatalog},
		{"foreign ref", "refs/heads/development", t.Context(), e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection, err := FreezeSourceHead(tc.ctx, "https://github.com/example/source", tc.ref)
			assertCatalogContractCode(t, err, tc.code)
			if selection != (SourceSelection{}) {
				t.Fatal("failed freeze returned a partial selection")
			}
		})
	}
}

func TestFreezeSourceHeadRejectsInvalidRefObservation(t *testing.T) {
	const ref = "refs/heads/main"
	commit := strings.Repeat("a", 40)
	line := commit + "\t" + ref
	for _, tc := range []struct{ name, output string }{
		{"missing", ""},
		{"short commit", "aaaa\t" + ref + "\n"},
		{"non hex commit", strings.Repeat("g", 40) + "\t" + ref + "\n"},
		{"wrong ref", commit + "\trefs/heads/other\n"},
		{"space separator", commit + " " + ref + "\n"},
		{"extra field", line + "\textra\n"},
		{"duplicate", line + "\n" + line + "\n"},
		{"blank line", line + "\n\n"},
		{"leading whitespace", " " + line + "\n"},
		{"trailing whitespace", line + " \n"},
		{"output limit", strings.Repeat("a", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := t.TempDir()
			calls := filepath.Join(t.TempDir(), "calls")
			script := "#!/bin/sh\nprintf 'call\\n' >> \"$SOURCE_FREEZE_CALLS\"\nprintf '%s' \"$SOURCE_FREEZE_OUTPUT\"\n"
			if err := os.WriteFile(filepath.Join(tools, "git"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools)
			t.Setenv("SOURCE_FREEZE_CALLS", calls)
			t.Setenv("SOURCE_FREEZE_OUTPUT", tc.output)
			selection, err := FreezeSourceHead(t.Context(), "https://github.com/example/source", ref)
			assertCatalogContractCode(t, err, e.CodeFetchFailed)
			if selection != (SourceSelection{}) {
				t.Fatal("invalid observation returned a partial selection")
			}
			got, err := os.ReadFile(calls)
			if err != nil || string(got) != "call\n" {
				t.Fatalf("invalid observation must fail before revision acquisition: calls = %q, err = %v", got, err)
			}
		})
	}
}
