// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func networkFixture(t *testing.T) *NetworkGuard {
	t.Helper()
	g, err := newNetworkGuard(Runner{Work: privateTestDir(t), Env: []string{"PATH=/usr/bin:/bin"}}, "172.18.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	g.boot = "a14e7158-1737-451e-abd5-3bc3f61955e3"
	g.pid = 2147483647
	g.helperID = networkFileID{Device: 1, Inode: 2, Mode: 0o755}
	g.helperHash = "sha256:" + strings.Repeat("a", 64)
	g.sysctls = map[string]string{networkSysctls[0]: "0", networkSysctls[1]: "0"}
	g.journal = true
	t.Cleanup(g.releaseHandles)
	return g
}

func TestNetworkKindAdmission(t *testing.T) {
	for _, row := range []struct {
		value string
		valid bool
	}{{"172.18.0.0/16", true}, {"10.2.0.0/16", true}, {"", false}, {"8.8.0.0/16", false}, {"172.30.0.0/24", false}, {"172.31.0.0/24", false}, {"172.18.0.1/16", false}, {"fd00::/64", false}, {"172.0.0.0/8", false}, {"10.0.0.0/7", false}, {"192.168.0.0/15", false}} {
		t.Run(row.value, func(t *testing.T) {
			err := validateNetworkKind(row.value)
			if (err == nil) != row.valid {
				t.Fatalf("err=%v, valid=%v", err, row.valid)
			}
		})
	}
}

func TestRecoveryProducerDiagnosticsMatchExactExecutable(t *testing.T) {
	g := &NetworkGuard{caps: []networkCapability{{Path: "/private/demo/dnsmasq", Binding: networkFileID{Device: 7, Inode: 19}}}}
	for _, row := range []struct {
		name          string
		device, inode uint64
		want          string
	}{
		{"exact", 7, 19, "/private/demo/dnsmasq"},
		{"different-device", 8, 19, ""},
		{"different-inode", 7, 20, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := g.ownedExecutablePath(row.device, row.inode); got != row.want {
				t.Fatalf("executable=%q, want %q", got, row.want)
			}
		})
	}
	err := remainingProducerError(123, "/private/demo/dnsmasq")
	for _, want := range []string{"PID 123", `"/private/demo/dnsmasq"`, "ps -p 123 -o pid=,args=", "recovery did not signal it"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}

func TestNetworkPlatformBeforeFilesystemOrCommands(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Skip("native admitted platform exercised by opt-in real E2E")
	}
	for _, recovery := range []bool{false, true} {
		writer := &recordedProgress{}
		var err error
		if recovery {
			_, err = RecoverNetworkGuard(t.Context(), Runner{Work: "/does-not-exist"})
		} else {
			_, err = NewNetworkGuard(t.Context(), Runner{Work: "/does-not-exist", Progress: writer}, "172.18.0.0/16", "/does-not-exist")
		}
		var coded *nvkrerr.Error
		if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
			t.Fatalf("platform admission=%v", err)
		}
		if !recovery && !slices.Equal(writer.events, phaseEvents("network-preflight", PhaseFailed)) {
			t.Fatalf("preflight boundary=%+v", writer.events)
		}
	}
}

func TestNetworkCheckpointRefusesAnotherValidJournalOwner(t *testing.T) {
	for _, field := range []string{"owner", "boot", "pid", "kind", "bridge-marker", "helper", "original-sysctl"} {
		t.Run(field, func(t *testing.T) {
			g := networkFixture(t)
			state := g.snapshot()
			switch field {
			case "owner":
				state.Owner = "nvke-demo-" + strings.Repeat("b", 32)
			case "boot":
				state.Boot = "a14e7158-1737-451e-abd5-3bc3f61955e4"
			case "pid":
				state.PID--
			case "kind":
				state.KindCIDR = "10.2.0.0/16"
			case "bridge-marker":
				state.BridgeMAC = "02:11:22:33:44:55"
			case "helper":
				state.HelperID.Inode++
			case "original-sysctl":
				state.Sysctls = map[string]string{networkSysctls[0]: "1", networkSysctls[1]: "0"}
			}
			raw, err := canonical.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(g.Work, networkJournal)
			if err = networkWriteExclusive(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = readNetworkState(g.Work); err != nil {
				t.Fatal("fixture must be an otherwise valid journal", err)
			}
			if err = g.Checkpoint(t.Context()); err == nil {
				t.Fatal("valid foreign journal overwritten")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("refusal changed foreign journal", err)
			}
		})
	}
}

func TestNetworkJournalUsesNVKENamespace(t *testing.T) {
	g := networkFixture(t)
	state := g.snapshot()
	if state.Version != 3 || BridgeName != "nvkevm0" || !strings.HasPrefix(state.Owner, "nvke-demo-") {
		t.Fatal("journal must use the NVKE namespace")
	}
	state.Staging = &networkStaging{Path: filepath.Join(filepath.Dir(qemuPolicy), ".nvke-policy-"+strings.TrimPrefix(state.Owner, "nvke-demo-"))}
	if err := validateNetworkState(state, g.Work); err != nil {
		t.Fatalf("version-3 journal rejected: %v", err)
	}
}

func TestUnsupportedNetworkJournalRefusesBeforeActions(t *testing.T) {
	for _, version := range []int{1, 2, 4} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			g := networkFixture(t)
			state := g.snapshot()
			state.Version = version
			raw, err := canonical.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(g.Work, networkJournal)
			if err := networkWriteExclusive(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			g.capture = func(context.Context, []string, bool) ([]byte, error) {
				t.Fatal("unsupported journal reached host commands")
				return nil, nil
			}
			if err := g.Start(t.Context()); err == nil {
				t.Fatal("unsupported journal accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("unsupported journal was changed", err)
			}
		})
	}
}

func TestNetworkJournalContracts(t *testing.T) {
	for _, row := range []struct {
		name   string
		mutate func(networkState) networkState
	}{{"valid", func(s networkState) networkState { return s }}, {"foreign-work", func(s networkState) networkState { s.Work += "x"; return s }}, {"foreign-owner", func(s networkState) networkState { s.UID++; return s }}, {"old-version", func(s networkState) networkState { s.Version = 2; return s }}, {"bad-digest", func(s networkState) networkState { s.HelperHash = s.HelperHash[7:]; return s }}, {"upper-digest", func(s networkState) networkState { s.HelperHash = strings.ToUpper(s.HelperHash); return s }}, {"bad-mac", func(s networkState) networkState { s.BridgeMAC = "00:00:00:00:00:00"; return s }}, {"bad-index", func(s networkState) networkState { s.BridgeID = "042"; return s }}, {"arbitrary-command", func(s networkState) networkState {
		s.Rules = []networkRule{{"filter", "INPUT", []string{"-j", "DROP"}}}
		return s
	}}, {"foreign-cap", func(s networkState) networkState {
		s.Caps = []networkCapability{{"/usr/bin/tool", networkFileID{Inode: 1, Mode: 0o700, UID: uint32(os.Getuid())}, s.HelperHash, "cap_net_admin=ep"}}
		return s
	}}, {"bad-sysctl", func(s networkState) networkState { s.SysctlIntents = []string{"kernel.unrelated"}; return s }}, {"duplicate-rule", func(s networkState) networkState {
		s.Rules = networkRules(s.Owner, s.KindCIDR)[:1]
		s.Rules = append(s.Rules, s.Rules[0])
		return s
	}}, {"wrong-spire-port", func(s networkState) networkState {
		s.Rules = []networkRule{guestTCPRule(s.Owner, "172.18.0.2", 80)}
		return s
	}}, {"finished-granted", func(s networkState) networkState { s.Finished = true; s.HelperGranted = true; return s }}} {
		t.Run(row.name, func(t *testing.T) {
			g := networkFixture(t)
			state := row.mutate(g.snapshot())
			raw, err := canonical.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err = networkWriteExclusive(filepath.Join(g.Work, networkJournal), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = readNetworkState(g.Work)
			if (err == nil) != (row.name == "valid") {
				t.Fatalf("journal=%v", err)
			}
		})
	}
}

func TestNetworkJournalBridgeMAC(t *testing.T) {
	for _, row := range []struct {
		mac   string
		valid bool
	}{
		{"02:11:aa:33:44:55", true},
		{"02:11:AA:33:44:55", false},
		{"02-11-aa-33-44-55", false},
		{"0211.aa33.4455", false},
		{"0211aa334455", false},
		{"02:11:aa:33:44:55:66:77", false},
		{"03:11:aa:33:44:55", false},
		{"00:11:aa:33:44:55", false},
		{"02:11:aa:33:44", false},
		{"02:11:aa:33:44:zz", false},
	} {
		t.Run(row.mac, func(t *testing.T) {
			guard := networkFixture(t)
			state := guard.snapshot()
			state.BridgeMAC = row.mac
			if err := validateNetworkState(state, guard.Work); (err == nil) != row.valid {
				t.Fatalf("bridge marker admission: %v", err)
			}
		})
	}
}

func TestNetworkJournalFilesystemAndJSONNegatives(t *testing.T) {
	for _, name := range []string{"shared", "symlink", "hardlink", "duplicate", "unknown", "oversized"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			if err := g.Checkpoint(t.Context()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(g.Work, networkJournal)
			switch name {
			case "shared":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				other := filepath.Join(g.Work, "other")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(g.Work, "other")); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				if err := os.WriteFile(path, []byte(`{"version":2,"version":2}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = append([]byte(`{"argv":["sudo","arbitrary"],`), raw[1:]...)
				if err = os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat(" ", 65537)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readNetworkState(g.Work); err == nil {
				t.Fatal("invalid journal accepted")
			}
		})
	}
}

func TestNetworkCheckpointPrecedesMutation(t *testing.T) {
	g := networkFixture(t)
	if err := g.Checkpoint(t.Context()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	g.capture = func(context.Context, []string, bool) ([]byte, error) { calls++; return nil, nil }
	g.checkpointHook = func() error { return unix.ENOSPC }
	if err := g.Start(t.Context()); !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatal("privilege mutation preceded persistence")
	}
	state, err := readNetworkState(g.Work)
	if err != nil {
		t.Fatal(err)
	}
	if state.BridgeIntent {
		t.Fatal("failed checkpoint committed bridge intent")
	}
	entries, err := os.ReadDir(g.Work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary journal survived: %v", entries)
	}
}

func TestNetworkCheckpointConcurrentSerialization(t *testing.T) {
	g := networkFixture(t)
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() { failures <- g.Checkpoint(t.Context()) })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readNetworkState(g.Work); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkCheckpointRejectsInvalidSnapshotWithoutReplacingJournal(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			g := networkFixture(t)
			path := filepath.Join(g.Work, networkJournal)
			var before []byte
			if existing {
				if err := g.Checkpoint(t.Context()); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			g.staging = &networkStaging{
				Path:    filepath.Join(filepath.Dir(qemuPolicy), ".nvke-policy-"+strings.TrimPrefix(g.Owner, "nvke-demo-")),
				Binding: &networkFileID{Device: 1, Inode: 3, Mode: 0o644},
			}
			if err := g.Checkpoint(t.Context()); err == nil || !strings.Contains(err.Error(), "unsupported root staging binding") {
				t.Fatalf("invalid snapshot result=%v", err)
			}
			after, err := os.ReadFile(path)
			if existing {
				if err != nil || string(after) != string(before) {
					t.Fatal("invalid snapshot replaced valid journal", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid snapshot created journal", err)
			}
			entries, err := os.ReadDir(g.Work)
			want := 0
			if existing {
				want = 1
			}
			if err != nil || len(entries) != want {
				t.Fatal("invalid snapshot left temporary files", entries, err)
			}
		})
	}
}

func TestNetworkStartPreservesStagingIdentityThroughPolicyModeTransition(t *testing.T) {
	for _, boundary := range []string{"after-chmod", "before-rename"} {
		t.Run(boundary, func(t *testing.T) {
			g := networkFixture(t)
			stage := filepath.Join(filepath.Dir(qemuPolicy), ".nvke-policy-"+strings.TrimPrefix(g.Owner, "nvke-demo-"))
			for _, path := range []string{qemuPolicy, stage} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Skipf("fixture refuses existing or uninspectable host control %s: %v", path, err)
				}
			}
			stop := errors.New("intentional stop before host publication")
			modeChanged, renamed := false, false
			observations := 0
			g.stageObservationHook = func(context.Context) (networkFileID, error) {
				observations++
				id := networkFileID{Device: 1, Inode: 3, Mode: 0o600}
				if modeChanged {
					if boundary == "after-chmod" {
						return networkFileID{}, stop
					}
					id.Mode = 0o644
				}
				return id, nil
			}
			g.capture = func(_ context.Context, args []string, _ bool) ([]byte, error) {
				switch args[0] {
				case "ip":
					if slices.Equal(args, []string{"ip", "-d", "-j", "link", "show"}) {
						return []byte(fmt.Sprintf(`[{"ifname":%q,"ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, BridgeName, g.BridgeMAC)), nil
					}
				case "chmod":
					if !slices.Equal(args, []string{"chmod", "0644", stage}) {
						t.Fatalf("unexpected protected mode change: %v", args)
					}
					state, err := readNetworkState(g.Work)
					if err != nil || state.Staging == nil || state.Staging.Binding == nil || state.Staging.Binding.Mode != 0o600 {
						t.Fatal("mode transition lacks valid staging checkpoint", err)
					}
					modeChanged = true
				case "mv":
					renamed = true
					return nil, stop
				}
				return nil, nil
			}
			if err := g.Start(t.Context()); !errors.Is(err, stop) {
				t.Fatalf("Start did not reach selected publication boundary: %v", err)
			}
			state, err := readNetworkState(g.Work)
			if err != nil || state.Staging == nil || state.Staging.Binding == nil || state.Staging.Binding.Mode != 0o600 {
				t.Fatal("staging identity was corrupted by policy mode transition", err)
			}
			if !modeChanged || observations != 2 || renamed != (boundary == "before-rename") {
				t.Fatalf("transition modeChanged=%v observations=%d renamed=%v", modeChanged, observations, renamed)
			}
			if boundary == "before-rename" {
				if state.PolicyID == nil || state.PolicyID.Mode != 0o644 || state.PolicyID.Device != state.Staging.Binding.Device || state.PolicyID.Inode != state.Staging.Binding.Inode || g.policyID == g.staging.Binding {
					t.Fatal("published-policy binding does not independently retain the same inode")
				}
			} else if state.PolicyID != nil {
				t.Fatal("failed policy observation committed a publication binding")
			}
		})
	}
}

func TestNetworkBridgeBindingContracts(t *testing.T) {
	for _, name := range []string{"valid", "ambiguous-created", "absent", "foreign-mac", "foreign-kind", "foreign-index", "duplicate", "inventory-failed"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			g.BridgeID = "42"
			raw := fmt.Sprintf(`[{"ifname":"nvkevm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, g.BridgeMAC)
			switch name {
			case "ambiguous-created":
				g.BridgeID = ""
			case "absent":
				raw = "[]"
			case "foreign-mac":
				raw = strings.ReplaceAll(raw, g.BridgeMAC, "02:00:00:00:00:00")
			case "foreign-kind":
				raw = strings.ReplaceAll(raw, `"info_kind":"bridge"`, `"info_kind":"veth"`)
			case "foreign-index":
				g.BridgeID = "43"
			case "duplicate":
				raw = raw[:len(raw)-1] + "," + raw[1:]
			}
			g.capture = func(context.Context, []string, bool) ([]byte, error) {
				if name == "inventory-failed" {
					return nil, &CommandError{ExitCode: 1, Cause: errors.New("no inventory")}
				}
				return []byte(raw), nil
			}
			binding, err := g.BridgeBinding(t.Context())
			if name == "absent" {
				if err != nil || binding != "" {
					t.Fatal(binding, err)
				}
			} else if name == "valid" || name == "ambiguous-created" {
				if err != nil || binding != "42" {
					t.Fatal(binding, err)
				}
			} else if err == nil {
				t.Fatal("unowned/unknown bridge accepted")
			}
		})
	}
}

func TestNetworkRuleAbsenceRequiresJoinedExactExitOne(t *testing.T) {
	for _, row := range []struct {
		name   string
		err    error
		absent bool
	}{{"exit1", &CommandError{ExitCode: 1, Cause: errors.New("missing")}, true}, {"exit2", &CommandError{ExitCode: 2, Cause: errors.New("denied")}, false}, {"unjoined", fmt.Errorf("%w: pending", ErrUnjoined), false}, {"timeout", &CommandError{ExitCode: 1, Cause: context.DeadlineExceeded}, false}} {
		t.Run(row.name, func(t *testing.T) {
			g := networkFixture(t)
			g.capture = func(context.Context, []string, bool) ([]byte, error) { return nil, row.err }
			exists, err := g.ruleExists(t.Context(), networkRules(g.Owner, g.KindCIDR)[0])
			if exists || (err == nil) != row.absent {
				t.Fatalf("exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestNetworkRuleOwnerAndGatewayBinding(t *testing.T) {
	rows := networkRules("private-owner", "172.18.0.0/16")
	if len(rows) != 12 {
		t.Fatal(len(rows))
	}
	redirects := 0
	for _, row := range rows {
		if !slices.Equal(row.Args[:4], []string{"-m", "comment", "--comment", "private-owner"}) {
			t.Fatal(row)
		}
		if slices.Contains(row.Args, "REDIRECT") {
			redirects++
			at := slices.Index(row.Args, "-d")
			if at < 0 || row.Args[at+1] != GuestGateway {
				t.Fatal("unscoped redirect")
			}
		}
	}
	if redirects != 2 {
		t.Fatal(redirects)
	}
}

func TestNetworkKindSimulatorInvalidBeforeWork(t *testing.T) {
	for _, gateway := range []string{"", "8.8.8.8", "172.19.0.1", "172.18.0.0", "172.18.255.255", "172.18.0.1/32", "::ffff:172.18.0.1"} {
		t.Run(gateway, func(t *testing.T) {
			g := networkFixture(t)
			g.BridgeIntent = true
			calls := 0
			g.capture = func(context.Context, []string, bool) ([]byte, error) { calls++; return nil, nil }
			if err := g.AllowKindSimulator(t.Context(), gateway); err == nil {
				t.Fatal("invalid simulator gateway accepted")
			}
			if calls != 0 || len(g.rules) != 0 || g.kindGateway != "" {
				t.Fatal("invalid grant performed work")
			}
		})
	}
}

func TestNetworkKindSimulatorJournalExactClosure(t *testing.T) {
	for _, name := range []string{"without-gateway", "valid", "gateway-outside", "unbound-rule", "wrong-source", "wrong-target", "wrong-port", "foreign-owner", "duplicate", "arbitrary-interface"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			state := g.snapshot()
			state.Rules = networkRules(state.Owner, state.KindCIDR)
			state.Rules = append(state.Rules, guestTCPRule(state.Owner, "172.18.0.2", 30440), guestTCPRule(state.Owner, "172.18.0.2", 30443))
			if name != "without-gateway" {
				state.KindGateway = "172.18.0.1"
				state.Rules = append(state.Rules, kindSimulatorRule(state.Owner, state.KindCIDR, state.KindGateway))
			}
			switch name {
			case "gateway-outside":
				state.KindGateway = "172.19.0.1"
			case "unbound-rule":
				state.KindGateway = ""
			case "wrong-source":
				state.Rules[14].Args[5] = "0.0.0.0/0"
			case "wrong-target":
				state.Rules[14].Args[7] = "172.18.0.0/16"
			case "wrong-port":
				state.Rules[14].Args[11] = "18080"
			case "foreign-owner":
				state.Rules[14].Args[3] = "foreign"
			case "duplicate":
				state.Rules = append(state.Rules[:14], state.Rules[13])
			case "arbitrary-interface":
				state.Rules[14].Args = append(state.Rules[14].Args, "-i", "eth0")
			}
			raw, err := canonical.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if name == "without-gateway" && strings.Contains(string(raw), "kindGateway") {
				t.Fatal("journal without a gateway gained an empty field")
			}
			if err = networkWriteExclusive(filepath.Join(g.Work, networkJournal), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = readNetworkState(g.Work)
			if (err == nil) != (name == "without-gateway" || name == "valid") {
				t.Fatalf("journal accepted=%v: %v", err == nil, err)
			}
		})
	}
}

func TestNetworkKindSimulatorIntentAndWithdrawal(t *testing.T) {
	for _, name := range []string{"success", "ambiguous-insert", "lost-readback", "cancel", "unjoined", "checkpoint-failure"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			g.BridgeIntent = true
			inserted, inserts, checks := false, 0, 0
			row := kindSimulatorRule(g.Owner, g.KindCIDR, "172.18.0.1")
			g.capture = func(_ context.Context, args []string, privileged bool) ([]byte, error) {
				if args[0] == "ip" {
					return []byte(fmt.Sprintf(`[{"ifname":"nvkevm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, g.BridgeMAC)), nil
				}
				if !privileged || args[0] != "iptables" || args[4] != row.Table || args[6] != row.Chain || !slices.Equal(args[7:], row.Args) {
					t.Fatalf("unscoped firewall command: %v", args)
				}
				switch args[5] {
				case "-C":
					checks++
					if name == "unjoined" {
						return nil, ErrUnjoined
					}
					if inserted && !(name == "lost-readback" && checks == 2) {
						return nil, nil
					}
					return nil, &CommandError{ExitCode: 1, Cause: errors.New("absent")}
				case "-I":
					state, err := readNetworkState(g.Work)
					if err != nil || state.KindGateway != "172.18.0.1" || len(state.Rules) != 1 || !slices.Equal(state.Rules[0].Args, row.Args) {
						t.Fatal("firewall mutation preceded durable intent", err)
					}
					inserts++
					inserted = true
					if name == "ambiguous-insert" {
						return nil, errors.New("ambiguous joined insertion")
					}
				case "-D":
					inserted = false
				default:
					t.Fatalf("unexpected firewall verb: %v", args)
				}
				return nil, nil
			}
			ctx := t.Context()
			if name == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if name == "checkpoint-failure" {
				g.checkpointHook = func() error { return errors.New("checkpoint failed") }
			}
			err := g.AllowKindSimulator(ctx, "172.18.0.1")
			if (err == nil) != (name == "success") {
				t.Fatalf("grant=%v", err)
			}
			if name == "cancel" || name == "unjoined" || name == "checkpoint-failure" {
				if inserts != 0 {
					t.Fatal("failed admission/checkpoint inserted rule")
				}
				if name == "unjoined" && !errors.Is(err, ErrUnjoined) {
					t.Fatal("lost unjoined error")
				}
				return
			}
			if len(g.rules) != 1 || !inserted {
				t.Fatal("lost grant intent")
			}
			if err = g.AllowKindSimulator(t.Context(), "172.18.0.1"); err == nil || inserts != 1 {
				t.Fatal("duplicate grant accepted")
			}
			if err = g.withdrawRule(t.Context(), g.rules[0]); err != nil || inserted {
				t.Fatal("exact recovery withdrawal failed", err)
			}
			g.kindGateway = "172.18.0.2"
			if err = g.Checkpoint(t.Context()); err == nil {
				t.Fatal("gateway binding rewritten")
			}
		})
	}
}

func TestNetworkRestorationRequiresIdleAndJoined(t *testing.T) {
	for _, name := range []string{"simulator", "domain", "dns-unjoined", "helper-unjoined", "bgp-intent"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			calls := [][]string{}
			g.capture = func(_ context.Context, args []string, _ bool) ([]byte, error) {
				calls = append(calls, slices.Clone(args))
				if name == "simulator" && args[0] == "ps" {
					return []byte("nico-sim\n"), nil
				}
				if name == "domain" && args[0] == "virsh" {
					return []byte("foreign-vm\n"), nil
				}
				return nil, nil
			}
			if name == "dns-unjoined" {
				g.stopDNSHook = func(context.Context) error { return ErrUnjoined }
			}
			if name == "helper-unjoined" {
				g.unjoined.Store(true)
			}
			if name == "bgp-intent" {
				g.BGPIntent = true
			}
			err := g.Close(t.Context())
			if err == nil {
				t.Fatal("unsafe restoration accepted")
			}
			if strings.Contains(name, "unjoined") && !errors.Is(err, ErrUnjoined) {
				t.Fatal(err)
			}
			for _, args := range calls {
				if args[0] != "ps" && args[0] != "virsh" {
					t.Fatalf("mutation with active producer: %v", args)
				}
			}
		})
	}
}

func TestNetworkPrivateFileNegatives(t *testing.T) {
	for _, name := range []string{"directory", "symlink", "hardlink", "shared", "foreign-path", "unknown-capability"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			path := filepath.Join(g.Work, "nico-sim")
			if err := os.WriteFile(path, []byte("private"), 0o700); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "directory":
				path = g.Work
			case "symlink":
				link := filepath.Join(g.Work, "linked")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "hardlink":
				if err := os.Link(path, filepath.Join(g.Work, "alias")); err != nil {
					t.Fatal(err)
				}
			case "shared":
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "foreign-path":
				path = filepath.Join(privateTestDir(t), "nico-sim")
				if err := os.WriteFile(path, []byte("foreign"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			g.capture = func(context.Context, []string, bool) ([]byte, error) { calls++; return nil, nil }
			caps := networkCapabilities["nico-sim"]
			if name == "unknown-capability" {
				caps = "cap_sys_admin=ep"
			}
			if err := g.Grant(t.Context(), path, caps); err == nil {
				t.Fatal("unsafe capability target accepted")
			}
			if calls != 0 {
				t.Fatal("invalid capability target performed work")
			}
		})
	}
}

func TestNetworkAddedAliasRefusesCapabilityRestoration(t *testing.T) {
	g := networkFixture(t)
	path := filepath.Join(g.Work, "nico-sim")
	if err := os.WriteFile(path, []byte("private"), 0o700); err != nil {
		t.Fatal(err)
	}
	g.capture = func(context.Context, []string, bool) ([]byte, error) { return nil, nil }
	if err := g.Grant(t.Context(), path, networkCapabilities["nico-sim"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(g.Work, "alias")); err != nil {
		t.Fatal(err)
	}
	mutation := false
	g.capture = func(_ context.Context, args []string, _ bool) ([]byte, error) {
		if args[0] == "setcap" {
			mutation = true
		}
		return nil, nil
	}
	if err := g.Close(t.Context()); err == nil {
		t.Fatal("hard-linked target restored")
	}
	if mutation {
		t.Fatal("foreign capability target mutated")
	}
}

func TestNetworkCleanupSurvivesJournalFailureButCannotReportSuccess(t *testing.T) {
	g := networkFixture(t)
	if err := g.Checkpoint(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(g.Work, "nico-sim")
	if err := os.WriteFile(path, []byte("private"), 0o700); err != nil {
		t.Fatal(err)
	}
	id, digest, err := networkFile(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	g.caps = []networkCapability{{path, id, digest, networkCapabilities["nico-sim"]}}
	g.rules = networkRules(g.Owner, g.KindCIDR)[:1]
	g.sysctlIntents = []string{networkSysctls[0]}
	g.checkpointHook = func() error { return unix.ENOSPC }
	capPresent, rulePresent, sysctl := true, true, "1"
	verified := false
	g.capture = func(_ context.Context, args []string, _ bool) ([]byte, error) {
		switch args[0] {
		case "getcap":
			if capPresent {
				return []byte("opaque-present"), nil
			}
		case "setcap":
			if args[1] == "-v" {
				verified = true
			}
			if args[1] == "-r" {
				if !verified {
					t.Fatal("capability revoked without libcap verification")
				}
				capPresent = false
			}
		case "iptables":
			switch args[5] {
			case "-C":
				if rulePresent {
					return nil, nil
				}
				return nil, &CommandError{ExitCode: 1, Cause: errors.New("absent")}
			case "-D":
				rulePresent = false
			}
		case "sysctl":
			if args[1] == "-n" {
				return []byte(sysctl), nil
			}
			sysctl = "0"
		}
		return nil, nil
	}
	if err = g.Close(t.Context()); err == nil || !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("cleanup result=%v", err)
	}
	if capPresent || rulePresent || sysctl != "0" {
		t.Fatal("live controls retained because journal full")
	}
	state, err := readNetworkState(g.Work)
	if err != nil {
		t.Fatal(err)
	}
	if state.Finished {
		t.Fatal("failed persistence reported success")
	}
	if err = g.Close(t.Context()); err == nil {
		t.Fatal("repeated cleanup hid persistence failure")
	}
}

func TestNetworkAmbiguousBridgeCreationCleanup(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			g := networkFixture(t)
			g.BridgeIntent = true
			present, deleted := true, false
			g.capture = func(_ context.Context, args []string, _ bool) ([]byte, error) {
				if args[0] != "ip" {
					return nil, nil
				}
				if slices.Contains(args, "delete") {
					deleted = true
					present = false
					return nil, nil
				}
				if !present {
					return []byte("[]"), nil
				}
				mac := g.BridgeMAC
				if foreign {
					mac = "02:00:00:00:00:00"
				}
				return []byte(fmt.Sprintf(`[{"ifname":"nvkevm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, mac)), nil
			}
			err := g.Close(t.Context())
			if foreign {
				if err == nil || deleted {
					t.Fatal("foreign ambiguous bridge deleted")
				}
			} else if err != nil || !deleted {
				t.Fatal("owned ambiguous bridge not restored", err)
			}
		})
	}
}

func TestNetworkCleanupDoesNotNeedDiagnosticFiles(t *testing.T) {
	g := networkFixture(t)
	g.cleaning = true
	g.runner.Work = filepath.Join(g.Work, "unavailable")
	raw, err := g.Command(t.Context(), []string{"/bin/sh", "-c", "printf exact-readback"}, false)
	if err != nil || string(raw) != "exact-readback" {
		t.Fatal(string(raw), err)
	}
	if _, err := os.Stat(g.runner.Work); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cleanup created diagnostic storage")
	}
}

func TestNetworkProbeProgressKeepsDiagnosticsWithoutFalseFailures(t *testing.T) {
	for _, exit := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(exit), func(t *testing.T) {
			g := networkFixture(t)
			writer := &recordedProgress{}
			tools := privateTestDir(t)
			script := fmt.Sprintf("#!/bin/sh\nprintf 'private network diagnostic\\n' >&2\nexit %d\n", exit)
			if err := os.WriteFile(filepath.Join(tools, "sudo"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			g.runner.Env = []string{"PATH=" + tools + ":/usr/bin:/bin"}
			g.runner.Progress = writer
			exists, err := g.ruleExists(t.Context(), networkRules(g.Owner, g.KindCIDR)[0])
			if exists != (exit == 0) || (err != nil) != (exit == 2) {
				t.Fatalf("exists=%t error=%v", exists, err)
			}
			if exit == 2 && CommandExitCode(err) != exit {
				t.Fatal("unexpected probe failure was hidden", err)
			}
			if len(writer.events) != 0 || writer.Len() != 0 {
				t.Fatalf("low-level/expected-absence probe emitted misleading progress: %+v %q", writer.events, writer.String())
			}
			raw, err := os.ReadFile(filepath.Join(g.Work, "network-001.log"))
			if err != nil || string(raw) != "private network diagnostic\n" {
				t.Fatalf("numbered diagnostic was lost: %q %v", raw, err)
			}
		})
	}
}

func TestNetworkProgressReportsOwningBoundary(t *testing.T) {
	for _, name := range []string{"setup-failed", "cleanup-failed", "cleanup-complete"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			writer := &recordedProgress{}
			g.runner.Progress = writer
			phase, last := "network-cleanup", PhaseFailed
			cause := errors.New("private cause")
			var err error
			switch name {
			case "setup-failed":
				phase = "network-setup"
				g.checkpointHook = func() error { return cause }
				err = g.Start(t.Context())
			case "cleanup-failed":
				g.stopDNSHook = func(context.Context) error { return cause }
				err = g.Close(t.Context())
			case "cleanup-complete":
				g.finished = true
				last = PhaseFinished
				err = g.Close(t.Context())
			}
			if name == "cleanup-complete" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, cause) || !strings.Contains(err.Error(), strings.ReplaceAll(phase, "-", " ")) {
				t.Fatalf("owning boundary/cause missing: %v", err)
			}
			if !slices.Equal(writer.events, phaseEvents(phase, last)) {
				t.Fatalf("events=%+v", writer.events)
			}
		})
	}
}

func TestNetworkProtectedHelperOwnsInnerDeadline(t *testing.T) {
	g := networkFixture(t)
	tools := privateTestDir(t)
	path := filepath.Join(tools, "sudo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	g.runner.Env = []string{"PATH=" + tools + ":/usr/bin:/bin"}
	raw, err := g.Command(t.Context(), []string{"ip", "link", "show"}, true)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"-n", "-k", "timeout", "--signal=TERM", "--kill-after=5", "25", "ip", "link", "show"}
	if !slices.Equal(strings.Fields(string(raw)), expected) {
		t.Fatalf("protected command=%s", raw)
	}
}

type sudoAuthenticationProgress struct {
	authenticate func(context.Context) error
}

func (p *sudoAuthenticationProgress) Write(raw []byte) (int, error) { return len(raw), nil }
func (p *sudoAuthenticationProgress) SudoAuthentication() func(context.Context) error {
	return p.authenticate
}

func TestNetworkInteractiveSudoBoundary(t *testing.T) {
	for _, scenario := range []string{"absent", "nil", "authenticated", "recheck-cleanup", "authentication-failed", "unjoined", "sudo-failed", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			g := networkFixture(t)
			tools := privateTestDir(t)
			calls, admitted := filepath.Join(tools, "calls"), filepath.Join(tools, "admitted")
			script := fmt.Sprintf("#!/bin/sh\nprintf 'command\\n' >> %q\nprintf '%%s\\n' \"$@\"\n", calls)
			if scenario == "authenticated" || scenario == "recheck-cleanup" {
				script += fmt.Sprintf("test -f %q || exit 99\n", admitted)
			}
			if scenario == "sudo-failed" {
				script += "exit 42\n"
			}
			if err := os.WriteFile(filepath.Join(tools, "sudo"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			g.runner.Env = []string{"PATH=" + tools + ":/usr/bin:/bin"}
			authCalls := 0
			failure := errors.New("authentication refused")
			if scenario != "absent" {
				presenter := &sudoAuthenticationProgress{}
				if scenario != "nil" {
					presenter.authenticate = func(ctx context.Context) error {
						authCalls++
						if err := ctx.Err(); err != nil {
							t.Fatal("canceled context reached authentication")
						}
						if scenario == "authentication-failed" {
							return failure
						}
						if scenario == "unjoined" {
							return ErrUnjoined
						}
						return os.WriteFile(admitted, []byte("authenticated"), 0600)
					}
				}
				g.runner.Progress = presenter
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			raw, err := g.Command(ctx, []string{"true"}, true)
			wantCalls := 1
			switch scenario {
			case "authentication-failed", "unjoined", "canceled":
				wantCalls = 0
				cause := map[string]error{"authentication-failed": failure, "unjoined": ErrUnjoined, "canceled": context.Canceled}[scenario]
				if !errors.Is(err, cause) || len(raw) != 0 || g.unjoined.Load() != (scenario == "unjoined") {
					t.Fatalf("failed admission: output=%q error=%v unjoined=%t", raw, err, g.unjoined.Load())
				}
			case "sudo-failed":
				if CommandExitCode(err) != 42 {
					t.Fatalf("actual sudo failure lost: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"-n", "timeout", "--signal=TERM", "--kill-after=5", "25", "true"}
				if scenario == "absent" || scenario == "nil" {
					want = append([]string{"-n", "-k"}, want[1:]...)
				}
				if !slices.Equal(strings.Fields(string(raw)), want) {
					t.Fatalf("sudo arguments=%q, want %v", raw, want)
				}
			}
			if scenario == "recheck-cleanup" {
				g.cleaning = true
				if _, err := g.Command(t.Context(), []string{"true"}, true); err != nil {
					t.Fatal(err)
				}
				wantCalls = 2
			}
			if err == nil {
				if _, err := g.Command(t.Context(), []string{"true"}, false); err != nil {
					t.Fatal(err)
				}
			}
			log, readErr := os.ReadFile(calls)
			if wantCalls == 0 && !errors.Is(readErr, os.ErrNotExist) || wantCalls != 0 && (readErr != nil || strings.Count(string(log), "command\n") != wantCalls) {
				t.Fatalf("command replay/admission: calls=%q error=%v, want %d", log, readErr, wantCalls)
			}
			wantAuth := wantCalls
			switch scenario {
			case "absent", "nil", "canceled":
				wantAuth = 0
			case "authentication-failed", "unjoined":
				wantAuth = 1
			}
			if authCalls != wantAuth {
				t.Fatalf("auth calls=%d, want %d", authCalls, wantAuth)
			}
		})
	}
}

func TestNetworkUnattendedSudoAdmissionDiagnostics(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			g := networkFixture(t)
			g.lockPath = filepath.Join(g.Work, "test.lock")
			tools := privateTestDir(t)
			script := "#!/bin/sh\nprintf '%s\\n' 'private sudo authentication diagnostic' >&2\nexit 42\n"
			if err := os.WriteFile(filepath.Join(tools, "sudo"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			g.runner.Env = []string{"PATH=" + tools + ":/usr/bin:/bin"}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}
			err := g.admit(ctx)
			if err == nil || !strings.Contains(err.Error(), "required unattended networking authorization") || !strings.Contains(err.Error(), networkSudoProbe) || !strings.Contains(err.Error(), "docs/local-nvke-demo.md#2-install-prerequisites-on-the-ubuntu-execution-host") || strings.Contains(err.Error(), "private sudo authentication diagnostic") {
				t.Fatalf("sudo prerequisite diagnostic=%v", err)
			}
			if canceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause=%v", err)
				}
			} else {
				var command *CommandError
				if !errors.As(err, &command) || command.Phase != "network-001" || command.ExitCode != 42 {
					t.Fatalf("sudo command cause=%v", err)
				}
			}
			if _, statErr := os.Lstat(filepath.Join(g.Work, networkJournal)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("network journal created after failed sudo prerequisite: %v", statErr)
			}
		})
	}
}

func TestNetworkProbeExecutableAdmission(t *testing.T) {
	for _, name := range []string{"valid", "empty", "relative", "missing", "directory", "no-exec", "group-write", "other-write", "setuid", "setgid", "foreign-owner", "parent-group-write", "parent-other-write", "symlink"} {
		t.Run(name, func(t *testing.T) {
			if name == "foreign-owner" && (runtime.GOOS != "linux" || os.Geteuid() != 0) {
				t.Skip("foreign executable owner requires an isolated Linux root fixture")
			}
			dir := privateTestDir(t)
			path := filepath.Join(dir, "nvke-demo")
			if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "empty":
				path = ""
			case "relative":
				path = "nvke-demo"
			case "missing":
				path += "-missing"
			case "directory":
				path = dir
			case "no-exec", "group-write", "other-write":
				mode := map[string]os.FileMode{"no-exec": 0o600, "group-write": 0o720, "other-write": 0o702}[name]
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			case "setuid", "setgid":
				mode := os.FileMode(0o700) | os.ModeSetuid
				if name == "setgid" {
					mode = 0o700 | os.ModeSetgid
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				if err := os.Chown(path, 1, -1); err != nil {
					t.Fatal(err)
				}
			case "parent-group-write", "parent-other-write":
				mode := os.FileMode(0o770)
				if name == "parent-other-write" {
					mode = 0o777
				}
				if err := os.Chmod(dir, mode); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := path + "-link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			resolved, err := admitNetworkProbeExecutable(t.Context(), path)
			if name == "valid" || name == "symlink" {
				if err != nil || resolved != filepath.Join(dir, "nvke-demo") {
					t.Fatalf("selected executable=%q, %v", resolved, err)
				}
			} else {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability || resolved != "" {
					t.Fatalf("unsafe executable admitted=%q, %v", resolved, err)
				}
			}
		})
	}
}

func TestNetworkProbeDirectoryProtection(t *testing.T) {
	for _, test := range []struct {
		name    string
		uid     uint32
		mode    uint32
		allowed bool
	}{
		{"root private", 0, unix.S_IFDIR | 0o755, true},
		{"current private", uint32(os.Getuid()), unix.S_IFDIR | 0o700, true},
		{"root sticky temporary", 0, unix.S_IFDIR | unix.S_ISVTX | 0o777, true},
		{"root non-sticky writable", 0, unix.S_IFDIR | 0o777, false},
		{"root group writable", 0, unix.S_IFDIR | 0o775, false},
		{"foreign owner", uint32(os.Getuid()) + 1, unix.S_IFDIR | 0o755, false},
		{"foreign sticky owner", uint32(os.Getuid()) + 1, unix.S_IFDIR | unix.S_ISVTX | 0o777, false},
		{"non-directory", 0, unix.S_IFREG | 0o755, false},
		{"unsearchable", 0, unix.S_IFDIR | 0o600, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if (networkProbeDirectoryProtection(test.uid, test.mode) == nil) != test.allowed {
				t.Fatal("unsafe directory protection decision")
			}
		})
	}
}

func TestNetworkProbeContextRefusalBeforePathWork(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code nvkrerr.Code
	}{{"nil context", nil, nvkrerr.CodeUsage}, {"canceled", canceled, nvkrerr.CodeTimeout}} {
		t.Run(test.name, func(t *testing.T) {
			path, err := admitNetworkProbeExecutable(test.ctx, "/must-not-inspect")
			var coded *nvkrerr.Error
			if path != "" || !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatal("context refusal reached file work or lost code", path, err)
			}
		})
	}
}

func TestNetworkPrivilegedProbeFixedTargetsAndFailure(t *testing.T) {
	for _, name := range []string{"success", "occupied-tcp", "occupied-udp", "occupied-dhcp", "canceled", "cancel-after-first"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			var calls []string
			err := probeNetworkPrivilegedPorts(ctx, func(udp bool, port int) error {
				calls = append(calls, fmt.Sprintf("%t:%d", udp, port))
				if name == "cancel-after-first" {
					cancel()
				}
				if name == "occupied-tcp" || name == "occupied-udp" && udp || name == "occupied-dhcp" && port == 67 {
					return unix.EADDRINUSE
				}
				return nil
			})
			want := []string{"false:53", "true:53", "true:67"}
			switch name {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "canceled":
				want = nil
			case "occupied-tcp", "cancel-after-first":
				want = want[:1]
			case "occupied-udp":
				want = want[:2]
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("fixed helper targets=%v, want %v", calls, want)
			}
			if name != "success" {
				var coded *nvkrerr.Error
				cause := error(unix.EADDRINUSE)
				if strings.HasPrefix(name, "cancel") {
					cause = context.Canceled
				}
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || !errors.Is(err, cause) {
					t.Fatalf("helper failure=%v", err)
				}
			}
		})
	}
	var nilContext context.Context
	if err := ProbeNetworkPrivilegedPorts(nilContext); err == nil {
		t.Fatal("nil helper context admitted")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		var coded *nvkrerr.Error
		if err := ProbeNetworkPrivilegedPorts(t.Context()); !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
			t.Fatalf("helper platform admission=%v", err)
		}
	}
}

func TestNetworkPrivilegedProbeFailureBeforeJournal(t *testing.T) {
	for _, cause := range []error{&CommandError{Phase: "network-007", ExitCode: 42, Cause: unix.EADDRINUSE}, context.Canceled, ErrUnjoined} {
		t.Run(cause.Error(), func(t *testing.T) {
			g := networkFixture(t)
			g.journal = false
			g.lockPath = filepath.Join(g.Work, "test.lock")
			g.policyPath = filepath.Join(g.Work, "bridge.conf")
			helper, err := resolveExisting("/usr/bin/true")
			if err != nil {
				t.Fatal(err)
			}
			g.helperPath = helper
			g.probeExecutable = "/selected/bootstrap/nvke-demo"
			called := false
			g.capture = func(_ context.Context, args []string, privileged bool) ([]byte, error) {
				if args[0] == g.probeExecutable {
					called = true
					if !privileged || !slices.Equal(args, []string{g.probeExecutable, "network-port-probe"}) {
						t.Fatalf("non-fixed privileged probe=%v privileged=%t", args, privileged)
					}
					return nil, cause
				}
				if privileged && !slices.Equal(args, []string{"true"}) {
					t.Fatalf("mutation before port admission=%v", args)
				}
				if args[0] == "ip" {
					return []byte("[]"), nil
				}
				return nil, nil
			}
			err = g.admit(t.Context())
			if !called || !errors.Is(err, cause) || !strings.Contains(err.Error(), "privileged DNS/DHCP port admission") {
				t.Fatalf("port child failure=%v called=%t", err, called)
			}
			if g.journal || g.unjoined.Load() != errors.Is(cause, ErrUnjoined) {
				t.Fatal("failed helper changed journal/join authority")
			}
			if _, err := os.Lstat(filepath.Join(g.Work, networkJournal)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("journal exists after failed child", err)
			}
		})
	}
}

func TestNetworkPrivilegedProbeChildError(t *testing.T) {
	g := networkFixture(t)
	g.journal = false
	g.probeExecutable = "/selected/bootstrap/nvke-demo"
	tools := privateTestDir(t)
	script := "#!/bin/sh\n[ \"$*\" = '-n -k timeout --signal=TERM --kill-after=5 25 /selected/bootstrap/nvke-demo network-port-probe' ] || exit 99\nprintf '%s\\n' 'private probe diagnostic' >&2\nexit 42\n"
	if err := os.WriteFile(filepath.Join(tools, "sudo"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g.runner.Env = []string{"PATH=" + tools + ":/usr/bin:/bin"}
	err := g.probePrivilegedPorts(t.Context())
	var command *CommandError
	if !errors.As(err, &command) || command.ExitCode != 42 || command.Phase != "network-001" || !strings.Contains(err.Error(), "privileged DNS/DHCP") || strings.Contains(err.Error(), "private probe diagnostic") {
		t.Fatalf("bounded privileged child invocation/error=%v", err)
	}
	if _, err = os.Lstat(filepath.Join(g.Work, networkJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed privileged child created journal", err)
	}
}

func TestNetworkPortProbeOccupiedSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("IP_FREEBIND probe is Linux-only")
	}
	for _, row := range []struct {
		udp  bool
		port int
	}{{false, 0}, {true, 0}, {false, 53}, {true, 53}, {true, 67}} {
		t.Run(fmt.Sprintf("UDP=%t/port=%d", row.udp, row.port), func(t *testing.T) {
			if row.port != 0 && (runtime.GOARCH != "arm64" || os.Geteuid() != 0) {
				t.Skip("fixed privileged sockets require isolated native ARM64 root fixture")
			}
			var address string
			var closeSocket func() error
			target := net.JoinHostPort("0.0.0.0", strconv.Itoa(row.port))
			if row.udp {
				held, err := net.ListenPacket("udp4", target)
				if err != nil {
					t.Fatal(err)
				}
				address, closeSocket = held.LocalAddr().String(), held.Close
			} else {
				held, err := net.Listen("tcp4", target)
				if err != nil {
					t.Fatal(err)
				}
				address, closeSocket = held.Addr().String(), held.Close
			}
			t.Cleanup(func() { _ = closeSocket() })
			_, raw, err := net.SplitHostPort(address)
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(raw)
			if err != nil {
				t.Fatal(err)
			}
			if row.port == 0 {
				err = networkProbePort(row.udp, port)
			} else {
				err = ProbeNetworkPrivilegedPorts(t.Context())
			}
			if !errors.Is(err, unix.EADDRINUSE) {
				t.Fatalf("occupied socket admission=%v", err)
			}
		})
	}
}

func TestNetworkKernelLockLifetimeAndSubstitution(t *testing.T) {
	for _, name := range []string{"lifetime", "symlink", "hardlink", "shared"} {
		t.Run(name, func(t *testing.T) {
			first, second := networkFixture(t), networkFixture(t)
			path := filepath.Join(first.Work, "test.lock")
			first.lockPath = path
			second.lockPath = path
			if name != "lifetime" {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				switch name {
				case "symlink":
					other := filepath.Join(first.Work, "other")
					if err := os.Rename(path, other); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path, filepath.Join(first.Work, "other")); err != nil {
						t.Fatal(err)
					}
				case "shared":
					if err := os.Chmod(path, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := first.acquireLock(); err == nil {
					t.Fatal("substituted lock adopted")
				}
				return
			}
			if err := first.acquireLock(); err != nil {
				t.Fatal(err)
			}
			if err := second.acquireLock(); err == nil {
				t.Fatal("concurrent guard admitted")
			}
			first.releaseHandles()
			if err := second.acquireLock(); err != nil {
				t.Fatal("kernel lock not released", err)
			}
		})
	}
}

func TestNetworkRootStagingSubstitutionBeforeUnlink(t *testing.T) {
	for _, name := range []string{"uid", "symlink", "hardlink", "mode", "foreign-path"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			path := filepath.Join(g.Work, "root-staging")
			if err := os.WriteFile(path, []byte("allow "+BridgeName+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "uid":
				if os.Getuid() == 0 {
					t.Skip("foreign owner fixture needs an unprivileged operator")
				}
			case "symlink":
				other := filepath.Join(g.Work, "other")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(g.Work, "other")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			case "foreign-path":
				path = filepath.Join(g.Work, "missing")
			}
			g.staging = &networkStaging{Path: path}
			calls := 0
			g.capture = func(context.Context, []string, bool) ([]byte, error) { calls++; return nil, nil }
			if _, err := g.stageBinding(t.Context()); err == nil {
				t.Fatal("substituted root staging accepted")
			}
			if calls != 0 {
				t.Fatal("unowned staging reached protected command")
			}
		})
	}
}
