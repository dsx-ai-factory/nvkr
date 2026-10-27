// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func guestBindingFixture(t *testing.T) []tenantMachineObservation {
	t.Helper()
	var list []tenantMachineObservation
	err := json.Unmarshal([]byte(`[
{"kind":"Machine","metadata":{"name":"private-cp","namespace":"nke-system","labels":{"cluster.x-k8s.io/cluster-name":"tenant","cluster.x-k8s.io/control-plane":""},"annotations":{"nke.nvidia.com/host-id":"cp-host"}},"spec":{"clusterName":"tenant","infrastructureRef":{"apiGroup":"infrastructure.cluster.x-k8s.io","kind":"NicoMachine","name":"private-cp"}}},
{"kind":"NicoMachine","metadata":{"name":"private-cp","namespace":"nke-system","labels":{"cluster.x-k8s.io/cluster-name":"tenant","cluster.x-k8s.io/control-plane":""}},"spec":{"labels":{"nke.nvidia.com/node-id":"cp-host","nke.nvidia.com/cluster-id":"cluster-id"}},"status":{"instanceID":"cp-instance","ready":false}},
{"kind":"Machine","metadata":{"name":"private-worker","namespace":"nke-system","labels":{"cluster.x-k8s.io/cluster-name":"tenant"},"annotations":{"nke.nvidia.com/host-id":"worker-host"}},"spec":{"clusterName":"tenant","infrastructureRef":{"apiVersion":"infrastructure.cluster.x-k8s.io/v1alpha1","kind":"NicoMachine","name":"private-worker"}}},
{"kind":"NicoMachine","metadata":{"name":"private-worker","namespace":"nke-system","labels":{"cluster.x-k8s.io/cluster-name":"tenant"}},"spec":{"labels":{"nke.nvidia.com/node-id":"worker-host","nke.nvidia.com/cluster-id":"cluster-id"}},"status":{"instanceID":"worker-instance","ready":true}}
]`), &list)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestTenantGuestBindingRequiresExactSourceOwnerJoin(t *testing.T) {
	for _, name := range []string{"valid", "wrong-cluster", "wrong-host", "wrong-role", "wrong-namespace", "wrong-reference", "duplicate-role", "duplicate-instance", "too-many", "pending"} {
		t.Run(name, func(t *testing.T) {
			rows := guestBindingFixture(t)
			switch name {
			case "wrong-cluster":
				rows[1].Spec.Labels["nke.nvidia.com/cluster-id"] = "foreign"
			case "wrong-host":
				rows[1].Spec.Labels["nke.nvidia.com/node-id"] = "foreign"
			case "wrong-role":
				delete(rows[1].Metadata.Labels, tenantControlLabel)
			case "wrong-namespace":
				rows[1].Metadata.Namespace = "foreign"
			case "wrong-reference":
				rows[0].Spec.InfrastructureRef.APIGroup = "foreign"
			case "duplicate-role":
				rows[2].Metadata.Labels[tenantControlLabel] = ""
			case "duplicate-instance":
				rows[3].Status.InstanceID = rows[1].Status.InstanceID
			case "too-many":
				rows = append(rows, rows[1])
			case "pending":
				rows[1].Status.InstanceID = ""
			}
			bound, valid := tenantGuestBindingsForCount(rows, "tenant", "cluster-id", 2, false)
			if valid != (name == "valid" || name == "pending") {
				t.Fatalf("binding validity %v for %s", valid, name)
			}
			if name == "valid" && (bound[0].instance != "cp-instance" || bound[1].instance != "worker-instance" || bound[0].ready != TenantConditionFalse || bound[1].ready != TenantConditionTrue) {
				t.Fatalf("incorrect source binding: %+v", bound)
			}
		})
	}
}

func TestTenantGuestFailureIsClosedAndRoleBound(t *testing.T) {
	for _, scenario := range []string{"known", "unknown-reason", "wrong-status", "wrong-condition", "duplicate-condition", "forged-host", "forged-cluster"} {
		t.Run(scenario, func(t *testing.T) {
			rows := guestBindingFixture(t)
			rows[3].Status.InstanceID = ""
			if err := json.Unmarshal([]byte(`{"conditions":[{"type":"MachineProvisioned","status":"False","reason":"InstanceCreateFailed","message":"private-console-password"}]}`), &rows[3].Status); err != nil {
				t.Fatal(err)
			}
			condition := &rows[3].Status.Conditions[0]
			switch scenario {
			case "unknown-reason":
				condition.Reason = "private\x1b[31mreason"
			case "wrong-status":
				condition.Status = "True"
			case "wrong-condition":
				condition.Type = "UnknownPrivateCondition"
			case "duplicate-condition":
				rows[3].Status.Conditions = append(rows[3].Status.Conditions, *condition)
			case "forged-host":
				rows[3].Spec.Labels["nke.nvidia.com/node-id"] = "foreign"
			case "forged-cluster":
				rows[3].Spec.Labels["nke.nvidia.com/cluster-id"] = "foreign"
			}
			bindings, valid := tenantGuestBindingsForCount(rows, "tenant", "cluster-id", 2, false)
			if valid != (scenario != "duplicate-condition" && scenario != "forged-host" && scenario != "forged-cluster") {
				t.Fatalf("unexpected source admission: %v", valid)
			}
			if (bindings[1].failure == TenantInstanceCreateFailed) != (scenario == "known") {
				t.Fatalf("unmapped or forged failure exposed: %+v", bindings[1])
			}
			public := emptyTenantGuests()
			if valid {
				public[1].ProvisioningFailure = bindings[1].failure
			}
			raw, err := json.Marshal(public)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "worker-host") || public[1].Available || public[1].ProviderReady != TenantConditionUnknown {
				t.Fatalf("failure changed privacy or readiness: %s", raw)
			}
		})
	}
}

func TestTenantConsoleMetadataRejectsUnsafeEntries(t *testing.T) {
	for _, name := range []string{"regular", "missing", "symlink", "directory-symlink", "fifo", "unowned-root", "canceled"} {
		t.Run(name, func(t *testing.T) {
			work := privateTestDir(t)
			dir := filepath.Join(work, "var", "instances", "instance")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "disk.qcow2.ttyS0.log")
			switch name {
			case "symlink":
				if err := os.Symlink("/etc/passwd", file); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				if err := os.Rename(dir, dir+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-old", dir); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(file, 0o600); err != nil {
					t.Fatal(err)
				}
			case "unowned-root":
				if err := os.Chmod(work, 0o755); err != nil {
					t.Fatal(err)
				}
			case "regular":
				if err := os.WriteFile(file, []byte("private console secret"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			files, err := tenantConsoleMetadata(ctx, work, "instance")
			if (err == nil) != (name == "regular" || name == "missing") {
				t.Fatalf("metadata admission %s: %v", name, err)
			}
			if name == "regular" && (files[0] == nil || files[0].Size() != 22) {
				t.Fatalf("metadata missing: %v", files)
			}
			if name == "missing" && files != [2]os.FileInfo{} {
				t.Fatal("missing file fabricated metadata")
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestTenantConsoleChangeIsObservedNotInferredFromBaseline(t *testing.T) {
	work := privateTestDir(t)
	dir := filepath.Join(work, "var", "instances", "instance")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "disk.qcow2.ttyS0.log")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	sample := tenantConsoleSample{}
	for i, name := range []string{"baseline", "unchanged", "grown", "rotated", "truncated"} {
		if name == "grown" {
			if err := os.WriteFile(path, []byte("secret grows"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if name == "rotated" {
			if err := os.Rename(path, path+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("secret grows"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if name == "truncated" {
			if err := os.Truncate(path, 0); err != nil {
				t.Fatal(err)
			}
		}
		files, err := tenantConsoleMetadata(t.Context(), work, "instance")
		if err != nil {
			t.Fatal(err)
		}
		event := TenantGuestObservation{}
		sample.observe(start.Add(time.Duration(i)*time.Second), files, &event)
		if !event.ConsoleKnown || event.ConsoleChanged != (name == "grown") || event.ConsoleLastChange != 0 {
			t.Fatalf("%s: %+v", name, event)
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), work) {
			t.Fatal("private console leaked")
		}
	}
}

func TestNativeTenantObserverUnavailableAndCancellationAreBounded(t *testing.T) {
	for _, scenario := range []string{"unavailable", "canceled", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			calls := filepath.Join(work, "calls")
			writeTenantFixture(t, filepath.Join(work, "kubectl"), []byte("#!/bin/sh\nprintf call >> \"$CALLS\"\nif test \"$SLOW\" = yes; then exec sleep 60; fi\nexit 7\n"), 0o700)
			writer := &recordedProgress{}
			selected := tenant{name: "tenant", id: "id", session: ManagementSession{Work: work, Tools: work, Runner: Runner{Work: work, Progress: writer, Env: []string{"PATH=/usr/bin:/bin", "SITE_KUBECONFIG=owned", "CALLS=" + calls, "SLOW=yes"}}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "unavailable" {
				selected.session.Runner.Env[len(selected.session.Runner.Env)-1] = "SLOW=no"
			}
			if scenario == "canceled" {
				cancel()
			}
			if scenario == "deadline" {
				var cancelDeadline context.CancelFunc
				ctx, cancelDeadline = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancelDeadline()
			}
			observer := nativeTenantObserver{}
			site := &nativeSiteAttempt{tenantOwner: &selected}
			runner := selected.session.Runner
			err := observer.update(ctx, site, runner, work, "owned", time.Now())
			if scenario == "unavailable" {
				if err != nil || len(writer.events) != 1 || writer.events[0].Kind != TenantGuests || writer.events[0].Guests != emptyTenantGuests() {
					t.Fatalf("unavailable observation: %+v %v", writer.events, err)
				}
				if err = observer.update(ctx, site, runner, work, "owned", observer.last.Add(time.Second)); err != nil || len(writer.events) != 1 {
					t.Fatal("observer ignored 30-second cadence")
				}
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnjoined) || len(writer.events) != map[string]int{"canceled": 0, "deadline": 1}[scenario] {
				t.Fatalf("canceled observer did not join: %v %+v", err, writer.events)
			}
			if scenario == "canceled" {
				if _, readErr := os.Stat(calls); !os.IsNotExist(readErr) {
					t.Fatal("already canceled observer started work")
				}
			}
		})
	}
}

func TestTenantDomainPresentationIsClosed(t *testing.T) {
	for _, raw := range []string{"private\nsecret", "running\x1b[31m", "RUNNING", "foreign"} {
		if presentedDomainState([]byte(raw)) != TenantDomainUnknown {
			t.Fatal("untrusted domain state exposed")
		}
	}
}

func TestTenantDomainObservationUsesOnlyExactOwnedDomains(t *testing.T) {
	for _, scenario := range []string{"owned", "foreign-disk", "unsafe-console"} {
		t.Run(scenario, func(t *testing.T) {
			base := privateTestDir(t)
			simulator, tools := filepath.Join(base, "simulator"), filepath.Join(base, "tools")
			dir := filepath.Join(simulator, "var", "instances", "cp-instance")
			for _, path := range []string{dir, tools} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeTenantFixture(t, filepath.Join(dir, "disk.qcow2"), []byte("disk"), 0o600)
			console := filepath.Join(dir, "disk.qcow2.ttyS0.log")
			if scenario == "unsafe-console" {
				if err := os.Symlink("/etc/passwd", console); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTenantFixture(t, console, []byte("private console secret"), 0o600)
			}
			writeTenantFixture(t, filepath.Join(tools, "virsh"), []byte(`#!/bin/sh
case "$3" in
 dumpxml)
   disk="$SIM/var/instances/cp-instance/disk.qcow2"
   if test "$SCENARIO" = foreign-disk; then disk=/foreign/disk.qcow2; fi
   printf '<domain><name>%s</name><uuid>cp-instance-uuid</uuid><devices><disk><source file="%s"/></disk></devices></domain>' "$4" "$disk"
   ;;
 domuuid) printf cp-instance-uuid ;;
 domstate) printf running ;;
 *) exit 9 ;;
esac
`), 0o700)
			runner := Runner{Env: []string{"PATH=" + tools + ":/usr/bin:/bin", "SIM=" + simulator, "SCENARIO=" + scenario}}
			sample := tenantConsoleSample{}
			guest := TenantGuestObservation{Domain: TenantDomainUnknown, ProviderReady: TenantConditionUnknown}
			observedAt := time.Unix(1000, 0)
			observed, err := observeTenantDomain(t.Context(), runner, simulator, "cp-instance", &sample, &guest, observedAt)
			if err != nil || observed != (scenario != "foreign-disk") || guest.ConsoleKnown != (scenario == "owned") || guest.ConsoleChanged || guest.Available || guest.ProviderReady != TenantConditionUnknown {
				t.Fatalf("domain observation invented readiness or unsafe console metadata: %+v %v", guest, err)
			}
			wantDomain := TenantDomainUnknown
			if observed {
				wantDomain = TenantDomainRunning
			}
			if guest.Domain != wantDomain {
				t.Fatal("foreign or owned domain state lost", guest)
			}
			if scenario == "owned" {
				writeTenantFixture(t, console, []byte("private console secret grows"), 0o600)
				for _, elapsed := range []time.Duration{30 * time.Second, 75 * time.Second} {
					guest = TenantGuestObservation{}
					if _, err := observeTenantDomain(t.Context(), runner, simulator, "cp-instance", &sample, &guest, observedAt.Add(elapsed)); err != nil || !guest.ConsoleChanged || guest.ConsoleLastChange != elapsed-30*time.Second {
						t.Fatal("domain observer discarded its injected observation time", guest, err)
					}
				}
			}
			public, err := json.Marshal(guest)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"secret", "cp-instance", base} {
				if strings.Contains(string(public), private) {
					t.Fatalf("domain progress leaked %s", private)
				}
			}
		})
	}
}
