// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"golang.org/x/sys/unix"
)

func readNetworkState(work string) (networkState, error) {
	var state networkState
	held, info, err := securepath.OpenRegular(filepath.Join(work, networkJournal))
	if err != nil {
		return state, err
	}
	defer held.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(held.Fd()), &st); err != nil {
		return state, err
	}
	if st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || st.Mode&0o7777 != 0o600 || info.Size() > 65536 {
		return state, fmt.Errorf("recovery journal must be a bounded private single-link file")
	}
	raw, err := io.ReadAll(io.LimitReader(held, 65537))
	if err != nil {
		return state, err
	}
	if len(raw) > 65536 {
		return state, fmt.Errorf("recovery journal exceeds bound")
	}
	if err = canonical.UnmarshalExact(raw, &state); err != nil {
		return state, err
	}
	if err = validateNetworkState(state, work); err != nil {
		return state, err
	}
	return state, nil
}

func validNetworkID(id networkFileID) bool { return id.Inode > 0 && id.Mode <= 0o7777 }
func validateNetworkState(s networkState, work string) error {
	if (s.Version != 3 && s.Version != 4) || s.Work != work || s.UID != uint32(os.Getuid()) || s.PID <= 0 {
		return fmt.Errorf("recovery journal version/work/owner differs")
	}
	owner := strings.TrimPrefix(s.Owner, "nvke-demo-")
	raw, err := hex.DecodeString(owner)
	if err != nil || len(raw) != 16 || s.Owner != "nvke-demo-"+hex.EncodeToString(raw) {
		return fmt.Errorf("invalid firewall owner")
	}
	boot := strings.Split(s.Boot, "-")
	if len(boot) != 5 || len(boot[0]) != 8 || len(boot[1]) != 4 || len(boot[2]) != 4 || len(boot[3]) != 4 || len(boot[4]) != 12 {
		return fmt.Errorf("invalid boot identity")
	}
	raw, err = hex.DecodeString(strings.Join(boot, ""))
	if err != nil || strings.Join(boot, "") != hex.EncodeToString(raw) {
		return fmt.Errorf("invalid boot identity")
	}
	mac, err := net.ParseMAC(s.BridgeMAC)
	if err != nil || len(mac) != 6 || mac[0]&3 != 2 {
		return fmt.Errorf("invalid bridge marker")
	}
	if mac.String() != s.BridgeMAC {
		return fmt.Errorf("noncanonical bridge marker")
	}
	if s.BridgeID != "" {
		index, err := strconv.Atoi(s.BridgeID)
		if err != nil || index <= 0 || strconv.Itoa(index) != s.BridgeID {
			return fmt.Errorf("invalid bridge index")
		}
	}
	if !validNetworkID(s.HelperID) || s.HelperID.Mode != 0o755 || s.HelperID.UID != 0 || s.HelperID.GID != 0 || !canonical.IsDigest(s.HelperHash) {
		return fmt.Errorf("unsupported original QEMU helper identity/digest")
	}
	if s.PolicyID != nil && (!validNetworkID(*s.PolicyID) || s.PolicyID.Mode != 0o644 || s.PolicyID.UID != 0 || s.PolicyID.GID != 0) {
		return fmt.Errorf("unsupported bridge policy identity")
	}
	if s.Staging != nil {
		if s.Staging.Path != filepath.Join(filepath.Dir(qemuPolicy), ".nvke-policy-"+owner) {
			return fmt.Errorf("unsupported root staging path")
		}
		if id := s.Staging.Binding; id != nil && (!validNetworkID(*id) || id.Mode != 0o600 || id.UID != 0 || id.GID != 0) {
			return fmt.Errorf("unsupported root staging binding")
		}
	}
	if len(s.Sysctls) != len(networkSysctls) {
		return fmt.Errorf("unsupported sysctl closure")
	}
	for _, key := range networkSysctls {
		if s.Sysctls[key] != "0" && s.Sysctls[key] != "1" {
			return fmt.Errorf("unsupported original sysctl")
		}
	}
	seen := map[string]bool{}
	for _, key := range s.SysctlIntents {
		if seen[key] || !slices.Contains(networkSysctls, key) || s.Sysctls[key] != "0" {
			return fmt.Errorf("unsupported sysctl intent")
		}
		seen[key] = true
	}
	// Reuse the constructor's IP admission, without opening work or generating IDs.
	if s.Version == 4 {
		if s.KindCIDR != "" || s.KindGateway != "" || s.BGPIntent {
			return fmt.Errorf("native host transport cannot carry Kind or BGP bindings")
		}
	} else if err := validateNetworkKind(s.KindCIDR); err != nil {
		return err
	}
	allowed := append(networkRules(s.Owner, s.KindCIDR), nativeBootstrapRules(s.Owner, s.KindCIDR)...)
	maxRules := 14 + len(nativeBootstrapRules(s.Owner, s.KindCIDR))
	if s.KindGateway != "" {
		if err := admittedKindGateway(s.KindCIDR, s.KindGateway); err != nil {
			return err
		}
		allowed = append(allowed, kindSimulatorRule(s.Owner, s.KindCIDR, s.KindGateway))
		maxRules++
	}
	if len(s.Rules) > maxRules {
		return fmt.Errorf("recovery firewall closure exceeds bound")
	}
	for i, row := range s.Rules {
		for _, old := range s.Rules[:i] {
			if row.Table == old.Table && row.Chain == old.Chain && slices.Equal(row.Args, old.Args) {
				return fmt.Errorf("duplicate firewall intent")
			}
		}
		exact := false
		for _, known := range allowed {
			if row.Table == known.Table && row.Chain == known.Chain && slices.Equal(row.Args, known.Args) {
				exact = true
				break
			}
		}
		if exact {
			continue
		}
		if s.Version == 4 || row.Table != "raw" || row.Chain != "PREROUTING" || len(row.Args) != 16 {
			return fmt.Errorf("unknown firewall intent")
		}
		port, err := strconv.Atoi(row.Args[13])
		if err != nil || admittedGuestTCP(s.KindCIDR, row.Args[9], []int{port}) != nil || !slices.Equal(row.Args, guestTCPRule(s.Owner, row.Args[9], port).Args) {
			return fmt.Errorf("unknown guest TCP intent")
		}
	}
	if len(s.Caps) > 2 {
		return fmt.Errorf("capability closure exceeds bound")
	}
	clear(seen)
	for _, row := range s.Caps {
		name := filepath.Base(row.Path)
		if filepath.Dir(row.Path) != work || seen[name] || networkCapabilities[name] != row.Capabilities || row.Capabilities == "" || !validNetworkID(row.Binding) || row.Binding.Mode != 0o700 || row.Binding.UID != uint32(os.Getuid()) || !canonical.IsDigest(row.Digest) {
			return fmt.Errorf("unsupported capability target/binding/digest")
		}
		seen[name] = true
	}
	if s.Finished && (s.BridgeIntent || s.BGPIntent || s.HelperGranted || s.PolicyID != nil || s.Staging != nil) {
		return fmt.Errorf("finished journal retains host grant intent")
	}
	return nil
}

// RecoverNetworkGuard authenticates only the original operator recovery journal.
// It never signals serialized PIDs or resumes builds. It supports version-3 Kind
// and version-4 native journals with their distinct admitted control sets.
func RecoverNetworkGuard(ctx context.Context, runner Runner) (*NetworkGuard, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if err := networkPlatform(); err != nil {
		return nil, err
	}
	for _, name := range networkTools {
		if _, err := commandPath(name, runner.Env); err != nil {
			return nil, err
		}
	}
	directory, work, err := privateNetworkDirectory(runner.Work)
	if err != nil {
		return nil, err
	}
	s, err := readNetworkState(work)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	g := &NetworkGuard{Work: work, Owner: s.Owner, BridgeMAC: s.BridgeMAC, BridgeID: s.BridgeID, KindCIDR: s.KindCIDR, BridgeIntent: s.BridgeIntent, BGPIntent: s.BGPIntent, runner: runner, directory: directory, boot: s.Boot, pid: s.PID, helperID: s.HelperID, helperHash: s.HelperHash, helperGranted: s.HelperGranted, policyID: s.PolicyID, staging: s.Staging, sysctls: s.Sysctls, sysctlIntents: s.SysctlIntents, rules: s.Rules, caps: s.Caps, finished: s.Finished, helperPath: qemuHelper, policyPath: qemuPolicy, cleaning: true, journal: true}
	g.kindGateway = s.KindGateway
	if err = g.acquireLock(); err != nil {
		g.releaseHandles()
		return nil, err
	}
	currentBoot, err := networkBootID()
	if err != nil {
		g.releaseHandles()
		return nil, err
	}
	if currentBoot == g.boot {
		err = unix.Kill(g.pid, 0) // Presence only. Never kill or trust a serialized PID.
		if !errors.Is(err, unix.ESRCH) {
			g.releaseHandles()
			return nil, fmt.Errorf("original PID is present or uninspectable; refusing recovery")
		}
	}
	if g.finished {
		return g, nil
	}
	if currentBoot == g.boot {
		// Outwait the protected helper's 25s TERM +5s KILL deadline after a crash.
		timer := time.NewTimer(40 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			g.releaseHandles()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err = g.requireRecoveryProducersIdle(ctx); err != nil {
		g.releaseHandles()
		return nil, err
	}
	if err = networkProbePort(false, 18080); err != nil {
		g.releaseHandles()
		return nil, err
	}
	return g, nil
}

func (g *NetworkGuard) requireRecoveryProducersIdle(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 {
			continue
		}
		path := filepath.Join("/proc", entry.Name(), "exe")
		var st unix.Stat_t
		err = unix.Stat(path, &st)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
			continue
		}
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
			var process unix.Stat_t
			if err = unix.Stat(filepath.Dir(path), &process); err != nil {
				if errors.Is(err, unix.ENOENT) {
					continue
				}
				return err
			}
			if process.Uid != uint32(os.Getuid()) {
				continue
			}
			raw, statErr := g.Command(ctx, []string{"stat", "--dereference", "--format=%d %i", "--", path}, true)
			if statErr != nil {
				return statErr
			}
			parts := strings.Fields(string(raw))
			if len(parts) != 2 {
				return fmt.Errorf("protected executable metadata unavailable")
			}
			device, devErr := strconv.ParseUint(parts[0], 10, 64)
			inode, inoErr := strconv.ParseUint(parts[1], 10, 64)
			if devErr != nil || inoErr != nil || strconv.FormatUint(device, 10) != parts[0] || strconv.FormatUint(inode, 10) != parts[1] {
				return fmt.Errorf("protected executable metadata malformed")
			}
			if executable := g.ownedExecutablePath(device, inode); executable != "" {
				return remainingProducerError(pid, executable)
			}
			continue
		}
		if err != nil {
			return err
		}
		id := networkStatIdentity(st)
		if executable := g.ownedExecutablePath(id.Device, id.Inode); executable != "" {
			return remainingProducerError(pid, executable)
		}
	}
	return nil
}
func remainingProducerError(pid int, executable string) error {
	return fmt.Errorf("private capability producer remains: PID %d, executable %q; inspect it with `ps -p %d -o pid=,args=` and stop/join the independently verified owned process before recovery; recovery did not signal it", pid, executable, pid)
}

func (g *NetworkGuard) ownedExecutablePath(device, inode uint64) string {
	for _, row := range g.caps {
		if row.Binding.Device == device && row.Binding.Inode == inode {
			return row.Path
		}
	}
	return ""
}
