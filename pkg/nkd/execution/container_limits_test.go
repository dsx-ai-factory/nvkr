// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"errors"
	"math"
	"runtime"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestVerifyCurrentCgroupLimitsAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, expire := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer expire()
	for _, tt := range []struct {
		name              string
		ctx               context.Context
		cpu, memory, pids uint64
		want              e.Code
	}{
		{name: "nil context", cpu: 2000, memory: 2048, pids: 256, want: e.CodeUsage},
		{name: "canceled", ctx: canceled, cpu: 2000, memory: 2048, pids: 256, want: e.CodeTimeout},
		{name: "deadline expired", ctx: expired, cpu: 2000, memory: 2048, pids: 256, want: e.CodeTimeout},
		{name: "no CPU", ctx: t.Context(), memory: 2048, pids: 256, want: e.CodeEngineCapability},
		{name: "no memory", ctx: t.Context(), cpu: 2000, pids: 256, want: e.CodeEngineCapability},
		{name: "memory overflow", ctx: t.Context(), cpu: 2000, memory: math.MaxUint64/(1024*1024) + 1, pids: 256, want: e.CodeEngineCapability},
		{name: "no PID bound", ctx: t.Context(), cpu: 2000, memory: 2048, want: e.CodeEngineCapability},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyCurrentCgroupLimits(tt.ctx, tt.cpu, tt.memory, tt.pids)
			if got := errorCode(err); got != tt.want {
				t.Fatalf("code=%s want=%s", got, tt.want)
			}
			if tt.ctx != nil && tt.ctx.Err() != nil && !errors.Is(err, tt.ctx.Err()) {
				t.Fatalf("timeout lost its context cancellation/deadline cause: %v", err)
			}
		})
	}
	if runtime.GOOS != "linux" && errorCode(VerifyCurrentCgroupLimits(t.Context(), 2000, 2048, 256)) != e.CodeEngineCapability {
		t.Fatal("non-Linux container admitted without kernel enforcement")
	}
}
