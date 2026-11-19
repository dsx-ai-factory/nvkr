// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/net/route"
)

func TestNativeAddressCaptureRetainsOnlyLatestPrivateOutput(t *testing.T) {
	for _, explicitDirectory := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-directory-%v", explicitDirectory), func(t *testing.T) {
			work, logs := privateTestDir(t), privateTestDir(t)
			latest := filepath.Join(logs, "latest.log")
			if err := privateWrite(latest, nil); err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(work, "capture")
			writeTenantFixture(t, program, []byte("#!/bin/sh\nset -eu\ntest \"$PWD\" = \"$EXPECTED_CWD\"\nprintf '%s' \"$1\"\nprintf '%s' \"$2\" >&2\nexit \"$3\"\n"), 0o700)
			var public bytes.Buffer
			runner := Runner{Work: work, Progress: &public, output: &public}
			cwd := work
			if explicitDirectory {
				cwd = privateTestDir(t)
				runner.directory = cwd
			}
			runner.Env = []string{"PATH=/usr/bin:/bin", "EXPECTED_CWD=" + cwd}
			for i, exit := range []int{0, 0, 7} {
				stdout, stderr := fmt.Sprintf("stdout-%d", i), fmt.Sprintf("private-stderr-%d", i)
				phase := fmt.Sprintf("native-address-%d", i)
				raw, err := captureNativeAddressObservation(t.Context(), runner, []string{program, stdout, stderr, fmt.Sprint(exit)}, phase, latest)
				if string(raw) != stdout || exit == 0 && err != nil || exit != 0 && CommandExitCode(err) != exit {
					t.Fatalf("stdout=%q err=%v", raw, err)
				}
				if exit != 0 {
					var command *CommandError
					if !errors.As(err, &command) || command.Phase != phase {
						t.Fatalf("lost command phase: %v", err)
					}
				}
				combined, readErr := os.ReadFile(latest)
				if readErr != nil || !strings.Contains(string(combined), stdout) || !strings.Contains(string(combined), stderr) || len(combined) != len(stdout)+len(stderr) {
					t.Fatalf("latest output=%q err=%v", combined, readErr)
				}
			}
			entries, err := os.ReadDir(logs)
			if err != nil || len(entries) != 1 || entries[0].Name() != "latest.log" {
				t.Fatalf("per-poll diagnostic files accumulated: %v %v", entries, err)
			}
			info, err := os.Stat(latest)
			if err != nil || info.Mode().Perm() != 0o600 || public.Len() != 0 {
				t.Fatalf("nonprivate diagnostic: %v %v public=%q", info, err, &public)
			}
			entries, err = os.ReadDir(work)
			if err != nil || len(entries) != 1 || entries[0].Name() != "capture" {
				t.Fatalf("capture wrote per-command logs: %v %v", entries, err)
			}
		})
	}
}

func TestNativeAddressCaptureCanceledBeforeLaunchPreservesLatest(t *testing.T) {
	work := privateTestDir(t)
	latest, marker := filepath.Join(work, "latest.log"), filepath.Join(work, "launched")
	if err := privateWrite(latest, []byte("previous meaningful output")); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(work, "capture")
	writeTenantFixture(t, program, []byte("#!/bin/sh\nprintf launched > \"$MARKER\"\n"), 0o700)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw, err := captureNativeAddressObservation(ctx, Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "MARKER=" + marker}}, []string{program}, "native-address-canceled", latest)
	if len(raw) != 0 || !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnjoined) {
		t.Fatalf("cancellation changed: stdout=%q err=%v", raw, err)
	}
	retained, readErr := os.ReadFile(latest)
	if readErr != nil || string(retained) != "previous meaningful output" {
		t.Fatalf("empty canceled poll erased evidence: %q %v", retained, readErr)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command launched: %v", err)
	}
}

func TestNativeAddressCaptureRefusesUnsafeLatestStorage(t *testing.T) {
	for _, scenario := range []string{"missing", "symlink", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			latest, target := filepath.Join(work, "latest.log"), filepath.Join(work, "foreign")
			if err := privateWrite(target, []byte("unchanged")); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "symlink":
				if err := os.Symlink(target, latest); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(latest, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			program := filepath.Join(work, "capture")
			writeTenantFixture(t, program, []byte("#!/bin/sh\nprintf partial\nprintf private-failure >&2\nexit 7\n"), 0o700)
			raw, err := captureNativeAddressObservation(t.Context(), Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}}, []string{program}, "native-address-storage", latest)
			var exit *exec.ExitError
			if string(raw) != "partial" || CommandExitCode(err) != -1 || !errors.As(err, &exit) || exit.ExitCode() != 7 {
				t.Fatalf("diagnostic failure hid original command failure: stdout=%q err=%v", raw, err)
			}
			retained, readErr := os.ReadFile(target)
			if readErr != nil || string(retained) != "unchanged" {
				t.Fatalf("unsafe target changed: %q %v", retained, readErr)
			}
			if scenario == "missing" {
				if _, err := os.Stat(latest); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing slot was adopted: %v", err)
				}
			}
		})
	}
}

func TestNativeAddressCaptureBoundsCombinedOutput(t *testing.T) {
	work := privateTestDir(t)
	latest := filepath.Join(work, "latest.log")
	if err := privateWrite(latest, nil); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Work: work, Env: []string{"NVKR_ADDRESS_CAPTURE_HELPER=1"}}
	raw, err := captureNativeAddressObservation(t.Context(), runner, []string{program, "-test.run=^TestNativeAddressCaptureHelperProcess$"}, "native-address-large", latest)
	if err == nil || CommandExitCode(err) != -1 || errors.Is(err, ErrUnjoined) || len(raw) != diagnosticLimit/2 || bytes.Count(raw, []byte("x")) != len(raw) {
		t.Fatalf("combined bound lost: stdout=%d err=%v", len(raw), err)
	}
	retained, readErr := os.ReadFile(latest)
	if readErr != nil || len(retained) != diagnosticLimit || !bytes.Contains(retained, []byte("x")) || !bytes.Contains(retained, []byte("y")) {
		t.Fatalf("combined output not bounded and retained: len=%d err=%v", len(retained), readErr)
	}
}

func TestNativeAddressCaptureHelperProcess(t *testing.T) {
	if os.Getenv("NVKR_ADDRESS_CAPTURE_HELPER") == "" {
		return
	}
	// Neither stream individually exceeds Runner's bound; only their combined
	// output does, so ignoring the caller-owned sink's result would hide failure.
	_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), diagnosticLimit/2))
	_, _ = os.Stderr.Write(bytes.Repeat([]byte("y"), diagnosticLimit/2+1))
	os.Exit(0)
}

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
