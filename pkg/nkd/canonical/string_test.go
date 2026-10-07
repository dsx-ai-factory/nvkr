// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package canonical

import (
	"strings"
	"testing"
)

func TestMarshalStringBytes(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"empty", "", `""`},
		{"literal unicode", "é界😀\u2028\u2029<>&/", "\"é界😀\u2028\u2029<>&/\""},
		{"escapes", "\"\\\b\f\n\r\t", `"\"\\\b\f\n\r\t"`},
		{"other controls", "\x00\x01\x07\x0b\x0e\x1f", `"\u0000\u0001\u0007\u000b\u000e\u001f"`},
		{"escape boundaries", "\n界\"😀\\end\t", "\"\\n界\\\"😀\\\\end\\t\""},
		{"long literal span", strings.Repeat("x", 65536) + "\nend", `"` + strings.Repeat("x", 65536) + `\nend"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Marshal(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("canonical string bytes differ: got %q, want %q", got, tc.want)
			}
			var decoded string
			if err := UnmarshalExact(got, &decoded); err != nil || decoded != tc.input {
				t.Fatalf("exact round trip: %v", err)
			}
		})
	}
}

// Use the same instrumentation as CI when investigating large inventory costs:
// go test -race -cover -run '^$' -bench BenchmarkMarshalString ./pkg/nkd/canonical
func BenchmarkMarshalString(b *testing.B) {
	for _, tc := range []struct{ name, value string }{
		{"small", "pkg:generic/example@1.0.0"},
		{"inventory", strings.Repeat("x", 1<<20)},
		{"escaped", strings.Repeat("path/界\t\"", 1024)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.value)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Marshal(tc.value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
