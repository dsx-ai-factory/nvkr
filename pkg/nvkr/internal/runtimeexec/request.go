// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package runtimeexec owns the native Linux namespace boundary. It is internal:
// its descriptor requests describe execution and cannot authenticate a produced
// runtime, Plan, Run, consumer, or evidence. The pipeline owner must join those
// authorities before issuing a request.
package runtimeexec

import (
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	EntryArgument = "internal-runtime-namespace"
	IsolationMode = "native-linux-private-rootfs+mount-pid-ipc-uts-net/v1"
	// AssemblyHostID is the first host ID mapped into the private assembly user
	// namespace. Host root is deliberately outside the entire mapping. These
	// IDs own only the per-attempt assembly scratch, never consumer inputs.
	AssemblyHostID  = 65536
	AssemblyIDCount = 65536
	maxRequestBytes = 64 << 10
)

// Projection exposes one already-held directory at a fixed root-relative
// location. Inputs are read-only; the Run owner selects writable outputs/scratch.
type Projection struct {
	Directory *os.File
	Target    string
	Writable  bool
}

type Request struct {
	Root           *os.File
	Projections    []Projection
	KVM            *os.File
	Command        []string
	Environment    []string
	Workdir        string
	Stdout, Stderr io.Writer
}

type projectionRecord struct {
	FD       int    `json:"fd"`
	Target   string `json:"target"`
	Writable bool   `json:"writable"`
}

type requestRecord struct {
	Mode        string             `json:"mode"`
	UID         int                `json:"uid"`
	GID         int                `json:"gid"`
	RootFD      int                `json:"rootFd"`
	KVMFD       int                `json:"kvmFd,omitempty"`
	Projections []projectionRecord `json:"projections,omitempty"`
	Command     []string           `json:"command,omitempty"`
	Environment []string           `json:"environment,omitempty"`
	Workdir     string             `json:"workdir,omitempty"`
}

func validateRecord(record requestRecord) error {
	assembly := record.Mode == "assemble" || record.Mode == "probe-assembly"
	wantID := 65534
	if assembly {
		wantID = 0
	}
	if record.UID != wantID || record.GID != wantID {
		return e.New(e.CodeEngineCapability, "runtime namespace requires its unprivileged execution IDs")
	}
	if record.Mode == "probe" || record.Mode == "probe-assembly" {
		if record.RootFD != 0 || record.KVMFD != 0 || len(record.Projections) != 0 || len(record.Command) != 0 || len(record.Environment) != 0 || record.Workdir != "" {
			return e.New(e.CodeUsage, "runtime admission probe contains execution inputs")
		}
		return nil
	}
	if (record.Mode != "execute" && record.Mode != "assemble") || record.RootFD != 5 || len(record.Projections) > 8 || len(record.Command) == 0 || len(record.Command) > 256 ||
		!absolutePath(record.Command[0]) || !absolutePath(record.Workdir) || len(record.Environment) > 128 {
		return e.New(e.CodeUsage, "invalid bounded runtime namespace request")
	}
	for _, argument := range record.Command {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) || len(argument) > 16<<10 {
			return e.New(e.CodeUsage, "invalid runtime command argument")
		}
	}
	seen := map[string]bool{}
	for _, variable := range record.Environment {
		name, value, ok := strings.Cut(variable, "=")
		if !ok || name == "" || seen[name] || strings.ContainsRune(value, 0) || !utf8.ValidString(variable) || len(variable) > 16<<10 ||
			strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") {
			return e.New(e.CodeUsage, "invalid or loader-sensitive runtime environment")
		}
		for index, ch := range name {
			if !(ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || index > 0 && ch >= '0' && ch <= '9') {
				return e.New(e.CodeUsage, "invalid runtime environment name")
			}
		}
		seen[name] = true
	}
	previous := ""
	for index, projection := range record.Projections {
		validTarget := projectionTarget(projection.Target)
		if assembly && projection.Target == "/usr" && !projection.Writable {
			validTarget = true
		}
		if projection.FD != 6+index || !validTarget || projection.Target <= previous {
			return e.New(e.CodeUsage, "runtime projections require sorted unique fixed mount points")
		}
		if previous != "" && strings.HasPrefix(projection.Target, previous+"/") {
			return e.New(e.CodeUsage, "runtime projections cannot hide one another")
		}
		if projection.Target == "/nvkr/input" && projection.Writable {
			return e.New(e.CodeUsage, "runtime consumer inputs must remain read-only")
		}
		previous = projection.Target
	}
	if assembly && record.KVMFD != 0 {
		return e.New(e.CodeEngineCapability, "runtime assembly cannot receive a KVM device")
	}
	if record.KVMFD != 0 && record.KVMFD != 6+len(record.Projections) {
		return e.New(e.CodeUsage, "runtime KVM descriptor is not in its admitted slot")
	}
	return nil
}

func absolutePath(value string) bool {
	return len(value) > 0 && len(value) <= 4096 && utf8.ValidString(value) && strings.HasPrefix(value, "/") &&
		path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n\\")
}

func projectionTarget(value string) bool {
	switch value {
	case "/src", "/out", "/inputs", "/work", "/nvkr", "/nvkr/input", "/nvkr/work":
		return true
	default:
		return false
	}
}
