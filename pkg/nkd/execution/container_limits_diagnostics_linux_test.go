//go:build linux

package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestVerifyCgroupLimitsRequiredObservedDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name, control, value string
		want                 []string
	}{
		{name: "CPU fractional excess", control: "cpu.max", value: "200001 100000", want: []string{"required cpuMillis=2000", `observed cpu.max="200001 100000"`}},
		{name: "CPU unlimited", control: "cpu.max", value: "max 100000", want: []string{"required cpuMillis=2000", `observed cpu.max="max 100000"`}},
		{name: "CPU malformed", control: "cpu.max", value: "invalid", want: []string{"required cpuMillis=2000", `observed cpu.max="invalid"`}},
		{name: "memory different", control: "memory.max", value: "2147483647", want: []string{"memory.max", "required=2147483648", "observed=2147483647"}},
		{name: "PID different", control: "pids.max", value: "255", want: []string{"pids.max", "required=256", "observed=255"}},
		{name: "PID unlimited", control: "pids.max", value: "max", want: []string{"pids.max", "required=256", `observed="max"`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, _, leaf := cgroupCapacityFixture(t)
			for control, value := range map[string]string{"cpu.max": "200000 100000", "memory.max": "2147483648", "pids.max": "256"} {
				if control == tt.control {
					value = tt.value
				}
				writeCgroupValue(t, leaf, control, value)
			}
			err := verifyCgroupLimits(t.Context(), leaf, root, 2000, 2048, 256)
			if errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("limit mismatch admitted: %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing bounded actual/required diagnostic %q: %v", want, err)
				}
			}
		})
	}
}

type limitVerificationCancelContext struct {
	context.Context
	cancel context.CancelFunc
	after  int
	calls  int
}

func (ctx *limitVerificationCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.after {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestVerifyCgroupLimitsCancellationDuringObservation(t *testing.T) {
	for _, after := range []int{1, 2, 3, 20} {
		t.Run(fmt.Sprintf("context check %d", after), func(t *testing.T) {
			root, parent, leaf := cgroupCapacityFixture(t)
			for _, path := range []string{root, parent, leaf} {
				for control, value := range map[string]string{"cpu.max": "200000 100000", "memory.max": "2147483648", "pids.max": "256"} {
					writeCgroupValue(t, path, control, value)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			controlled := &limitVerificationCancelContext{Context: ctx, cancel: cancel, after: after}
			err := verifyCgroupLimits(controlled, leaf, root, 2000, 2048, 256)
			if errorCode(err) != e.CodeTimeout || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost its timeout code/cause: %v", err)
			}
		})
	}
}
