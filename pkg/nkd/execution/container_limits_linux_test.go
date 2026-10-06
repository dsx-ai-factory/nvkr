//go:build linux

package execution

import (
	"context"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestVerifyCgroupLimitsExactRealization(t *testing.T) {
	for _, tt := range []struct {
		name, directory, control, value string
		omit, canceled                  bool
		want                            e.Code
	}{
		{name: "exact finite leaf with unlimited ancestors"},
		{name: "equivalent CPU ratio", directory: "leaf", control: "cpu.max", value: "400000 200000"},
		{name: "128 bit CPU equality", directory: "leaf", control: "cpu.max", value: "18446744073709551614 9223372036854775807"},
		{name: "finite looser ancestor", directory: "parent", control: "cpu.max", value: "400000 100000"},
		{name: "root controller absent", directory: "root", control: "cpu.max", omit: true},
		{name: "CPU unlimited", directory: "leaf", control: "cpu.max", value: "max 100000", want: e.CodeEngineCapability},
		{name: "memory unlimited", directory: "leaf", control: "memory.max", value: "max", want: e.CodeEngineCapability},
		{name: "PID unlimited", directory: "leaf", control: "pids.max", value: "max", want: e.CodeEngineCapability},
		{name: "CPU missing", directory: "leaf", control: "cpu.max", omit: true, want: e.CodeEngineCapability},
		{name: "memory missing", directory: "leaf", control: "memory.max", omit: true, want: e.CodeEngineCapability},
		{name: "PID missing", directory: "leaf", control: "pids.max", omit: true, want: e.CodeEngineCapability},
		{name: "CPU malformed", directory: "leaf", control: "cpu.max", value: "200000 100000 extra", want: e.CodeEngineCapability},
		{name: "memory malformed", directory: "leaf", control: "memory.max", value: "2147483648 extra", want: e.CodeEngineCapability},
		{name: "PID malformed", directory: "leaf", control: "pids.max", value: "not-an-integer", want: e.CodeEngineCapability},
		{name: "CPU fractional excess", directory: "leaf", control: "cpu.max", value: "200001 100000", want: e.CodeEngineCapability},
		{name: "CPU insufficient", directory: "leaf", control: "cpu.max", value: "199999 100000", want: e.CodeEngineCapability},
		{name: "memory excess", directory: "leaf", control: "memory.max", value: "2147483649", want: e.CodeEngineCapability},
		{name: "memory insufficient", directory: "leaf", control: "memory.max", value: "2147483647", want: e.CodeEngineCapability},
		{name: "PID excess", directory: "leaf", control: "pids.max", value: "257", want: e.CodeEngineCapability},
		{name: "PID insufficient", directory: "leaf", control: "pids.max", value: "255", want: e.CodeEngineCapability},
		{name: "PID zero", directory: "leaf", control: "pids.max", value: "0", want: e.CodeEngineCapability},
		{name: "PID overflow", directory: "leaf", control: "pids.max", value: "18446744073709551616", want: e.CodeEngineCapability},
		{name: "ancestor CPU stricter", directory: "parent", control: "cpu.max", value: "100000 100000", want: e.CodeEngineCapability},
		{name: "ancestor memory stricter", directory: "parent", control: "memory.max", value: "1073741824", want: e.CodeEngineCapability},
		{name: "ancestor PID stricter", directory: "parent", control: "pids.max", value: "128", want: e.CodeEngineCapability},
		{name: "ancestor unknown", directory: "parent", control: "pids.max", omit: true, want: e.CodeEngineCapability},
		{name: "ancestor malformed", directory: "root", control: "memory.max", value: "unknown", want: e.CodeEngineCapability},
		{name: "unbounded control file", directory: "leaf", control: "pids.max", value: strings.Repeat("1", 129), want: e.CodeEngineCapability},
		{name: "canceled", canceled: true, want: e.CodeTimeout},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, parent, leaf := cgroupCapacityFixture(t)
			for name, path := range map[string]string{"root": root, "parent": parent, "leaf": leaf} {
				for control, value := range map[string]string{"cpu.max": "max 100000", "memory.max": "max", "pids.max": "max"} {
					if name == "leaf" {
						value = map[string]string{"cpu.max": "200000 100000", "memory.max": "2147483648", "pids.max": "256"}[control]
					}
					if name == tt.directory && control == tt.control {
						if tt.omit {
							continue
						}
						value = tt.value
					}
					writeCgroupValue(t, path, control, value)
				}
			}
			ctx := t.Context()
			if tt.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := verifyCgroupLimits(ctx, leaf, root, 2000, 2048, 256); errorCode(err) != tt.want {
				t.Fatalf("error=%v want code=%s", err, tt.want)
			}
		})
	}
}
