package main

import (
	"bytes"
	"context"
	"io"
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args
			if len(args) == 4 {
				args = append(append([]string(nil), args...), "2000", "2048", "256")
			}
			var output, diagnostic bytes.Buffer
			got := runWithLimitCheck(tt.ctx, args, strings.NewReader("x"), &output, &diagnostic, func(_ context.Context, _, _, _ uint64) error {
				return nil
			})
			if got != e.ExitCode(e.New(tt.want, "")) || output.Len() != 0 || diagnostic.Len() == 0 {
				t.Fatalf("status=%d output=%q diagnostic=%q", got, output.String(), diagnostic.String())
			}
		})
	}
}

func TestInspectionHelperNilContextUsesProductionAdmission(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	args := []string{digest, digest, digest, "1", "2000", "2048", "256"}
	var output, diagnostic bytes.Buffer
	got := run(nil, args, forbiddenSubjectRead{t}, &output, &diagnostic) //nolint:staticcheck // intentional nil-context production admission test
	if got != e.ExitCode(e.New(e.CodeUsage, "")) || output.Len() != 0 || diagnostic.Len() == 0 {
		t.Fatalf("status=%d output=%q diagnostic=%q", got, output.String(), diagnostic.String())
	}
}

type forbiddenSubjectRead struct{ t *testing.T }

func (reader forbiddenSubjectRead) Read([]byte) (int, error) {
	reader.t.Error("subject read before resource admission")
	return 0, io.EOF
}

func TestInspectionHelperLimitsBeforeSubject(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	for _, composition := range []bool{false, true} {
		for _, mode := range []string{"unrealized", "missing", "malformed", "zero", "overflow"} {
			t.Run(mode+map[bool]string{false: "/disk", true: "/content"}[composition], func(t *testing.T) {
				args := []string{digest, digest, digest, "1"}
				if composition {
					args = append(args, "composition", "disk-image", digest, digest, digest, "1048576", "linux/arm64")
				}
				args = append(args, "2000", "2048", "256")
				want := e.CodeUsage
				switch mode {
				case "unrealized":
					want = e.CodeEngineCapability
				case "missing":
					args = args[:len(args)-3]
				case "malformed":
					args[len(args)-3] = "unlimited"
				case "zero":
					args[len(args)-2] = "0"
				case "overflow":
					args[len(args)-1] = "18446744073709551616"
				}
				var output, diagnostic bytes.Buffer
				called := false
				got := runWithLimitCheck(t.Context(), args, forbiddenSubjectRead{t}, &output, &diagnostic, func(_ context.Context, cpu, memory, pids uint64) error {
					called = true
					if cpu != 2000 || memory != 2048 || pids != 256 {
						t.Fatalf("substituted resource grant: %d/%d/%d", cpu, memory, pids)
					}
					return e.New(e.CodeEngineCapability, "unrealized cgroup limits")
				})
				if got != e.ExitCode(e.New(want, "")) || output.Len() != 0 || diagnostic.Len() == 0 || called != (mode == "unrealized") {
					t.Fatalf("status=%d output=%q diagnostic=%q verifier=%v", got, output.String(), diagnostic.String(), called)
				}
			})
		}
	}
}
