// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"bytes"
	"context"
	"debug/elf"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// Launcher owns an immutable copy of this running NVKR executable and a
// successful kernel namespace probe. It never elevates itself or invokes sudo.
// The admitted runner bootstrap must supply privilege before any producer work.
// Close waits for active execution and revokes all copies of this capability.
type Launcher struct{ state *launcherState }
type launcherState struct {
	mu         sync.Mutex
	executable *heldfile.File
	assembly   bool
}

func Admit(ctx context.Context) (*Launcher, error) {
	return admit(ctx, false)
}

// AdmitAssembly probes an additional user namespace whose root maps to an
// unprivileged host ID. The returned launcher can only execute assembly requests;
// it cannot confer consumer or KVM authority.
func AdmitAssembly(ctx context.Context) (*Launcher, error) {
	return admit(ctx, true)
}

func admit(ctx context.Context, assembly bool) (*Launcher, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "runtime namespace admission requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "runtime namespace admission canceled")
	}
	if err := platformAdmission(); err != nil {
		return nil, err
	}
	// /proc/self/exe is the kernel's reference to the running program, not an
	// authored pathname. Capture and seal it before executing the private child.
	file, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "opening running namespace bootstrap")
	}
	self, err := heldfile.FromReaderContext(ctx, file, heldfile.Options{Label: "runtime-namespace-bootstrap", Executable: true, MaxBytes: 256 << 20})
	closeErr := file.Close()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding namespace bootstrap")
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = self.Close()
		}
	}()
	if closeErr != nil {
		return nil, e.Wrap(e.CodeEngineCapability, closeErr, "closing namespace bootstrap source")
	}
	program, err := elf.NewFile(self)
	if err != nil {
		return nil, e.New(e.CodeEngineCapability, "namespace bootstrap requires static ELF")
	}
	if program.Class != elf.ELFCLASS64 || program.Data != elf.ELFDATA2LSB ||
		(runtime.GOARCH == "amd64" && program.Machine != elf.EM_X86_64) || (runtime.GOARCH == "arm64" && program.Machine != elf.EM_AARCH64) {
		return nil, e.New(e.CodeEngineCapability, "namespace bootstrap has a foreign executable format")
	}
	for _, segment := range program.Progs {
		if segment.Type == elf.PT_INTERP {
			return nil, e.New(e.CodeEngineCapability, "namespace bootstrap requires a dynamic loader")
		}
	}
	libraries, err := program.ImportedLibraries()
	if err != nil || len(libraries) != 0 {
		return nil, e.New(e.CodeEngineCapability, "namespace bootstrap has dynamic linkage")
	}
	if assembly {
		// heldfile intentionally permits execution only by its owner. Transfer
		// this private sealed copy to the mapped assembly ID before clone; the
		// child cannot impersonate host root to open a root-owned 0500 memfd.
		if err := self.OSFile().Chown(AssemblyHostID, AssemblyHostID); err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err, "assigning sealed assembly bootstrap to its mapped owner")
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var diagnostic bytes.Buffer
	probe := requestRecord{Mode: "probe", UID: 65534, GID: 65534}
	if assembly {
		probe.Mode, probe.UID, probe.GID = "probe-assembly", 0, 0
	}
	if err := executeChild(probeCtx, self, probe, nil, io.Discard, &diagnostic); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "native namespace admission probe failed: %s", strings.TrimSpace(diagnostic.String()))
	}
	accepted = true
	return &Launcher{state: &launcherState{executable: self, assembly: assembly}}, nil
}

func (launcher *Launcher) Close() error {
	if launcher == nil || launcher.state == nil {
		return nil
	}
	state := launcher.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.executable == nil {
		return nil
	}
	err := state.executable.Close()
	state.executable = nil
	return err
}

func (launcher *Launcher) Execute(ctx context.Context, request Request) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "runtime execution requires context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return e.New(e.CodeEngineCapability, "runtime execution requires an admitted deadline")
	}
	if launcher == nil || launcher.state == nil {
		return e.New(e.CodeEngineCapability, "runtime namespace admission is absent")
	}
	state := launcher.state
	// One owner serializes launch and revocation. Context-aware acquisition keeps
	// cancellation bounded even when another consumer owns the capability.
	for !state.mu.TryLock() {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for runtime namespace owner")
		case <-timer.C:
		}
	}
	defer state.mu.Unlock()
	if state.executable == nil {
		return e.New(e.CodeEngineCapability, "runtime namespace admission is closed")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime execution canceled")
	}
	if request.Root == nil {
		return e.New(e.CodeUsage, "runtime execution requires a held root directory")
	}
	record := requestRecord{Mode: "execute", UID: 65534, GID: 65534, RootFD: 5, Command: request.Command, Environment: request.Environment, Workdir: request.Workdir}
	if state.assembly {
		record.Mode, record.UID, record.GID = "assemble", 0, 0
	}
	descriptors := []*os.File{request.Root}
	for index, projection := range request.Projections {
		if projection.Directory == nil {
			return e.New(e.CodeUsage, "runtime projection is absent")
		}
		record.Projections = append(record.Projections, projectionRecord{FD: 6 + index, Target: projection.Target, Writable: projection.Writable})
		descriptors = append(descriptors, projection.Directory)
	}
	if request.KVM != nil {
		record.KVMFD = 5 + len(descriptors)
		descriptors = append(descriptors, request.KVM)
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	if err := state.executable.AssertUnchangedContext(ctx); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "revalidating namespace bootstrap")
	}
	return executeChild(ctx, state.executable, record, descriptors, request.Stdout, request.Stderr)
}

func encodeRequest(record requestRecord) ([]byte, error) {
	if err := validateRecord(record); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRequestBytes {
		return nil, e.New(e.CodeUsage, "runtime namespace request exceeds its bound")
	}
	return raw, nil
}
