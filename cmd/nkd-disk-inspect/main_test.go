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

func TestInspectionCompositionDispatchPreservesDistinctFrozenPins(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name string
			edit func([]string)
			want e.Code
		}{
			{"complete dispatch", nil, ""},
			{"bad operation", func(args []string) { args[4] = "diagnostic" }, e.CodeUsage},
			{"bad budget", func(args []string) { args[9] = "not-a-budget" }, e.CodeUsage},
			{"overflow budget", func(args []string) { args[9] = "18446744073709551616" }, e.CodeUsage},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				pin := func(value string) string { return "sha256:" + strings.Repeat(value, 64) }
				// This is the frozen 14-position launcher contract also exercised
				// by pipeline.TestDiskContentInvocationSeparatesNativeHostAndConsumer.
				args := []string{pin("b"), pin("c"), pin("a"), "7", "composition", "disk-image", pin("d"), pin("e"), pin("f"), "1048576", platform, "1000", "8192", "256"}
				if test.edit != nil {
					test.edit(args)
				}
				var output, diagnostic bytes.Buffer
				admitted, called := false, false
				input := strings.NewReader("subject")
				status := runWithContentHelper(t.Context(), args, input, &output, &diagnostic,
					func(_ context.Context, cpu, memory, pids uint64) error {
						admitted = true
						if cpu != 1000 || memory != 8192 || pids != 256 {
							t.Fatal("resource argument mapping changed")
						}
						return nil
					},
					func(ctx context.Context, inspector, codec, subject string, size int64, kind, guestfish, scanner, reader string, budget uint64, consumer string, heldInput io.Reader, result io.Writer) error {
						called = true
						if !admitted || ctx != t.Context() || inspector != pin("b") || codec != pin("c") || subject != pin("a") || size != 7 || kind != "disk-image" || guestfish != pin("d") || scanner != pin("e") || reader != pin("f") || budget != 1048576 || consumer != platform || heldInput != input || result != &output {
							t.Fatal("composition dispatch changed a frozen pin, subject, bound or stream")
						}
						_, err := io.Copy(result, heldInput)
						return err
					})
				wantStatus := 0
				if test.want != "" {
					wantStatus = e.ExitCode(e.New(test.want, ""))
				}
				if status != wantStatus || called != (test.want == "") {
					t.Fatalf("status=%d called=%t diagnostic=%q", status, called, diagnostic.String())
				}
				if test.want == "" {
					if output.String() != "subject" || diagnostic.Len() != 0 {
						t.Fatal("successful composition lost its report stream")
					}
				} else if output.Len() != 0 || diagnostic.Len() == 0 {
					t.Fatal("invalid operation wrote a partial report")
				}
			})
		}
	}
}
