package execution

import (
	"context"
	"math"
	"runtime"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestVerifyCurrentCgroupLimitsAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tt := range []struct {
		name              string
		ctx               context.Context
		cpu, memory, pids uint64
		want              e.Code
	}{
		{name: "nil context", cpu: 2000, memory: 2048, pids: 256, want: e.CodeUsage},
		{name: "canceled", ctx: canceled, cpu: 2000, memory: 2048, pids: 256, want: e.CodeEngineCapability},
		{name: "no CPU", ctx: t.Context(), memory: 2048, pids: 256, want: e.CodeEngineCapability},
		{name: "no memory", ctx: t.Context(), cpu: 2000, pids: 256, want: e.CodeEngineCapability},
		{name: "memory overflow", ctx: t.Context(), cpu: 2000, memory: math.MaxUint64/(1024*1024) + 1, pids: 256, want: e.CodeEngineCapability},
		{name: "no PID bound", ctx: t.Context(), cpu: 2000, memory: 2048, want: e.CodeEngineCapability},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorCode(VerifyCurrentCgroupLimits(tt.ctx, tt.cpu, tt.memory, tt.pids)); got != tt.want {
				t.Fatalf("code=%s want=%s", got, tt.want)
			}
		})
	}
	if runtime.GOOS != "linux" && errorCode(VerifyCurrentCgroupLimits(t.Context(), 2000, 2048, 256)) != e.CodeEngineCapability {
		t.Fatal("non-Linux container admitted without kernel enforcement")
	}
}
