// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestResolverConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"numeric addresses", "search private.example\nnameserver 127.0.0.53\nnameserver 2001:db8::1 # note\noptions ndots:5\n", "nameserver 127.0.0.53\nnameserver 2001:db8::1\n"},
		{"bounded servers", "nameserver 192.0.2.1\nnameserver 192.0.2.2\nnameserver 192.0.2.3\nnameserver 192.0.2.4\n", "nameserver 192.0.2.1\nnameserver 192.0.2.2\nnameserver 192.0.2.3\n"},
		{"name", "nameserver dns.example\n", ""},
		{"zone", "nameserver fe80::1%host0\n", ""},
		{"unspecified", "nameserver 0.0.0.0\n", ""},
		{"multicast", "nameserver 224.0.0.1\n", ""},
		{"absent", "search private.example\n", ""},
		{"oversized", strings.Repeat("#", 64<<10) + "\nnameserver 192.0.2.1\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolverConfiguration([]byte(tc.input)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutboundRequestCannotExposeHostExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*requestRecord)
		valid  bool
	}{
		{"fixed helper", func(*requestRecord) {}, true},
		{"arbitrary executable", func(r *requestRecord) { r.Command[0] = "/bin/sh" }, false},
		{"API socket", func(r *requestRecord) { r.Command = append(r.Command, "--api-socket=/run/api") }, false},
		{"loopback mapping", func(r *requestRecord) { r.Command[3] = "--enable-ipv6" }, false},
		{"authored environment", func(r *requestRecord) { r.Environment = append(r.Environment, "TOKEN=synthetic") }, false},
		{"work projection", func(r *requestRecord) {
			r.Projections = []projectionRecord{{FD: 8, Target: "/nvkr/work", Writable: true}}
		}, false},
		{"KVM", func(r *requestRecord) { r.KVMFD = 8 }, false},
		{"TAP slot", func(r *requestRecord) { r.NetworkFD = 8 }, false},
		{"readiness slot", func(r *requestRecord) { r.ReadyFD = 8 }, false},
		{"DNS options", func(r *requestRecord) { r.Resolver += "search private.example\n" }, false},
		{"missing DNS", func(r *requestRecord) { r.Resolver = "" }, false},
		{"working directory", func(r *requestRecord) { r.Workdir = "/nvkr/work" }, false},
		{"root identity", func(r *requestRecord) { r.UID = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := requestRecord{Mode: "outbound", UID: 65534, GID: 65534, RootFD: 5,
				NetworkFD: 6, ReadyFD: 7, Resolver: "nameserver 192.0.2.1\n", Command: outboundCommand(), Environment: outboundEnvironment(), Workdir: "/"}
			tc.mutate(&record)
			_, err := encodeRequest(record)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || (coded.Code != e.CodeUsage && coded.Code != e.CodeEngineCapability) {
				t.Fatalf("unsafe outbound invocation admitted: %v", err)
			}
		})
	}
}
