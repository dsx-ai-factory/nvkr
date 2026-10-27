// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/net/route"
)

// NICo's bridge backend consumes a dnsmasq-shaped address file. Apple's vmnet
// owns DHCP on Mac. Project only kernel-observed neighbors belonging to MACs
// from this attempt's private NICo database; never invent a lease or an IP.
// This local diagnostic does not authenticate network peers or qualify a Run.
type nativeAddressOwner struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

type nativeAddressRow struct {
	Instance string `json:"instance_id"`
	MAC      string `json:"mac_address"`
	IPs      string `json:"ip_addresses_json"`
}
type nativeAddressObservation struct {
	MAC string
	IP  netip.Addr
}

func admitNativeAddresses(ctx context.Context, env []string) error {
	sqlite, err := commandPath("sqlite3", env)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native Mac address observation requires SQLite's read-only CLI")
	}
	raw, err := (Runner{Env: env}).Capture(ctx, []string{sqlite, "-safe", "-batch", "-init", "/dev/null", ":memory:", "SELECT json_array();"}, "native-address-sqlite-admission", 15*time.Second)
	if err != nil || strings.TrimSpace(string(raw)) != "[]" {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, errors.Join(err, errors.New("require SQLite JSON support")), "native Mac address observation")
	}
	if _, err := nativeNeighborMessages(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native Mac kernel neighbor observation")
	}
	return nil
}

func configureNativeAddresses(runner *Runner, simdir string) error {
	hosts := filepath.Join(simdir, "dhcp-hosts")
	if err := os.Mkdir(hosts, 0700); err != nil {
		return err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"NICO_VMS_DHCP_HOSTS_DIR": hosts, "NICO_VMS_DHCP_LEASE_FILE": filepath.Join(simdir, "observed-addresses")})
	return nil
}

func startNativeAddresses(ctx context.Context, runner Runner, simdir string, network nativeBridgeConfig, limit int, fail context.CancelCauseFunc) (*nativeAddressOwner, error) {
	sqlite, err := commandPath("sqlite3", runner.Env)
	if err != nil {
		return nil, err
	}
	index, err := nativeAddressInterface(ctx, network.gateway)
	if err != nil {
		return nil, err
	}
	diagnostics, err := nativeObservationRunner(ctx, runner, "native-address-observation-")
	if err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native Mac address source")
	}
	latest := filepath.Join(diagnostics.Work, "latest.log")
	if err := privateWrite(latest, nil); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native Mac address source")
	}
	sequence := 0
	observe := func(ctx context.Context) error {
		sequence++
		// SQLite owns its live WAL/locking and JSON encoding. No DB writes, custom
		// SQLite reader, QEMU argv parser, ARP text parser or DHCP packet code.
		query := `SELECT json_group_array(json_object('instance_id',instance_id,'mac_address',mac_address,'ip_addresses_json',ip_addresses_json)) FROM (SELECT instance_id,mac_address,ip_addresses_json FROM instance_interfaces WHERE mac_address <> '' ORDER BY instance_id);`
		raw, err := captureNativeAddressObservation(ctx, runner, []string{sqlite, "-safe", "-readonly", "-batch", "-init", "/dev/null", "-cmd", ".timeout 1000", filepath.Join(simdir, "nico.db"), query}, fmt.Sprintf("native-address-%d", sequence), latest)
		if err != nil {
			return err
		}
		var rows []nativeAddressRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		messages, err := nativeNeighborMessages(ctx)
		if err != nil {
			return err
		}
		addresses := nativeNeighborAddresses(messages, index)
		contents, err := nativeAddressProjection(rows, addresses, network, limit)
		if err != nil {
			return err
		}
		target := filepath.Join(simdir, "observed-addresses")
		old, err := os.ReadFile(target)
		if err == nil && string(old) == contents {
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		file, err := os.CreateTemp(simdir, ".observed-addresses-")
		if err != nil {
			return err
		}
		temporary := file.Name()
		defer func() { _ = os.Remove(temporary) }()
		_, writeErr := file.WriteString(contents)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return err
		}
		return os.Rename(temporary, target)
	}
	// Schema/tool compatibility is checked while the database is empty, before
	// the first VM. Every subsequent observation is bounded and owner-joined.
	if err := observe(ctx); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native Mac address source")
	}
	return runNativeAddressObserver(ctx, observe, fail), nil
}

func captureNativeAddressObservation(ctx context.Context, runner Runner, args []string, phase, latest string) ([]byte, error) {
	// Poll continuously without accumulating per-command files or exposing private
	// address data. Clearing diagnostic storage must not change the command cwd.
	if runner.directory == "" {
		runner.directory = runner.Work
	}
	runner.Work, runner.Progress = "", nil
	sink := &boundedDiagnostic{remaining: diagnosticLimit, capture: true}
	runner.output = sink
	raw, err := runner.Capture(ctx, args, phase, 10*time.Second)
	if errors.Is(err, ErrUnjoined) {
		return raw, err
	}
	// Only joined drains yield a complete combined capture. An empty canceled
	// poll cannot erase the last meaningful output; the original error owns status.
	combined, retentionErr := sink.result()
	var storeErr error
	if len(combined) != 0 {
		storeErr = privateReplace(latest, combined)
	}
	if diagnosticErr := errors.Join(retentionErr, storeErr); diagnosticErr != nil {
		return raw, &CommandError{Phase: phase, ExitCode: -1, Cause: errors.Join(err, diagnosticErr)}
	}
	return raw, err
}

func runNativeAddressObserver(ctx context.Context, observe func(context.Context) error, fail context.CancelCauseFunc) *nativeAddressOwner {
	child, cancel := context.WithCancel(ctx)
	owner := &nativeAddressOwner{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(owner.done)
		for {
			if err := pause(child, time.Second); err != nil {
				return
			}
			if err := observe(child); err != nil {
				if child.Err() == nil {
					owner.err = nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native Mac address observation")
					fail(owner.err)
				}
				return
			}
		}
	}()
	return owner
}

func (owner *nativeAddressOwner) Stop(ctx context.Context) error {
	if owner == nil {
		return nil
	}
	owner.cancel()
	select {
	case <-owner.done:
		return owner.err
	case <-ctx.Done():
		return errors.Join(ErrUnjoined, ctx.Err())
	}
}

func nativeAddressProjection(rows []nativeAddressRow, neighbors []nativeAddressObservation, network nativeBridgeConfig, limit int) (string, error) {
	if limit != 1 && limit != 1+nativeTenantNodeCount(false) && limit != 1+nativeTenantNodeCount(true) || len(rows) > limit {
		return "", errors.New("private native fixture exceeds its requested address owners")
	}
	owners := make(map[string]netip.Addr)
	for _, row := range rows {
		mac, err := net.ParseMAC(row.MAC)
		if !safePhase(row.Instance) || err != nil || len(mac) != 6 || mac[0]&1 != 0 || row.MAC == "00:00:00:00:00:00" {
			return "", errors.New("invalid private native interface identity")
		}
		key := mac.String()
		if _, exists := owners[key]; exists {
			return "", errors.New("ambiguous private native MAC")
		}
		var ips []string
		// Match NICo's interface reader: an empty column means no addresses.
		if row.IPs != "" {
			if err := json.Unmarshal([]byte(row.IPs), &ips); err != nil {
				return "", errors.New("invalid private native interface addresses")
			}
		}
		var primary netip.Addr
		for i, value := range ips {
			ip, err := netip.ParseAddr(value)
			if err != nil || !ip.Is4() || !network.prefix.Contains(ip) || ip.Compare(network.gateway) <= 0 || ip.Compare(network.last) > 0 {
				return "", errors.New("recorded native address outside the vmnet allocation range")
			}
			if i == 0 {
				primary = ip
			}
		}
		owners[key] = primary
	}
	matched := make(map[string]netip.Addr)
	for _, n := range neighbors {
		primary, owned := owners[n.MAC]
		if !owned {
			continue
		}
		// NICo confirms its first interface IP once, before phone_home/Ready.
		// Later aliases do not select a new primary. Publish that recorded IP
		// only while the kernel still observes it for the same owned MAC.
		// Missing observations remain absent; this is not a health guarantee.
		if primary.IsValid() && n.IP != primary {
			continue
		}
		if !n.IP.Is4() || !network.prefix.Contains(n.IP) || n.IP.Compare(network.gateway) <= 0 || n.IP.Compare(network.last) > 0 {
			return "", errors.New("observed native address outside the vmnet allocation range")
		}
		if previous, exists := matched[n.MAC]; exists && previous != n.IP {
			return "", errors.New("ambiguous native neighbor address")
		}
		matched[n.MAC] = n.IP
	}
	keys := make([]string, 0, len(matched))
	for key := range matched {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&out, "0 %s %s * *\n", key, matched[key])
	}
	return out.String(), nil
}

func nativeNeighborMessages(ctx context.Context) ([]route.Message, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	raw, err := route.FetchRIB(syscall.AF_INET, route.RIBType(syscall.NET_RT_DUMP2), 0)
	if err != nil {
		return nil, err
	}
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	return route.ParseRIB(route.RIBType(syscall.NET_RT_DUMP2), raw)
}

func nativeNeighborAddresses(messages []route.Message, index int) []nativeAddressObservation {
	var out []nativeAddressObservation
	for _, message := range messages {
		m, ok := message.(*route.RouteMessage)
		// NET_RT_DUMP2 returns rt_msghdr2, which has no errno field.
		// The generic Err field decodes its route-use counter. FetchRIB and
		// ParseRIB report snapshot failures; accept only the exact dump type.
		if !ok || m.Type != syscall.RTM_GET2 || m.Index != index || m.Flags&syscall.RTF_LLINFO == 0 || m.Flags&(syscall.RTF_REJECT|syscall.RTF_BLACKHOLE) != 0 || len(m.Addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		ip, ipOK := m.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		link, linkOK := m.Addrs[syscall.RTAX_GATEWAY].(*route.LinkAddr)
		if !ipOK || !linkOK || len(link.Addr) != 6 {
			continue
		}
		out = append(out, nativeAddressObservation{net.HardwareAddr(link.Addr).String(), netip.AddrFrom4(ip.IP)})
	}
	return out
}

func nativeAddressInterface(ctx context.Context, gateway netip.Addr) (int, error) {
	if err := requireContext(ctx); err != nil {
		return 0, err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return 0, err
	}
	found := 0
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			return 0, err
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				return 0, err
			}
			if prefix.Addr() != gateway {
				continue
			}
			if found != 0 {
				return 0, errors.New("ambiguous vmnet gateway interface")
			}
			found = iface.Index
		}
	}
	if found == 0 {
		return 0, errors.New("vmnet gateway interface is unavailable")
	}
	return found, nil
}
