// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const (
	// Durable namespace for version-3 NVKE demo network journals.
	BridgeName     = "nvkevm0"
	GuestCIDR      = "172.30.0.0/24"
	GuestGateway   = "172.30.0.1"
	VIPCIDR        = "172.31.0.0/24"
	networkJournal = "network-state.json"
	qemuHelper     = "/usr/lib/qemu/qemu-bridge-helper"
	qemuPolicy     = "/etc/qemu/bridge.conf"
)

var networkSysctls = []string{"net.ipv4.ip_forward", "net.ipv4.fib_multipath_hash_policy"}
var networkCapabilities = map[string]string{"dnsmasq": "cap_net_bind_service,cap_net_admin,cap_net_raw=ep", "nico-sim": "cap_net_admin=ep"}
var networkTools = []string{"sudo", "timeout", "ip", "iptables", "dnsmasq", "setcap", "getcap", "virsh", "ps", "sysctl", "cp", "chmod", "install", "mv", "sha256sum", "unlink", "stat"}

const networkSudoProbe = "sudo -n -k timeout --signal=TERM --kill-after=5 25 true"

// Unattended commands ignore cached credentials without invalidating the user's
// timestamp. Interactive commands use the caller's preauthenticated session;
// sudo -n refuses expired credentials or a denied policy without replay.
func networkSudoCommand(args []string, interactive bool) []string {
	prefix := []string{"sudo", "-n"}
	if !interactive {
		prefix = append(prefix, "-k")
	}
	prefix = append(prefix, "timeout", "--signal=TERM", "--kill-after=5", "25")
	return append(prefix, args...)
}

type networkFileID struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
}
type networkRule struct {
	Table string   `json:"table"`
	Chain string   `json:"chain"`
	Args  []string `json:"args"`
}
type networkCapability struct {
	Path         string        `json:"path"`
	Binding      networkFileID `json:"binding"`
	Digest       string        `json:"digest"`
	Capabilities string        `json:"capabilities"`
}
type networkStaging struct {
	Path    string         `json:"path"`
	Binding *networkFileID `json:"binding"`
}
type networkState struct {
	Version       int                 `json:"version"`
	Work          string              `json:"work"`
	UID           uint32              `json:"uid"`
	Boot          string              `json:"boot"`
	PID           int                 `json:"pid"`
	Owner         string              `json:"owner"`
	KindCIDR      string              `json:"kindCIDR"`
	KindGateway   string              `json:"kindGateway,omitempty"`
	BridgeMAC     string              `json:"bridgeMAC"`
	BridgeID      string              `json:"bridgeID"`
	BridgeIntent  bool                `json:"bridgeIntent"`
	PolicyID      *networkFileID      `json:"policyID"`
	Staging       *networkStaging     `json:"staging"`
	HelperID      networkFileID       `json:"helperID"`
	HelperHash    string              `json:"helperHash"`
	HelperGranted bool                `json:"helperGranted"`
	Sysctls       map[string]string   `json:"sysctls"`
	SysctlIntents []string            `json:"sysctlIntents"`
	Rules         []networkRule       `json:"rules"`
	Caps          []networkCapability `json:"caps"`
	Finished      bool                `json:"finished"`
	BGPIntent     bool                `json:"bgpIntent"`
}

// NetworkGuard owns only this downstream demo's host controls. Exported fields
// are descriptive integration state, not NVKR receipts. The orchestration owner
// must serialize direct field changes and join all producers before Close.
type NetworkGuard struct {
	Work, Owner, BridgeMAC, BridgeID, KindCIDR string
	BridgeIntent, BGPIntent                    bool
	mu                                         sync.Mutex
	runner                                     Runner
	sequence                                   atomic.Uint64
	unjoined                                   atomic.Bool
	directory, lock                            *os.File
	boot                                       string
	pid                                        int
	helperID                                   networkFileID
	helperHash                                 string
	helperGranted                              bool
	policyID                                   *networkFileID
	staging                                    *networkStaging
	sysctls                                    map[string]string
	sysctlIntents                              []string
	rules                                      []networkRule
	kindGateway                                string
	caps                                       []networkCapability
	dns                                        *OwnedProcess
	cleaning, journal, finished                bool
	persistenceFailures                        []error
	// Test seams are private. Production always uses the source-owned boundary.
	capture                func(context.Context, []string, bool) ([]byte, error)
	stageObservationHook   func(context.Context) (networkFileID, error)
	checkpointHook         func() error
	stopDNSHook            func(context.Context) error
	helperPath, policyPath string
	lockPath               string
	probeExecutable        string
}

func networkPlatform() error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "local networking requires dedicated ARM64 Linux/Colima")
	}
	return nil
}

func privateNetworkDirectory(path string) (*os.File, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	if path == "" {
		return nil, "", fmt.Errorf("existing private work directory required")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, "", err
	}
	if resolved != absolute {
		return nil, "", fmt.Errorf("network work directory must not traverse symlinks")
	}
	held, info, err := securepath.OpenDirectory(absolute)
	if err != nil {
		return nil, "", err
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(held.Fd()), &st); err != nil || info.Mode().Perm() != 0o700 || st.Uid != uint32(os.Getuid()) {
		_ = held.Close()
		return nil, "", fmt.Errorf("network work directory must be current-user-owned mode0700")
	}
	return held, absolute, nil
}

func newNetworkGuard(runner Runner, kindCIDR string) (*NetworkGuard, error) {
	return newNetworkGuardScope(runner, kindCIDR, false)
}

func newNetworkGuardScope(runner Runner, kindCIDR string, native bool) (*NetworkGuard, error) {
	if native {
		if kindCIDR != "" {
			return nil, fmt.Errorf("native host transport must not declare a host Kind subnet")
		}
	} else if err := validateNetworkKind(kindCIDR); err != nil {
		return nil, err
	}
	directory, work, err := privateNetworkDirectory(runner.Work)
	if err != nil {
		return nil, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		_ = directory.Close()
		return nil, err
	}
	marker := append([]byte(nil), token[:6]...)
	marker[0] = (marker[0] | 2) & 0xfe
	return &NetworkGuard{Work: work, Owner: "nvke-demo-" + hex.EncodeToString(token[:]), BridgeMAC: net.HardwareAddr(marker).String(), KindCIDR: kindCIDR, runner: runner, directory: directory, pid: os.Getpid(), sysctls: map[string]string{}, helperPath: qemuHelper, policyPath: qemuPolicy}, nil
}
func validateNetworkKind(kindCIDR string) error {
	kind, err := netip.ParsePrefix(kindCIDR)
	if err != nil || kind.String() != kindCIDR || kind != kind.Masked() || !kind.Addr().Is4() || !kind.Addr().IsPrivate() || !networkLast(kind).IsPrivate() || kind.Overlaps(netip.MustParsePrefix(GuestCIDR)) || kind.Overlaps(netip.MustParsePrefix(VIPCIDR)) {
		return fmt.Errorf("kind network must be a distinct canonical private IPv4 range")
	}
	return nil
}

// NewNetworkGuard admits the host, resources, private workspace and kernel lock,
// then durably records an empty intent ledger before returning a Start obligation.
func NewNetworkGuard(ctx context.Context, runner Runner, kindCIDR, demoExecutable string) (result *NetworkGuard, resultErr error) {
	return admitNetworkGuard(ctx, runner, kindCIDR, demoExecutable, false)
}

// Native bootstrap transport originates on the host through its owned backend;
// there is no Kind bridge in the host namespace to admit or grant forwarding.
func admitNetworkGuard(ctx context.Context, runner Runner, kindCIDR, demoExecutable string, native bool) (result *NetworkGuard, resultErr error) {
	boundary := beginProgress(runner.Progress, "network-preflight", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("network preflight: %w", resultErr)
		}
		boundary.finish(resultErr)
	}()
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if err := networkPlatform(); err != nil {
		return nil, err
	}
	probeExecutable, err := admitNetworkProbeExecutable(ctx, demoExecutable)
	if err != nil {
		return nil, err
	}
	for _, name := range networkTools {
		if _, err := commandPath(name, runner.Env); err != nil {
			return nil, err
		}
	}
	g, err := newNetworkGuardScope(runner, kindCIDR, native)
	if err != nil {
		return nil, err
	}
	g.probeExecutable = probeExecutable
	if err = g.admit(ctx); err != nil {
		g.releaseHandles()
		return nil, err
	}
	return g, nil
}

func admitNetworkProbeExecutable(ctx context.Context, path string) (string, error) {
	if ctx == nil {
		return "", nvkrerr.New(nvkrerr.CodeUsage, "network probe executable requires a context")
	}
	if err := requireContext(ctx); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "network probe executable context")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nvkrerr.New(nvkrerr.CodeEngineCapability, "require explicitly selected absolute native demo executable for network admission")
	}
	resolved, err := resolveExisting(path)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "resolve network probe executable")
	}
	held, info, err := holdNetworkProbeExecutable(ctx, resolved)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "network probe executable context")
		}
		return "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "open network probe executable")
	}
	defer held.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(held.Fd()), &st); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "inspect network probe executable")
	}
	if info.Mode().Perm()&0o111 == 0 || st.Mode&0o6022 != 0 || st.Uid != 0 && st.Uid != uint32(os.Getuid()) {
		return "", nvkrerr.New(nvkrerr.CodeEngineCapability, "network probe requires an executable owned by root/current user without set-ID or group/other write permissions")
	}
	if err = ctx.Err(); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "network probe executable context")
	}
	return resolved, nil
}

// Reuse securepath's descriptor-relative O_NOFOLLOW walk, as the existing
// source-vendor helper admission does. The sudo child later executes by path;
// reject ancestors another principal could rename. This is trusted-host path
// admission, not immutable executable authority against root/the same user.
func holdNetworkProbeExecutable(ctx context.Context, path string) (*os.File, os.FileInfo, error) {
	current, _, err := securepath.OpenDirectory(string(filepath.Separator))
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if current != nil {
			_ = current.Close()
		}
	}()
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(current.Fd()), &stat); err != nil {
			return nil, nil, err
		}
		if err := networkProbeDirectoryProtection(stat.Uid, stat.Mode); err != nil {
			return nil, nil, err
		}
		directory := index != len(parts)-1
		next, info, err := securepath.OpenChild(current, part, directory)
		if err != nil {
			return nil, nil, err
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, nil, err
		}
		current = next
		if !directory {
			current = nil // Transfer the exact final descriptor to its caller.
			return next, info, nil
		}
	}
	return nil, nil, errors.New("network probe path has no executable member")
}

// Native Stat_t mode is uint16 on Darwin and uint32 on Linux. Keep that
// existing representation; the permission predicate is identical on both.
func networkProbeDirectoryProtection[T ~uint16 | ~uint32](uid uint32, mode T) error {
	trustedOwner := uid == 0 || uid == uint32(os.Getuid())
	// Root-owned sticky temporary directories protect each accepted root/user
	// child against other users' rename/unlink. Non-sticky writable ancestors
	// and foreign-owned intermediate children are never admitted.
	stickyRoot := uid == 0 && mode&unix.S_ISVTX != 0
	if mode&unix.S_IFMT != unix.S_IFDIR || !trustedOwner || mode&0o111 == 0 || mode&0o022 != 0 && !stickyRoot {
		return errors.New("network probe ancestors must be root/current-user owned and not replaceable by other principals")
	}
	return nil
}

// ProbeNetworkPrivilegedPorts is the fixed native helper: it accepts no target,
// configuration or workspace and performs no durable host mutation. The guard
// invokes it through its existing bounded sudo child before recording intent.
func ProbeNetworkPrivilegedPorts(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "network port probe context")
	}
	if err := networkPlatform(); err != nil {
		return err
	}
	return probeNetworkPrivilegedPorts(ctx, networkProbePort)
}

func probeNetworkPrivilegedPorts(ctx context.Context, probe func(bool, int) error) error {
	for _, row := range []struct {
		udp  bool
		port int
	}{{false, 53}, {true, 53}, {true, 67}} {
		if err := requireContext(ctx); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "network port probe context")
		}
		if err := probe(row.udp, row.port); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "bind demo port %d (UDP=%t)", row.port, row.udp)
		}
	}
	return nil
}

func (g *NetworkGuard) probePrivilegedPorts(ctx context.Context) error {
	if g.probeExecutable == "" {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "network port probe executable was not admitted")
	}
	if _, err := g.Command(ctx, []string{g.probeExecutable, "network-port-probe"}, true); err != nil {
		return fmt.Errorf("privileged DNS/DHCP port admission: %w", err)
	}
	return nil
}

func (g *NetworkGuard) acquireLock() error {
	path := fmt.Sprintf("/tmp/nvke-local-network-%d.lock", os.Getuid())
	if g.lockPath != "" {
		path = g.lockPath
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	held := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || st.Mode&0o7777 != 0o600 {
		_ = held.Close()
		return fmt.Errorf("network guard lock ownership differs")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = held.Close()
		return fmt.Errorf("another network guard is active: %w", err)
	}
	g.lock = held
	return nil
}

func networkBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if len(value) != 36 {
		return "", fmt.Errorf("invalid kernel boot identity")
	}
	return value, nil
}

func (g *NetworkGuard) admit(ctx context.Context) error {
	if err := g.acquireLock(); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(g.Work, networkJournal)); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("network journal exists or cannot be inspected; recover original work directory")
	}
	if _, err := g.Command(ctx, []string{"true"}, true); err != nil {
		if g.runner.sudoAuthentication() != nil {
			return fmt.Errorf("interactive networking authorization failed; authenticate in the original terminal and retry network recovery if controls remain; see docs/local-nvke-demo.md: %w", err)
		}
		return fmt.Errorf("required unattended networking authorization failed; check `%s` (ignores temporary cached credentials without clearing them); see docs/local-nvke-demo.md#2-install-prerequisites-on-the-ubuntu-execution-host: %w", networkSudoProbe, err)
	}
	var links []networkLink
	if err := g.inventory(ctx, []string{"ip", "-j", "link", "show"}, &links); err != nil {
		return err
	}
	for _, row := range links {
		if row.Name == BridgeName {
			return fmt.Errorf("refusing pre-existing demo bridge")
		}
	}
	if _, err := os.Lstat(g.policyPath); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("refusing pre-existing or uninspectable QEMU bridge policy")
	}
	id, digest, err := networkFile(ctx, g.helperPath, false)
	if err != nil {
		return err
	}
	if id.Mode != 0o755 || id.UID != 0 || id.GID != 0 {
		return fmt.Errorf("QEMU helper must initially be root-owned mode0755")
	}
	g.helperID, g.helperHash = id, digest
	if err = g.requireIdle(ctx); err != nil {
		return err
	}
	var routes []struct {
		Destination string `json:"dst"`
	}
	if err = g.inventory(ctx, []string{"ip", "-j", "route", "show", "table", "main"}, &routes); err != nil {
		return err
	}
	for _, route := range routes {
		if route.Destination == "" || route.Destination == "default" {
			continue
		}
		prefix, parseErr := netip.ParsePrefix(route.Destination)
		if parseErr != nil {
			address, addressErr := netip.ParseAddr(route.Destination)
			if addressErr != nil {
				return fmt.Errorf("unknown native route: %w", parseErr)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Overlaps(netip.MustParsePrefix(GuestCIDR)) || prefix.Overlaps(netip.MustParsePrefix(VIPCIDR)) {
			return fmt.Errorf("existing route overlaps guest/VIP ranges")
		}
	}
	var addresses []struct {
		Addresses []struct {
			Local string `json:"local"`
		} `json:"addr_info"`
	}
	if err = g.inventory(ctx, []string{"ip", "-j", "address", "show"}, &addresses); err != nil {
		return err
	}
	for _, link := range addresses {
		for _, address := range link.Addresses {
			if address.Local == "169.254.169.254" {
				return fmt.Errorf("metadata address already assigned")
			}
		}
	}
	if err = g.probePrivilegedPorts(ctx); err != nil {
		return err
	}
	for _, row := range []struct {
		udp  bool
		port int
	}{{false, 18080}, {false, 7777}, {false, 10179}} {
		if err = networkProbePort(row.udp, row.port); err != nil {
			return err
		}
	}
	for _, key := range networkSysctls {
		raw, readErr := g.Command(ctx, []string{"sysctl", "-n", key}, false)
		if readErr != nil {
			return readErr
		}
		value := strings.TrimSpace(string(raw))
		if value != "0" && value != "1" {
			return fmt.Errorf("unsupported host sysctl %s", key)
		}
		g.sysctls[key] = value
	}
	g.boot, err = networkBootID()
	if err != nil {
		return err
	}
	g.journal = true
	return g.checkpoint(ctx)
}

// Command bounds protected children inside sudo as well as at the user runner.
// Cleanup captures in memory and cannot depend on writable diagnostic storage.
func (g *NetworkGuard) Command(ctx context.Context, args []string, privileged bool) ([]byte, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if g.capture != nil {
		raw, err := g.capture(ctx, args, privileged)
		if errors.Is(err, ErrUnjoined) {
			g.unjoined.Store(true)
		}
		return raw, err
	}
	runner := g.runner
	if privileged {
		interactive, err := runner.authenticateSudo(ctx)
		if err != nil {
			if errors.Is(err, ErrUnjoined) {
				g.unjoined.Store(true)
			}
			return nil, fmt.Errorf("network sudo authentication: %w", err)
		}
		args = networkSudoCommand(args, interactive)
	}
	// Inventory and absence checks legitimately return nonzero status. Keep
	// their private numbered logs and errors, but report progress at the owning
	// setup/cleanup boundary only, after that status has been interpreted.
	runner.Progress = nil
	if g.cleaning {
		runner.Work = ""
	}
	raw, err := runner.Capture(ctx, args, fmt.Sprintf("network-%03d", g.sequence.Add(1)), 40*time.Second)
	if errors.Is(err, ErrUnjoined) {
		g.unjoined.Store(true)
	}
	return raw, err
}

func (g *NetworkGuard) inventory(ctx context.Context, args []string, out any) error {
	raw, err := g.Command(ctx, args, false)
	if err != nil {
		return err
	}
	if err = canonical.ValidateJSONDocument(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func networkFile(ctx context.Context, path string, private bool) (networkFileID, string, error) {
	if err := requireContext(ctx); err != nil {
		return networkFileID{}, "", err
	}
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return networkFileID{}, "", err
	}
	defer file.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &st); err != nil {
		return networkFileID{}, "", err
	}
	id := networkStatIdentity(st)
	if private && (st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0) {
		return networkFileID{}, "", fmt.Errorf("capability target must be a private single-link current-user file")
	}
	h := sha256.New()
	if _, err = transportarchive.CopyExact(ctx, h, file, info.Size()); err != nil {
		return networkFileID{}, "", err
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return networkFileID{}, "", fmt.Errorf("control file changed while hashing")
	}
	var afterStat unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &afterStat); err != nil {
		return networkFileID{}, "", err
	}
	if networkStatIdentity(afterStat) != id || afterStat.Nlink != st.Nlink {
		return networkFileID{}, "", fmt.Errorf("control file owner or links changed while hashing")
	}
	return id, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (g *NetworkGuard) snapshot() networkState {
	version := 3
	if g.KindCIDR == "" {
		version = 4
	}
	return networkState{Version: version, Work: g.Work, UID: uint32(os.Getuid()), Boot: g.boot, PID: g.pid, Owner: g.Owner, KindCIDR: g.KindCIDR, KindGateway: g.kindGateway, BridgeMAC: g.BridgeMAC, BridgeID: g.BridgeID, BridgeIntent: g.BridgeIntent, PolicyID: g.policyID, Staging: g.staging, HelperID: g.helperID, HelperHash: g.helperHash, HelperGranted: g.helperGranted, Sysctls: g.sysctls, SysctlIntents: g.sysctlIntents, Rules: g.rules, Caps: g.caps, Finished: g.finished, BGPIntent: g.BGPIntent}
}

func (g *NetworkGuard) Checkpoint(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.checkpoint(ctx)
}
func (g *NetworkGuard) checkpoint(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if g.checkpointHook != nil {
		return g.checkpointHook()
	}
	if !g.journal {
		return nil
	}
	if err := g.checkDirectory(); err != nil {
		return err
	}
	target := filepath.Join(g.Work, networkJournal)
	if _, err := os.Lstat(target); err == nil {
		old, readErr := readNetworkState(g.Work)
		if readErr != nil {
			return readErr
		}
		if old.Owner != g.Owner || old.Boot != g.boot || old.PID != g.pid || old.KindCIDR != g.KindCIDR || old.KindGateway != "" && old.KindGateway != g.kindGateway || old.BridgeMAC != g.BridgeMAC || old.HelperID != g.helperID || old.HelperHash != g.helperHash || !maps.Equal(old.Sysctls, g.sysctls) {
			return fmt.Errorf("network journal ownership or original host binding changed")
		}
		if err = g.checkDirectory(); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := g.snapshot()
	if err := validateNetworkState(state, g.Work); err != nil {
		return err
	}
	raw, err := canonical.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return fmt.Errorf("recovery journal exceeds bound")
	}
	temporary, name, err := securepath.CreateTemporarySibling(g.directory)
	if err != nil {
		return err
	}
	defer func() { _ = temporary.Close(); _ = unix.Unlinkat(int(g.directory.Fd()), name, 0) }()
	if _, err = temporary.Write(raw); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = unix.Renameat(int(g.directory.Fd()), name, int(g.directory.Fd()), networkJournal); err != nil {
		return err
	}
	return g.directory.Sync()
}

func (g *NetworkGuard) checkDirectory() error {
	if g.directory == nil {
		return fmt.Errorf("network workspace handle unavailable")
	}
	info, err := g.directory.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(g.Work)
	if err != nil || !os.SameFile(info, current) || current.Mode().Perm() != 0o700 {
		return fmt.Errorf("network workspace changed")
	}
	return nil
}
func (g *NetworkGuard) cleanupCheckpoint(ctx context.Context) {
	if err := g.checkpoint(ctx); err != nil {
		g.persistenceFailures = append(g.persistenceFailures, err)
	}
}

func networkRules(owner, kind string) []networkRule {
	var rows []networkRule
	for _, row := range [][2]string{{"udp", "67"}, {"udp", "53"}, {"tcp", "53"}, {"tcp", "7777"}, {"tcp", "10179"}, {"tcp", "18080"}} {
		rows = append(rows, networkRule{"filter", "INPUT", []string{"-i", BridgeName, "-p", row[0], "--dport", row[1], "-j", "ACCEPT"}})
	}
	rows = append(rows, networkRule{"filter", "FORWARD", []string{"-i", BridgeName, "-s", GuestCIDR, "-j", "ACCEPT"}}, networkRule{"filter", "FORWARD", []string{"-o", BridgeName, "-d", GuestCIDR, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}})
	if kind != "" {
		rows = append(rows, networkRule{"filter", "FORWARD", []string{"-o", BridgeName, "-s", kind, "-d", VIPCIDR, "-p", "tcp", "--dport", "6443", "-j", "ACCEPT"}})
	}
	rows = append(rows, networkRule{"nat", "POSTROUTING", []string{"-s", GuestCIDR, "!", "-d", GuestCIDR, "-j", "MASQUERADE"}})
	for _, row := range [][2]string{{"80", "7777"}, {"179", "10179"}} {
		rows = append(rows, networkRule{"nat", "PREROUTING", []string{"-i", BridgeName, "-d", GuestGateway, "-p", "tcp", "--dport", row[0], "-j", "REDIRECT", "--to-port", row[1]}})
	}
	for i := range rows {
		rows[i].Args = append([]string{"-m", "comment", "--comment", owner}, rows[i].Args...)
	}
	return rows
}

func (g *NetworkGuard) ruleExists(ctx context.Context, row networkRule) (bool, error) {
	_, err := g.Command(ctx, append([]string{"iptables", "-w", "5", "-t", row.Table, "-C", row.Chain}, row.Args...), true)
	if err == nil {
		return true, nil
	}
	if CommandExitCode(err) == 1 {
		return false, nil
	}
	return false, err
}
func (g *NetworkGuard) ruleCommand(ctx context.Context, row networkRule, verb string) error {
	_, err := g.Command(ctx, append([]string{"iptables", "-w", "5", "-t", row.Table, verb, row.Chain}, row.Args...), true)
	return err
}

type networkLink struct {
	Name    string `json:"ifname"`
	Index   int    `json:"ifindex"`
	Address string `json:"address"`
	Info    struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}

func (g *NetworkGuard) BridgeBinding(ctx context.Context) (string, error) {
	var rows []networkLink
	if err := g.inventory(ctx, []string{"ip", "-d", "-j", "link", "show"}, &rows); err != nil {
		return "", err
	}
	var found []networkLink
	for _, row := range rows {
		if row.Name == BridgeName {
			found = append(found, row)
		}
	}
	if len(found) == 0 {
		return "", nil
	}
	row := found[0]
	if len(found) != 1 || row.Index <= 0 || row.Address != g.BridgeMAC || row.Info.Kind != "bridge" || g.BridgeID != "" && strconv.Itoa(row.Index) != g.BridgeID {
		return "", fmt.Errorf("bridge identity/owner changed; refusing restoration")
	}
	return strconv.Itoa(row.Index), nil
}

func (g *NetworkGuard) Start(ctx context.Context) (resultErr error) {
	boundary := beginProgress(g.runner.Progress, "network-setup", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("network setup: %w", resultErr)
		}
		boundary.finish(resultErr)
	}()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cleaning || g.finished || g.BridgeIntent {
		return fmt.Errorf("network guard is not fresh")
	}
	g.BridgeIntent = true
	if err := g.checkpoint(ctx); err != nil {
		return err
	}
	if _, err := g.Command(ctx, []string{"ip", "link", "add", "name", BridgeName, "address", g.BridgeMAC, "type", "bridge"}, true); err != nil {
		return err
	}
	index, err := g.BridgeBinding(ctx)
	if err != nil || index == "" {
		return errors.Join(err, fmt.Errorf("created bridge readback unavailable"))
	}
	g.BridgeID = index
	if err = g.checkpoint(ctx); err != nil {
		return err
	}
	for _, address := range []string{GuestGateway + "/24", "169.254.169.254/32"} {
		if _, err = g.Command(ctx, []string{"ip", "address", "add", address, "dev", BridgeName}, true); err != nil {
			return err
		}
	}
	if _, err = g.Command(ctx, []string{"ip", "link", "set", BridgeName, "up"}, true); err != nil {
		return err
	}
	policy := filepath.Join(g.Work, "bridge-policy")
	if err = networkWriteExclusive(policy, []byte("allow "+BridgeName+"\n"), 0o600); err != nil {
		return err
	}
	if _, err = g.Command(ctx, []string{"install", "-d", "-m", "0755", filepath.Dir(g.policyPath)}, true); err != nil {
		return err
	}
	if _, err = os.Lstat(g.policyPath); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("bridge policy appeared; refusing replacement")
	}
	stage := filepath.Join(filepath.Dir(g.policyPath), ".nvke-policy-"+strings.TrimPrefix(g.Owner, "nvke-demo-"))
	if _, err = os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("root staging exists or cannot be inspected")
	}
	g.staging = &networkStaging{Path: stage}
	if err = g.checkpoint(ctx); err != nil {
		return err
	}
	if _, err = g.Command(ctx, []string{"cp", "--no-clobber", "--no-dereference", "--preserve=mode", policy, stage}, true); err != nil {
		return err
	}
	binding, err := g.stageBinding(ctx)
	if err != nil {
		return err
	}
	g.staging.Binding = &binding
	if err = g.checkpoint(ctx); err != nil {
		return err
	}
	if _, err = g.Command(ctx, []string{"chmod", "0644", stage}, true); err != nil {
		return err
	}
	policyBinding, err := g.stageBinding(ctx)
	if err != nil {
		return err
	}
	g.policyID = &policyBinding
	if err = g.checkpoint(ctx); err != nil {
		return err
	}
	if _, err = g.Command(ctx, []string{"mv", "-T", "--no-clobber", stage, g.policyPath}, true); err != nil {
		return err
	}
	if _, err = os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("staging remains after no-replace policy commit")
	}
	actual, _, err := networkFile(ctx, g.policyPath, false)
	if err != nil || actual != *g.policyID {
		return fmt.Errorf("policy changed during no-replace setup")
	}
	actual, digest, err := networkFile(ctx, g.helperPath, false)
	if err != nil || actual != g.helperID || digest != g.helperHash {
		return fmt.Errorf("QEMU helper changed before grant")
	}
	g.helperGranted = true
	if err = g.checkpoint(ctx); err != nil {
		return err
	}
	if _, err = g.Command(ctx, []string{"chmod", "4755", g.helperPath}, true); err != nil {
		return err
	}
	for _, key := range networkSysctls {
		if g.sysctls[key] == "1" {
			continue
		}
		g.sysctlIntents = append(g.sysctlIntents, key)
		if err = g.checkpoint(ctx); err != nil {
			return err
		}
		if _, err = g.Command(ctx, []string{"sysctl", "-w", key + "=1"}, true); err != nil {
			return err
		}
	}
	for _, row := range networkRules(g.Owner, g.KindCIDR) {
		exists, readErr := g.ruleExists(ctx, row)
		if readErr != nil {
			return readErr
		}
		if exists {
			return fmt.Errorf("fresh owned firewall rule exists")
		}
		g.rules = append(g.rules, row)
		if err = g.checkpoint(ctx); err != nil {
			return err
		}
		if err = g.ruleCommand(ctx, row, "-I"); err != nil {
			return err
		}
	}
	if err = os.Mkdir(filepath.Join(g.Work, "dhcp-hosts"), 0o700); err != nil {
		return err
	}
	if err = networkWriteExclusive(filepath.Join(g.Work, "leases"), nil, 0o600); err != nil {
		return err
	}
	dns, err := commandPath("dnsmasq", g.runner.Env)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(dns)
	if err != nil {
		return err
	}
	binary := filepath.Join(g.Work, "dnsmasq")
	if err = networkWriteExclusive(binary, raw, 0o700); err != nil {
		return err
	}
	if err = g.grant(ctx, binary, networkCapabilities["dnsmasq"]); err != nil {
		return err
	}
	args := []string{binary, "--keep-in-foreground", "--conf-file=/dev/null", "--interface=" + BridgeName, "--bind-interfaces", "--except-interface=lo", "--dhcp-range=172.30.0.10,172.30.0.254,255.255.255.0,30m", "--dhcp-hostsdir=" + filepath.Join(g.Work, "dhcp-hosts"), "--dhcp-leasefile=" + filepath.Join(g.Work, "leases"), "--dhcp-option=option:router," + GuestGateway, "--dhcp-option=option:dns-server," + GuestGateway, "--resolv-file=/etc/resolv.conf", "--no-hosts", "--address=/carbide-pxe.forge/" + GuestGateway, "--pid-file=", "--log-dhcp", "--log-facility=-", "--dhcp-ignore-clid"}
	g.dns, err = g.runner.Start(ctx, args, "dnsmasq")
	return err
}

func networkWriteExclusive(path string, raw []byte, mode uint32) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	if _, err = file.Write(raw); err != nil {
		return err
	}
	return file.Sync()
}

func (g *NetworkGuard) Grant(ctx context.Context, path, capabilities string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.grant(ctx, path, capabilities)
}
func (g *NetworkGuard) grant(ctx context.Context, path, capabilities string) error {
	if g.cleaning || g.finished || filepath.Dir(path) != g.Work || networkCapabilities[filepath.Base(path)] != capabilities || capabilities == "" {
		return fmt.Errorf("capability target must be this guard's declared private executable")
	}
	for _, row := range g.caps {
		if row.Path == path {
			return fmt.Errorf("duplicate capability target")
		}
	}
	binding, digest, err := networkFile(ctx, path, true)
	if err != nil {
		return err
	}
	if binding.Mode != 0o700 {
		return fmt.Errorf("private executable must be mode0700")
	}
	current, err := g.Command(ctx, []string{"getcap", path}, false)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(current)) != 0 {
		return fmt.Errorf("private capability target already has privileges")
	}
	g.caps = append(g.caps, networkCapability{path, binding, digest, capabilities})
	if err = g.checkpoint(ctx); err != nil {
		return err
	}
	if _, err = g.Command(ctx, []string{"setcap", capabilities, path}, true); err != nil {
		return err
	}
	_, err = g.Command(ctx, []string{"setcap", "-v", capabilities, path}, true)
	return err
}

func guestTCPRule(owner, address string, port int) networkRule {
	return networkRule{"raw", "PREROUTING", []string{"-m", "comment", "--comment", owner, "-i", BridgeName, "-s", GuestCIDR, "-d", address, "-p", "tcp", "--dport", strconv.Itoa(port), "-j", "ACCEPT"}}
}

func kindSimulatorRule(owner, kind, gateway string) networkRule {
	return networkRule{"filter", "INPUT", []string{"-m", "comment", "--comment", owner, "-s", kind, "-d", gateway + "/32", "-p", "tcp", "--dport", strconv.Itoa(simulatorAPIPort), "-j", "ACCEPT"}}
}

func admittedKindGateway(kind, gateway string) error {
	if err := validateNetworkKind(kind); err != nil {
		return err
	}
	prefix := netip.MustParsePrefix(kind)
	target, err := netip.ParseAddr(gateway)
	if err != nil || !target.Is4() || target.String() != gateway || !prefix.Contains(target) || target == prefix.Addr() || target == networkLast(prefix) {
		return fmt.Errorf("simulator grant requires the exact admitted Kind IPv4 gateway")
	}
	return nil
}

// AllowKindSimulator grants only the admitted Kind subnet's access to the
// simulator on its admitted host gateway. The caller supplies the gateway from
// the same Docker network admission as KindCIDR; no host-wide INPUT policy changes.
func (g *NetworkGuard) AllowKindSimulator(ctx context.Context, gateway string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := admittedKindGateway(g.KindCIDR, gateway); err != nil {
		return err
	}
	if g.cleaning || g.finished || !g.BridgeIntent || g.kindGateway != "" {
		return fmt.Errorf("simulator grant requires a fresh active owned bridge")
	}
	binding, err := g.BridgeBinding(ctx)
	if err != nil {
		return err
	}
	if binding == "" {
		return fmt.Errorf("owned bridge absent")
	}
	row := kindSimulatorRule(g.Owner, g.KindCIDR, gateway)
	exists, err := g.ruleExists(ctx, row)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("fresh simulator rule exists")
	}
	g.kindGateway = gateway
	g.rules = append(g.rules, row)
	if err = g.checkpoint(ctx); err != nil {
		return err
	}
	if err = g.ruleCommand(ctx, row, "-I"); err != nil {
		return err
	}
	exists, err = g.ruleExists(ctx, row)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("simulator insert did not survive readback")
	}
	return nil
}

func admittedGuestTCP(kind, address string, ports []int) error {
	prefix, err := netip.ParsePrefix(kind)
	if err != nil {
		return err
	}
	target, err := netip.ParseAddr(address)
	if err != nil || !target.Is4() || target.String() != address || !prefix.Contains(target) || target == prefix.Addr() || target == networkLast(prefix) || len(ports) == 0 {
		return fmt.Errorf("guest TCP requires one exact Kind host and SPIRE ports")
	}
	seen := map[int]bool{}
	for _, port := range ports {
		if (port != 30440 && port != 30443) || seen[port] {
			return fmt.Errorf("only unique SPIRE TCP ports30440/30443 are admitted")
		}
		seen[port] = true
	}
	return nil
}
func networkLast(prefix netip.Prefix) netip.Addr {
	a := prefix.Addr().As4()
	bits := prefix.Bits()
	for i := bits; i < 32; i++ {
		a[i/8] |= 1 << uint(7-i%8)
	}
	return netip.AddrFrom4(a)
}
func (g *NetworkGuard) withdrawRule(ctx context.Context, row networkRule) error {
	exists, err := g.ruleExists(ctx, row)
	if err != nil {
		return err
	}
	if exists {
		if err = g.ruleCommand(ctx, row, "-D"); err != nil {
			return err
		}
	}
	exists, err = g.ruleExists(ctx, row)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("owned firewall rule remains")
	}
	return nil
}

func (g *NetworkGuard) stageBinding(ctx context.Context) (networkFileID, error) {
	if g.stageObservationHook != nil {
		return g.stageObservationHook(ctx)
	}
	path := g.staging.Path
	var st unix.Stat_t
	err := unix.Lstat(path, &st)
	if err != nil {
		return networkFileID{}, err
	}
	id := networkStatIdentity(st)
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || id.UID != 0 || id.GID != 0 || id.Mode != 0o600 && id.Mode != 0o644 || g.staging.Binding != nil && (id.Device != g.staging.Binding.Device || id.Inode != g.staging.Binding.Inode) {
		return networkFileID{}, fmt.Errorf("root policy staging changed")
	}
	expected := sha256.Sum256([]byte("allow " + BridgeName + "\n"))
	raw, err := g.Command(ctx, []string{"sha256sum", path}, true)
	if err != nil {
		return networkFileID{}, err
	}
	if string(raw) != hex.EncodeToString(expected[:])+"  "+path+"\n" {
		return networkFileID{}, fmt.Errorf("root staging bytes differ")
	}
	return id, nil
}

func (g *NetworkGuard) StopDNS(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopDNS(ctx)
}
func (g *NetworkGuard) stopDNS(ctx context.Context) error {
	if g.stopDNSHook != nil {
		return g.stopDNSHook(ctx)
	}
	if g.dns == nil {
		return nil
	}
	if err := g.dns.Stop(ctx); err != nil {
		return err
	}
	g.dns = nil
	return nil
}
func (g *NetworkGuard) requireIdle(ctx context.Context) error {
	raw, err := g.Command(ctx, []string{"ps", "-eo", "comm="}, false)
	if err != nil {
		return err
	}
	if slices.Contains(strings.Fields(string(raw)), "nico-sim") {
		return fmt.Errorf("active simulator; refusing network restoration")
	}
	raw, err = g.Command(ctx, []string{"virsh", "-c", "qemu:///session", "list", "--all", "--name"}, false)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		return fmt.Errorf("active/unknown guest; refusing network restoration")
	}
	return nil
}
func (g *NetworkGuard) RequireIdle(ctx context.Context) error {
	if g.unjoined.Load() {
		return fmt.Errorf("%w: network command may remain active; retaining controls", ErrUnjoined)
	}
	return g.requireIdle(ctx)
}
func (g *NetworkGuard) SetCleaning(value bool) { g.mu.Lock(); defer g.mu.Unlock(); g.cleaning = value }

// Close stops/joins DNS first, verifies all guest producers are idle, and restores
// only exact held controls. Restoration does not require successful log writes.
func (g *NetworkGuard) Close(ctx context.Context) (resultErr error) {
	boundary := beginProgress(g.runner.Progress, "network-cleanup", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("network cleanup: %w", resultErr)
		}
		boundary.finish(resultErr)
	}()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.finished {
		g.releaseHandles()
		return errors.Join(g.persistenceFailures...)
	}
	g.cleaning = true
	defer g.releaseHandles()
	if err := g.stopDNS(ctx); err != nil {
		return err
	}
	if g.unjoined.Load() {
		return fmt.Errorf("%w: network command may remain active; retaining controls", ErrUnjoined)
	}
	if err := g.requireIdle(ctx); err != nil {
		return err
	}
	if g.BGPIntent {
		return fmt.Errorf("BGP restoration remains; refusing network restoration")
	}
	bridge := ""
	var err error
	if g.BridgeIntent {
		bridge, err = g.BridgeBinding(ctx)
		if err != nil {
			return err
		}
	}
	for i := len(g.caps) - 1; i >= 0; i-- {
		row := g.caps[i]
		id, digest, readErr := networkFile(ctx, row.Path, true)
		if readErr != nil || id != row.Binding || digest != row.Digest {
			return fmt.Errorf("capability target changed; refusing restoration")
		}
		raw, err := g.Command(ctx, []string{"getcap", row.Path}, false)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(raw)) != 0 {
			if _, err = g.Command(ctx, []string{"setcap", "-v", row.Capabilities, row.Path}, true); err != nil {
				return err
			}
			if _, err = g.Command(ctx, []string{"setcap", "-r", row.Path}, true); err != nil {
				return err
			}
		}
		raw, err = g.Command(ctx, []string{"getcap", row.Path}, false)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(raw)) != 0 {
			return fmt.Errorf("private capability remains")
		}
		g.cleanupCheckpoint(ctx)
	}
	for i := len(g.rules) - 1; i >= 0; i-- {
		if err = g.withdrawRule(ctx, g.rules[i]); err != nil {
			return err
		}
		g.cleanupCheckpoint(ctx)
	}
	for i := len(g.sysctlIntents) - 1; i >= 0; i-- {
		key := g.sysctlIntents[i]
		original := g.sysctls[key]
		raw, err := g.Command(ctx, []string{"sysctl", "-n", key}, false)
		if err != nil {
			return err
		}
		current := strings.TrimSpace(string(raw))
		if current != original && current != "1" {
			return fmt.Errorf("host sysctl changed; refusing restoration")
		}
		if current != original {
			if _, err = g.Command(ctx, []string{"sysctl", "-w", key + "=" + original}, true); err != nil {
				return err
			}
		}
		raw, err = g.Command(ctx, []string{"sysctl", "-n", key}, false)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(raw)) != original {
			return fmt.Errorf("original sysctl did not survive restoration")
		}
		g.cleanupCheckpoint(ctx)
	}
	if g.helperGranted {
		id, digest, err := networkFile(ctx, g.helperPath, false)
		want := g.helperID
		want.Mode = 0o4755
		if err != nil || id != g.helperID && id != want || digest != g.helperHash {
			return fmt.Errorf("privileged QEMU helper changed")
		}
		if _, err = g.Command(ctx, []string{"chmod", "0755", g.helperPath}, true); err != nil {
			return err
		}
		id, digest, err = networkFile(ctx, g.helperPath, false)
		if err != nil || id != g.helperID || digest != g.helperHash {
			return fmt.Errorf("original QEMU helper did not survive restoration")
		}
		g.helperGranted = false
		g.cleanupCheckpoint(ctx)
	}
	if g.policyID != nil {
		if _, err = os.Lstat(g.policyPath); err == nil {
			id, digest, readErr := networkFile(ctx, g.policyPath, false)
			want := sha256.Sum256([]byte("allow " + BridgeName + "\n"))
			if readErr != nil || id != *g.policyID || digest != "sha256:"+hex.EncodeToString(want[:]) {
				return fmt.Errorf("bridge policy changed")
			}
			if _, err = g.Command(ctx, []string{"unlink", g.policyPath}, true); err != nil {
				return err
			}
			if _, err = os.Lstat(g.policyPath); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("bridge policy remains")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		g.policyID = nil
		g.cleanupCheckpoint(ctx)
	}
	if g.staging != nil {
		if _, err = os.Lstat(g.staging.Path); err == nil {
			if _, err = g.stageBinding(ctx); err != nil {
				return err
			}
			if _, err = g.Command(ctx, []string{"unlink", g.staging.Path}, true); err != nil {
				return err
			}
			if _, err = os.Lstat(g.staging.Path); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("root staging remains")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	g.staging = nil
	g.cleanupCheckpoint(ctx)
	if bridge != "" {
		current, err := g.BridgeBinding(ctx)
		if err != nil {
			return err
		}
		if current != bridge {
			return fmt.Errorf("bridge changed during restoration")
		}
		if _, err = g.Command(ctx, []string{"ip", "link", "delete", BridgeName, "type", "bridge"}, true); err != nil {
			return err
		}
		current, err = g.BridgeBinding(ctx)
		if err != nil {
			return err
		}
		if current != "" {
			return fmt.Errorf("owned bridge remains")
		}
	}
	g.BridgeIntent = false
	g.BridgeID = ""
	g.finished = true
	g.cleanupCheckpoint(ctx)
	if len(g.persistenceFailures) != 0 {
		return fmt.Errorf("host controls restored but journal persistence failed: %w", errors.Join(g.persistenceFailures...))
	}
	return nil
}
func (g *NetworkGuard) releaseHandles() {
	if g.directory != nil {
		_ = g.directory.Close()
		g.directory = nil
	}
	if g.lock != nil {
		_ = g.lock.Close()
		g.lock = nil
	}
}
