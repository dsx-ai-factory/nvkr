// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func module(t *testing.T, name, text string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestNotices(t *testing.T) {
	dir := module(t, "demos/nvke/module cache/nested-only", "Apache License\nSynthetic copyright — preserved.\n")
	if err := os.WriteFile(filepath.Join(dir, "NOTICE"), []byte("Synthetic nested NOTICE.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := "example.test/a\tv1.2.3\t" + dir + "\n"
	second := "example.test/z\tv2.0.0\t" + module(t, "z", "MIT License\nSynthetic license text.") + "\n"
	baseline, err := generate(t.Context(), first+second)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| `example.test/a` | v1.2.3 | Apache-2.0 |", "| `example.test/z` | v2.0.0 | MIT |", "Synthetic copyright — preserved.", "NOTICE:\n\n```\nSynthetic nested NOTICE.\n```"} {
		if !strings.Contains(baseline, want) {
			t.Fatalf("missing %q in %s", want, baseline)
		}
	}
	for _, linked := range []string{second + first, first + second + first, second + second + first} {
		got, err := generate(t.Context(), linked)
		if err != nil || got != baseline {
			t.Fatalf("duplicate/order drift: %v", err)
		}
	}
	crlf, err := generate(t.Context(), strings.ReplaceAll(first+second, "\n", "\r\n"))
	if err != nil || crlf != baseline {
		t.Fatalf("universal-newline drift: %v", err)
	}
	if strings.Count(baseline, "## example.test/a\n") != 1 || strings.Index(baseline, "## example.test/a\n") > strings.Index(baseline, "## example.test/z\n") {
		t.Fatal("incorrect order or duplication")
	}
}
func TestNoticesFailClosed(t *testing.T) {
	dir := module(t, "m", "MIT License")
	empty := t.TempDir()
	bad := module(t, "bad", string([]byte{0xff}))
	first := "example.test/m\tv1\t" + dir + "\n"
	for _, tc := range []struct{ name, linked, want string }{
		{"version", first + "example.test/m\tv2\t" + dir + "\n", "conflicting linked versions"},
		{"directory", first + "example.test/m\tv1\t" + empty + "\n", "conflicting linked versions"},
		{"short", "m\tv1\n", "malformed"}, {"long", "m\tv1\t/no\textra\n", "malformed"},
		{"missing directory", "m\tv1\t/no/such/module\n", "no local module directory"},
		{"missing license", "m\tv1\t" + empty + "\n", "no LICENSE"},
		{"invalid UTF8", "m\tv1\t" + bad + "\n", "invalid UTF-8"},
		{"invalid metadata UTF8", string([]byte{0xff}), "invalid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := generate(t.Context(), tc.linked)
			if err == nil || !strings.Contains(err.Error(), tc.want) || got != "" {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
