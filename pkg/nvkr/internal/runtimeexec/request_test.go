// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNamespaceRequestAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*requestRecord)
		code   e.Code
	}{
		{"valid", func(*requestRecord) {}, ""},
		{"root execution", func(r *requestRecord) { r.UID = 0 }, e.CodeEngineCapability},
		{"root group", func(r *requestRecord) { r.GID = 0 }, e.CodeEngineCapability},
		{"unknown mode", func(r *requestRecord) { r.Mode = "other" }, e.CodeUsage},
		{"probe with payload", func(r *requestRecord) { r.Mode = "probe" }, e.CodeUsage},
		{"root descriptor substitution", func(r *requestRecord) { r.RootFD = 6 }, e.CodeUsage},
		{"relative executable", func(r *requestRecord) { r.Command[0] = "tool" }, e.CodeUsage},
		{"unclean executable", func(r *requestRecord) { r.Command[0] = "/usr/../bin/tool" }, e.CodeUsage},
		{"argument NUL", func(r *requestRecord) { r.Command = append(r.Command, "bad\x00argument") }, e.CodeUsage},
		{"ambient workdir", func(r *requestRecord) { r.Workdir = "." }, e.CodeUsage},
		{"loader environment", func(r *requestRecord) { r.Environment = []string{"LD_PRELOAD=/inputs/payload"} }, e.CodeUsage},
		{"duplicate environment", func(r *requestRecord) { r.Environment = []string{"PATH=/usr/bin", "PATH=/inputs"} }, e.CodeUsage},
		{"environment NUL", func(r *requestRecord) { r.Environment = []string{"HOME=/tmp\x00"} }, e.CodeUsage},
		{"invalid environment name", func(r *requestRecord) { r.Environment = []string{"1BAD=value"} }, e.CodeUsage},
		{"host device projection", func(r *requestRecord) { r.Projections[0].Target = "/dev" }, e.CodeUsage},
		{"consumer input and work", func(r *requestRecord) {
			r.Projections = []projectionRecord{{FD: 6, Target: "/nvkr/input"}, {FD: 7, Target: "/nvkr/work", Writable: true}}
		}, ""},
		{"writable consumer input", func(r *requestRecord) { r.Projections[0].Target = "/nvkr/input" }, e.CodeUsage},
		{"hidden runtime plugins", func(r *requestRecord) { r.Projections[0].Target = "/nvkr/toolchain" }, e.CodeUsage},
		{"overlapping projections", func(r *requestRecord) {
			r.Projections = []projectionRecord{{FD: 6, Target: "/nvkr"}, {FD: 7, Target: "/nvkr/work", Writable: true}}
		}, e.CodeUsage},
		{"consumer host bootstrap", func(r *requestRecord) { r.Projections[0] = projectionRecord{FD: 6, Target: "/usr"} }, e.CodeUsage},
		{"assembly", func(r *requestRecord) { r.Mode, r.UID, r.GID = "assemble", 0, 0 }, ""},
		{"assembly KVM", func(r *requestRecord) { r.Mode, r.UID, r.GID, r.KVMFD = "assemble", 0, 0, 7 }, e.CodeEngineCapability},
		{"assembly bootstrap", func(r *requestRecord) {
			r.Mode, r.UID, r.GID = "assemble", 0, 0
			r.Projections[0] = projectionRecord{FD: 6, Target: "/usr"}
		}, ""},
		{"assembly writable bootstrap", func(r *requestRecord) {
			r.Mode, r.UID, r.GID = "assemble", 0, 0
			r.Projections[0] = projectionRecord{FD: 6, Target: "/usr", Writable: true}
		}, e.CodeUsage},
		{"projection descriptor substitution", func(r *requestRecord) { r.Projections[0].FD = 3 }, e.CodeUsage},
		{"repeated projection", func(r *requestRecord) {
			r.Projections = append(r.Projections, projectionRecord{FD: 7, Target: "/out", Writable: true})
		}, e.CodeUsage},
		{"KVM descriptor substitution", func(r *requestRecord) { r.KVMFD = 6 }, e.CodeUsage},
		{"oversized request", func(r *requestRecord) {
			r.Command = []string{"/bin/tool", strings.Repeat("x", 16000), strings.Repeat("x", 16000), strings.Repeat("x", 16000), strings.Repeat("x", 16000), strings.Repeat("x", 16000)}
		}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := requestRecord{Mode: "execute", UID: 65534, GID: 65534, RootFD: 5, Command: []string{"/bin/tool"}, Workdir: "/out",
				Projections: []projectionRecord{{FD: 6, Target: "/out", Writable: true}}}
			tc.mutate(&record)
			_, err := encodeRequest(record)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v; want %s", err, tc.code)
			}
		})
	}
}
