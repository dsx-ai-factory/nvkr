// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package execution

import (
	"context"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

type systemLocalObserver struct{}

type darwinLocalObservation struct {
	mu          sync.Mutex
	workspaceFD int
	observed    atomic.Bool
}

func (systemLocalObserver) Open(ctx context.Context, workspace *os.File) (localObservationLease, error) {
	if err := localObservationContextError(ctx, "opening local Darwin observation"); err != nil {
		return nil, err
	}
	if runtime.GOARCH != "arm64" {
		return nil, e.New(e.CodeEngineCapability, "native Darwin observation requires ARM64")
	}
	fd, err := duplicateWorkspaceDirectory(workspace, func(fd uintptr) (int, error) { return unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0) })
	if err != nil {
		return nil, err
	}
	observation := &darwinLocalObservation{workspaceFD: fd}
	if err := observation.checkWorkspace(); err != nil {
		_ = observation.Close()
		return nil, err
	}
	return observation, nil
}

func (o *darwinLocalObservation) checkWorkspace() error {
	if o.workspaceFD < 0 {
		return e.New(e.CodeEngineCapability, "local Darwin workspace capability is closed")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(o.workspaceFD, &stat); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "inspecting held Darwin workspace")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return e.New(e.CodeEngineCapability, "local Darwin workspace is not a directory")
	}
	if err := unix.Faccessat(o.workspaceFD, ".", unix.W_OK|unix.X_OK, unix.AT_EACCESS); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "checking held Darwin workspace access")
	}
	return nil
}

func (o *darwinLocalObservation) Observe(ctx context.Context) (ObservedHostFacts, error) {
	if !o.observed.CompareAndSwap(false, true) {
		return ObservedHostFacts{}, e.New(e.CodeInternal, "local Darwin observation lease has already been used")
	}
	return o.observeCurrent(ctx)
}
func (o *darwinLocalObservation) reobserve(ctx context.Context) (ObservedHostFacts, error) {
	if !o.observed.Load() {
		return ObservedHostFacts{}, e.New(e.CodeInternal, "local Darwin observation has not completed initial observation")
	}
	return o.observeCurrent(ctx)
}
func (o *darwinLocalObservation) observeCurrent(ctx context.Context) (ObservedHostFacts, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := localObservationContextError(ctx, "observing local Darwin host"); err != nil {
		return ObservedHostFacts{}, err
	}
	if err := o.checkWorkspace(); err != nil {
		return ObservedHostFacts{}, err
	}
	cpu, err := unix.SysctlUint32("hw.logicalcpu")
	if err != nil {
		return ObservedHostFacts{}, e.Wrap(e.CodeEngineCapability, err, "reading Darwin CPU capacity")
	}
	memory, err := darwinAvailableMemoryMiB(ctx)
	if err != nil {
		return ObservedHostFacts{}, err
	}
	scratch, err := availableScratchMiB(o.workspaceFD)
	if err != nil {
		return ObservedHostFacts{}, err
	}
	hvf, err := unix.SysctlUint32("kern.hv_support")
	if err != nil {
		return ObservedHostFacts{}, e.Wrap(e.CodeEngineCapability, err, "reading Darwin hypervisor support")
	}
	if cpu == 0 || hvf > 1 {
		return ObservedHostFacts{}, e.New(e.CodeEngineCapability, "Darwin host reported invalid CPU or hypervisor capacity")
	}
	if err := localObservationContextError(ctx, "observing local Darwin host"); err != nil {
		return ObservedHostFacts{}, err
	}
	facts := ObservedHostFacts{Platform: HostPlatformDarwinARM64, Resources: ResourceCapacity{CPUMillis: uint64(cpu) * 1000, MemoryMiB: memory, EphemeralStorageMiB: scratch}}
	if hvf == 1 {
		facts.Capabilities = []CapabilityName{CapabilityHVF}
	}
	return facts, nil
}
func (o *darwinLocalObservation) holdWorkspace() (*os.File, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return holdLocalFD(o.workspaceFD, "local Darwin VM workspace")
}
func (*darwinLocalObservation) holdKVM() (*os.File, error) {
	return nil, e.New(e.CodeEngineCapability, "Darwin exposes no KVM device")
}
func (o *darwinLocalObservation) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.workspaceFD < 0 {
		return nil
	}
	fd := o.workspaceFD
	o.workspaceFD = -1
	if err := unix.Close(fd); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "closing Darwin workspace observation")
	}
	return nil
}

// vm_stat(1), supplied by macOS, owns these Mach counters. Count free,
// inactive and speculative pages once; purgeable pages overlap other queues.
// This is a reclaimable-capacity estimate, never a reservation or pressure gate.
func darwinAvailableMemoryMiB(ctx context.Context) (uint64, error) {
	if err := localObservationContextError(ctx, "reading Darwin memory counters"); err != nil {
		return 0, err
	}
	pageSize, err := unix.SysctlUint32("hw.pagesize")
	if err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading Darwin page size")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, "/usr/bin/vm_stat")
	command.Env = []string{"LC_ALL=C", "LANG=C", "PATH=/usr/bin:/bin"}
	var capture rundiagnostics.Buffer
	command.Stdout = &capture
	command.Stderr = io.Discard
	if err := ownedcmd.Run(probeCtx, command); err != nil {
		if probeCtx.Err() != nil {
			return 0, e.Wrap(e.CodeTimeout, probeCtx.Err(), "reading Darwin memory counters")
		}
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading Darwin memory counters")
	}
	raw, truncated := capture.Snapshot()
	if truncated {
		return 0, e.New(e.CodeEngineCapability, "Darwin memory counter output exceeds its byte limit")
	}
	memory, err := darwinMemoryMiB(raw, uint64(pageSize))
	if err != nil {
		return 0, err
	}
	physical, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading Darwin physical memory")
	}
	if memory > physical/bytesPerMiB {
		return 0, e.New(e.CodeEngineCapability, "Darwin memory counters exceed physical capacity")
	}
	return memory, nil
}

func darwinMemoryMiB(raw []byte, pageSize uint64) (uint64, error) {
	if pageSize == 0 || pageSize&(pageSize-1) != 0 {
		return 0, e.New(e.CodeEngineCapability, "Darwin page size is invalid")
	}
	counters := map[string]uint64{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || (key != "Pages free" && key != "Pages inactive" && key != "Pages speculative") {
			continue
		}
		if _, duplicate := counters[key]; duplicate {
			return 0, e.New(e.CodeEngineCapability, "Darwin memory counter is duplicated")
		}
		value = strings.TrimSpace(value)
		if !strings.HasSuffix(value, ".") {
			return 0, e.New(e.CodeEngineCapability, "Darwin memory counter is malformed")
		}
		count, err := strconv.ParseUint(strings.TrimSuffix(value, "."), 10, 64)
		if err != nil {
			return 0, e.Wrap(e.CodeEngineCapability, err, "reading Darwin memory counter")
		}
		counters[key] = count
	}
	if len(counters) != 3 {
		return 0, e.New(e.CodeEngineCapability, "Darwin memory counters are incomplete")
	}
	var pages uint64
	for _, count := range counters {
		if count > math.MaxUint64-pages {
			return 0, e.New(e.CodeEngineCapability, "Darwin memory page count overflows")
		}
		pages += count
	}
	if pages > math.MaxUint64/pageSize {
		return 0, e.New(e.CodeEngineCapability, "Darwin memory capacity overflows")
	}
	memory := pages * pageSize / bytesPerMiB
	if memory == 0 {
		return 0, e.New(e.CodeEngineCapability, "Darwin reclaimable memory is below one MiB")
	}
	return memory, nil
}
