// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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

func TestNetworkPlatformBeforeFilesystemOrCommands(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Skip("native admitted platform exercised by opt-in real E2E")
	}
	for _, recovery := range []bool{false, true} {
		var err error
		if recovery {
			_, err = RecoverNetworkGuard(t.Context(), Runner{Work: "/does-not-exist"})
		} else {
			_, err = NewNetworkGuard(t.Context(), Runner{Work: "/does-not-exist"}, "172.18.0.0/16")
		}
		var coded *nkderr.Error
		if !errors.As(err, &coded) || coded.Code != nkderr.CodeEngineCapability {
			t.Fatalf("platform admission=%v", err)
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
				state.Owner = "nkd-demo-" + strings.Repeat("b", 32)
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

func TestNetworkJournalContracts(t *testing.T) {
	for _, row := range []struct {
		name   string
		mutate func(networkState) networkState
	}{{"valid", func(s networkState) networkState { return s }}, {"foreign-work", func(s networkState) networkState { s.Work += "x"; return s }}, {"foreign-owner", func(s networkState) networkState { s.UID++; return s }}, {"old-version", func(s networkState) networkState { s.Version = 1; return s }}, {"bad-digest", func(s networkState) networkState { s.HelperHash = s.HelperHash[7:]; return s }}, {"upper-digest", func(s networkState) networkState { s.HelperHash = strings.ToUpper(s.HelperHash); return s }}, {"bad-mac", func(s networkState) networkState { s.BridgeMAC = "00:00:00:00:00:00"; return s }}, {"bad-index", func(s networkState) networkState { s.BridgeID = "042"; return s }}, {"arbitrary-command", func(s networkState) networkState {
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

func TestNetworkLegacyJournalRefusesBeforeActions(t *testing.T) {
	g := networkFixture(t)
	// Legacy array-shaped identities must be diagnosed before Go v2 decoding.
	raw := []byte(`{"version":1,"helperID":[1,2,493,0,0],"rules":[],"caps":[]}`)
	path := filepath.Join(g.Work, networkJournal)
	if err := networkWriteExclusive(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	g.capture = func(context.Context, []string, bool) ([]byte, error) { calls++; return nil, nil }
	err := g.Start(t.Context())
	var coded *nkderr.Error
	if !errors.As(err, &coded) || coded.Code != nkderr.CodePreflightFailed ||
		!strings.Contains(err.Error(), legacyNetworkRecoveryRevision) ||
		!strings.Contains(err.Error(), legacyNetworkRecoveryHelper) ||
		!strings.Contains(err.Error(), "version 1") {
		t.Fatalf("legacy refusal=%v", err)
	}
	if calls != 0 {
		t.Fatal("legacy refusal executed host commands")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("legacy refusal changed the journal", err)
	}
	entries, err := os.ReadDir(g.Work)
	if err != nil || len(entries) != 1 {
		t.Fatal("legacy refusal created recovery files", entries, err)
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
				Path:    filepath.Join(filepath.Dir(qemuPolicy), ".nkd-policy-"+strings.TrimPrefix(g.Owner, "nkd-demo-")),
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
			stage := filepath.Join(filepath.Dir(qemuPolicy), ".nkd-policy-"+strings.TrimPrefix(g.Owner, "nkd-demo-"))
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
			raw := fmt.Sprintf(`[{"ifname":"nkdvm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, g.BridgeMAC)
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

func TestNetworkGuestTCPInvalidBeforeWork(t *testing.T) {
	for _, row := range []struct {
		address string
		ports   []int
	}{{"8.8.8.8", []int{30440}}, {"172.18.0.0", []int{30440}}, {"172.18.255.255", []int{30440}}, {"172.18.0.2", nil}, {"172.18.0.2", []int{0}}, {"172.18.0.2", []int{80}}, {"172.18.0.2", []int{30440, 30440}}} {
		t.Run(fmt.Sprint(row), func(t *testing.T) {
			g := networkFixture(t)
			calls := 0
			g.capture = func(context.Context, []string, bool) ([]byte, error) { calls++; return nil, nil }
			if err := g.AllowGuestTCP(t.Context(), row.address, row.ports); err == nil {
				t.Fatal("invalid exception accepted")
			}
			if calls != 0 || len(g.rules) != 0 {
				t.Fatal("invalid grant performed work")
			}
		})
	}
}

func TestNetworkGuestTCPAmbiguousInsertRetainsIntent(t *testing.T) {
	g := networkFixture(t)
	g.BridgeIntent = true
	inserted := false
	fail := true
	g.capture = func(_ context.Context, args []string, _ bool) ([]byte, error) {
		if args[0] == "ip" {
			return []byte(fmt.Sprintf(`[{"ifname":"nkdvm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, g.BridgeMAC)), nil
		}
		if args[0] == "iptables" {
			switch args[5] {
			case "-C":
				if inserted {
					return nil, nil
				}
				return nil, &CommandError{ExitCode: 1, Cause: errors.New("absent")}
			case "-I":
				inserted = true
				if fail {
					return nil, errors.New("ambiguous joined insert")
				}
			case "-D":
				inserted = false
			}
		}
		return nil, nil
	}
	if err := g.AllowGuestTCP(t.Context(), "172.18.0.2", []int{30440}); err == nil {
		t.Fatal("ambiguous insert succeeded")
	}
	if len(g.rules) != 1 || !inserted {
		t.Fatal("grant intent lost")
	}
	state, err := readNetworkState(g.Work)
	if err != nil || len(state.Rules) != 1 {
		t.Fatal(state, err)
	}
	fail = false
	if err = g.RevokeGuestTCP(t.Context()); err != nil {
		t.Fatal(err)
	}
	if inserted || len(g.rules) != 1 || g.cleaning {
		t.Fatal("revocation lost ledger or failed cleanup")
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
				return []byte(fmt.Sprintf(`[{"ifname":"nkdvm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, mac)), nil
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
	expected := []string{"-n", "timeout", "--signal=TERM", "--kill-after=5", "25", "ip", "link", "show"}
	if !slices.Equal(strings.Fields(string(raw)), expected) {
		t.Fatalf("protected command=%s", raw)
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
			if err == nil || !strings.Contains(err.Error(), "required unattended sudo") || !strings.Contains(err.Error(), "sudo -n true") || !strings.Contains(err.Error(), "docs/local-nke-demo.md#2-install-prerequisites-on-the-ubuntu-execution-host") || strings.Contains(err.Error(), "private sudo authentication diagnostic") {
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
