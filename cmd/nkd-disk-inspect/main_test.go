package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestInspectionHelperExitCodes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	for _, tt := range []struct {
		name string
		args []string
		ctx  context.Context
		want e.Code
	}{
		{name: "missing args", ctx: t.Context(), want: e.CodeUsage},
		{name: "extra args", args: []string{digest, digest, digest, "1", "extra"}, ctx: t.Context(), want: e.CodeUsage},
		{name: "invalid size", args: []string{digest, digest, digest, "not-an-integer"}, ctx: t.Context(), want: e.CodeUsage},
		{name: "overflow size", args: []string{digest, digest, digest, "9223372036854775808"}, ctx: t.Context(), want: e.CodeUsage},
		{name: "invalid subject pin", args: []string{digest, digest, "invalid", "1"}, ctx: t.Context(), want: e.CodeUnpinnedInput},
		{name: "zero size", args: []string{digest, digest, digest, "0"}, ctx: t.Context(), want: e.CodeUnpinnedInput},
		{name: "nil context", args: []string{digest, digest, digest, "1"}, want: e.CodeUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output, diagnostic bytes.Buffer
			got := run(tt.ctx, tt.args, strings.NewReader("x"), &output, &diagnostic)
			if got != e.ExitCode(e.New(tt.want, "")) || output.Len() != 0 || diagnostic.Len() == 0 {
				t.Fatalf("status=%d output=%q diagnostic=%q", got, output.String(), diagnostic.String())
			}
		})
	}
}
