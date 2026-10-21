// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/net/route"
)

func TestNativeAddressProjectionRequiresObservedOwnedBinding(t *testing.T) {
	network := nativeBridgeConfig{prefix: netip.MustParsePrefix("192.0.2.0/24"), gateway: netip.MustParseAddr("192.0.2.1"), last: netip.MustParseAddr("192.0.2.254"), dhcp: true}
	row := nativeAddressRow{Instance: "owned-instance", MAC: "52:54:00:12:34:56"}
	observed := nativeAddressObservation{row.MAC, netip.MustParseAddr("192.0.2.5")}
	for _, tc := range []struct {
		name      string
		rows      []nativeAddressRow
		neighbors []nativeAddressObservation
		want      string
		bad       bool
	}{
		{name: "no instance", neighbors: []nativeAddressObservation{observed}},
		{name: "no neighbor", rows: []nativeAddressRow{row}},
		{name: "observed", rows: []nativeAddressRow{row}, neighbors: []nativeAddressObservation{observed}, want: "0 52:54:00:12:34:56 192.0.2.5 * *\n"},
		{name: "foreign MAC", rows: []nativeAddressRow{row}, neighbors: []nativeAddressObservation{{"52:54:00:00:00:01", observed.IP}}},
		{name: "foreign address", rows: []nativeAddressRow{row}, neighbors: []nativeAddressObservation{{row.MAC, netip.MustParseAddr("198.51.100.5")}}, bad: true},
		{name: "gateway", rows: []nativeAddressRow{row}, neighbors: []nativeAddressObservation{{row.MAC, network.gateway}}, bad: true},
		{name: "ambiguous MAC", rows: []nativeAddressRow{row, row}, bad: true},
		{name: "ambiguous address", rows: []nativeAddressRow{row}, neighbors: []nativeAddressObservation{observed, {row.MAC, netip.MustParseAddr("192.0.2.6")}}, bad: true},
		{name: "invalid MAC", rows: []nativeAddressRow{{Instance: row.Instance, MAC: "malformed"}}, bad: true},
		{name: "too many owners", rows: []nativeAddressRow{row, row, row, row}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nativeAddressProjection(tc.rows, tc.neighbors, network, 1)
			if (err != nil) != tc.bad || err == nil && got != tc.want {
				t.Fatalf("projection=%q err=%v", got, err)
			}
		})
	}
}

func TestNativeAddressProjectionPreservesNICoPrimary(t *testing.T) {
	network := nativeBridgeConfig{prefix: netip.MustParsePrefix("192.0.2.0/24"), gateway: netip.MustParseAddr("192.0.2.1"), last: netip.MustParseAddr("192.0.2.254"), dhcp: true}
	primary := nativeAddressObservation{"52:54:00:12:34:56", netip.MustParseAddr("192.0.2.5")}
	other := nativeAddressObservation{primary.MAC, netip.MustParseAddr("192.0.2.6")}
	for _, tc := range []struct {
		name      string
		ips       string
		neighbors []nativeAddressObservation
		want      string
		bad       bool
	}{
		{name: "recorded primary with another address", ips: `["192.0.2.5"]`, neighbors: []nativeAddressObservation{primary, other}, want: "0 52:54:00:12:34:56 192.0.2.5 * *\n"},
		{name: "neighbor order cannot select another primary", ips: `["192.0.2.5"]`, neighbors: []nativeAddressObservation{other, primary}, want: "0 52:54:00:12:34:56 192.0.2.5 * *\n"},
		{name: "another subnet cannot replace the primary", ips: `["192.0.2.5"]`, neighbors: []nativeAddressObservation{primary, {primary.MAC, netip.MustParseAddr("198.51.100.5")}}, want: "0 52:54:00:12:34:56 192.0.2.5 * *\n"},
		{name: "missing primary is not replaced", ips: `["192.0.2.5"]`, neighbors: []nativeAddressObservation{other}},
		{name: "no neighbor is not a binding", ips: `["192.0.2.5"]`},
		{name: "unconfirmed remains ambiguous", ips: `[]`, neighbors: []nativeAddressObservation{primary, other}, bad: true},
		{name: "empty source column remains ambiguous", neighbors: []nativeAddressObservation{primary, other}, bad: true},
		{name: "invalid recorded primary", ips: `["invalid"]`, neighbors: []nativeAddressObservation{primary}, bad: true},
		{name: "foreign recorded primary", ips: `["198.51.100.5"]`, neighbors: []nativeAddressObservation{primary}, bad: true},
		{name: "malformed recorded JSON", ips: `[`, neighbors: []nativeAddressObservation{primary}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := nativeAddressRow{Instance: "owned-instance", MAC: primary.MAC, IPs: tc.ips}
			got, err := nativeAddressProjection([]nativeAddressRow{row}, tc.neighbors, network, 1)
			if (err != nil) != tc.bad || err == nil && got != tc.want {
				t.Fatalf("projection=%q err=%v", got, err)
			}
		})
	}
}

func TestNativeNeighborsOnlyUseKernelLinkRecordsOnSelectedInterface(t *testing.T) {
	addresses := make([]route.Addr, syscall.RTAX_MAX)
	addresses[syscall.RTAX_DST] = &route.Inet4Addr{IP: [4]byte{192, 0, 2, 5}}
	addresses[syscall.RTAX_GATEWAY] = &route.LinkAddr{Addr: []byte{0x52, 0x54, 0, 0x12, 0x34, 0x56}}
	for _, tc := range []struct {
		name         string
		index, flags int
		err          error
		want         int
	}{
		{"selected", 7, syscall.RTF_LLINFO, nil, 1},
		{"nonzero route-use counter", 7, syscall.RTF_LLINFO, syscall.Errno(639174), 1},
		{"foreign interface", 8, syscall.RTF_LLINFO, nil, 0},
		{"not a neighbor", 7, 0, nil, 0},
		{"rejected route", 7, syscall.RTF_LLINFO | syscall.RTF_REJECT, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nativeNeighborAddresses([]route.Message{&route.RouteMessage{Type: syscall.RTM_GET2, Index: tc.index, Flags: tc.flags, Err: tc.err, Addrs: addresses}}, 7)
			if len(got) != tc.want {
				t.Fatalf("accepted %d neighbors, want %d", len(got), tc.want)
			}
		})
	}
}

func TestNativeAddressObserverFailureCancelsAndJoins(t *testing.T) {
	ctx, fail := context.WithCancelCause(t.Context())
	defer fail(nil)
	owner := runNativeAddressObserver(ctx, func(context.Context) error { return errors.New("synthetic failed observation") }, fail)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("failed observation did not cancel runtime")
	}
	budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	err := owner.Stop(budget)
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability || !errors.Is(context.Cause(ctx), err) {
		t.Fatalf("lost observation failure: %v / %v", err, context.Cause(ctx))
	}
	if again := owner.Stop(budget); again != err {
		t.Fatal("joined failure changed on repeated stop")
	}
}

func TestNativeAddressObserverStopCancelsBlockingRead(t *testing.T) {
	entered := make(chan struct{})
	ctx, fail := context.WithCancelCause(t.Context())
	defer fail(nil)
	owner := runNativeAddressObserver(ctx, func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }, fail)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("observer did not start")
	}
	budget, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := owner.Stop(budget); err != nil {
		t.Fatal("read was not joined", err)
	}
	if ctx.Err() != nil {
		t.Fatal("normal stop canceled runtime")
	}
}

func TestNativeNeighborsRejectRoutingSocketReplies(t *testing.T) {
	for _, messageType := range []int{0, syscall.RTM_GET, syscall.RTM_ADD} {
		message := &route.RouteMessage{Type: messageType, Index: 7, Flags: syscall.RTF_LLINFO}
		if got := nativeNeighborAddresses([]route.Message{message}, 7); len(got) != 0 {
			t.Fatalf("non-snapshot message %d became an address", messageType)
		}
	}
}

func TestNativeAddressProjectionUsesRequestedTopologyBound(t *testing.T) {
	network := nativeBridgeConfig{prefix: netip.MustParsePrefix("192.0.2.0/24"), gateway: netip.MustParseAddr("192.0.2.1"), last: netip.MustParseAddr("192.0.2.254"), dhcp: true}
	for _, limit := range []int{3, 4} {
		for _, extra := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/extra-%v", limit, extra), func(t *testing.T) {
				count := limit
				if extra {
					count++
				}
				rows := make([]nativeAddressRow, count)
				for i := range rows {
					rows[i] = nativeAddressRow{Instance: fmt.Sprintf("owned-%d", i), MAC: fmt.Sprintf("52:54:00:12:34:%02x", i+1)}
				}
				got, err := nativeAddressProjection(rows, nil, network, limit)
				if (err != nil) != extra || got != "" {
					t.Fatalf("projection=%q err=%v", got, err)
				}
			})
		}
	}
}
