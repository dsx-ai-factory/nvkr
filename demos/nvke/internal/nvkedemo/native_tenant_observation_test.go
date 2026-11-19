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
)

func nativeThreeGuestBindingsFixture(t *testing.T) []tenantMachineObservation {
	t.Helper()
	rows := guestBindingFixture(t)
	// Preserve the actual CAPI projection while adding a distinct second worker.
	raw, err := json.Marshal(rows[2:])
	if err != nil {
		t.Fatal(err)
	}
	var second []tenantMachineObservation
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatal(err)
	}
	second[0].Metadata.Name = "private-worker-2"
	second[0].Metadata.Annotations["nke.nvidia.com/host-id"] = "worker-host-2"
	second[0].Spec.InfrastructureRef.Name = "private-worker-2"
	second[1].Metadata.Name = "private-worker-2"
	second[1].Spec.Labels["nke.nvidia.com/node-id"] = "worker-host-2"
	second[1].Status.InstanceID = "worker-instance-2"
	rows = append(rows, second...)
	return rows
}

func genericNativeGuestBindingsFixture(t *testing.T) []tenantMachineObservation {
	rows := nativeThreeGuestBindingsFixture(t)
	for i := 0; i < len(rows); i += 2 {
		machine, infra := &rows[i], &rows[i+1]
		machine.Metadata.UID = machine.Metadata.Name + "-uid"
		machine.Metadata.OwnerReferences = []tenantObjectOwner{{APIVersion: "cluster.x-k8s.io/v1beta2", Kind: "Cluster", Name: "tenant", UID: "cluster-uid"}}
		machine.Metadata.Annotations = nil
		infra.Metadata.UID = infra.Metadata.Name + "-infra-uid"
		controller := true
		infra.Metadata.OwnerReferences = []tenantObjectOwner{{APIVersion: "cluster.x-k8s.io/v1beta2", Kind: "Machine", Name: machine.Metadata.Name, UID: machine.Metadata.UID, Controller: &controller}}
		delete(infra.Metadata.Labels, tenantControlLabel)
		infra.Spec.Labels = nil
	}
	return rows
}

func TestTenantGuestBindingsSupportBothNativeTopologies(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		expected int
		valid    bool
	}{
		{"two", 2, true}, {"three", 3, true}, {"extra worker", 2, false},
		{"duplicate instance", 3, false}, {"foreign host", 3, false},
		{"unknown topology", 4, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			rows := nativeThreeGuestBindingsFixture(t)
			if scenario.name == "two" {
				rows = rows[:4]
			}
			if scenario.name == "duplicate instance" {
				rows[5].Status.InstanceID = rows[3].Status.InstanceID
			}
			if scenario.name == "foreign host" {
				rows[5].Spec.Labels["nke.nvidia.com/node-id"] = "foreign"
			}
			bindings, valid := tenantGuestBindingsForCount(rows, "tenant", "cluster-id", scenario.expected, false)
			if valid != scenario.valid {
				t.Fatalf("role/instance binding valid=%t; wanted %t", valid, scenario.valid)
			}
			if valid && (bindings[0].instance != "cp-instance" || bindings[1].instance != "worker-instance" || scenario.expected == 3 && bindings[2].instance != "worker-instance-2") {
				t.Fatal("native worker identities were collapsed", bindings)
			}
		})
	}
}

func TestNativeGuestBindingsRequireExactCAPIUIDOwnerChain(t *testing.T) {
	for _, scenario := range []string{"valid", "foreign cluster UID", "foreign machine UID", "not controller", "missing owner", "wrong namespace", "wrong group"} {
		t.Run(scenario, func(t *testing.T) {
			rows := genericNativeGuestBindingsFixture(t)
			switch scenario {
			case "foreign cluster UID":
				rows[2].Metadata.OwnerReferences[0].UID = "foreign"
			case "foreign machine UID":
				rows[3].Metadata.OwnerReferences[0].UID = "foreign"
			case "not controller":
				controller := false
				rows[3].Metadata.OwnerReferences[0].Controller = &controller
			case "missing owner":
				rows[3].Metadata.OwnerReferences = nil
			case "wrong namespace":
				rows[3].Metadata.Namespace = "foreign"
			case "wrong group":
				rows[3].Metadata.OwnerReferences[0].APIVersion = "foreign/v1"
			}
			bound, valid := tenantGuestBindingsForCount(rows, "tenant", "", 3, true)
			if valid != (scenario == "valid") {
				t.Fatalf("generic CAPI owner join valid=%t; expected %t", valid, scenario == "valid")
			}
			if valid && (bound[0].instance != "cp-instance" || bound[1].instance != "worker-instance" || bound[2].instance != "worker-instance-2") {
				t.Fatal("three actual roles not kept distinct", bound)
			}
		})
	}
}

func TestNativeTenantObserverBindsProviderStatusWithoutInventingQEMULiveness(t *testing.T) {
	for _, scenario := range []string{"ready", "error", "unknown status", "duplicate instance", "missing inventory", "failed observation", "console", "unsafe console", "libvirt", "foreign-domain", "creation-failed-no-inventory"} {
		t.Run(scenario, func(t *testing.T) {
			root := privateTestDir(t)
			tools := filepath.Join(root, "tools")
			if err := os.Mkdir(tools, 0700); err != nil {
				t.Fatal(err)
			}
			writer := &recordedProgress{}
			runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "NICO_VMS_VM_BACKEND=darwin-qemu"}, Progress: writer}
			writeTenantFixture(t, filepath.Join(tools, "kubectl"), []byte("#!/bin/sh\ncat \"$FIXTURE/machines.json\"\n"), 0700)
			writeTenantFixture(t, filepath.Join(tools, "nico-simctl"), []byte("#!/bin/sh\ncat \"$FIXTURE/instances.json\"\n"), 0700)
			machines := genericNativeGuestBindingsFixture(t)
			if scenario == "creation-failed-no-inventory" {
				machines[3].Status.InstanceID = ""
				machines[3].Status.Conditions = []struct{ Type, Status, Reason string }{{"MachineProvisioned", "False", "InstanceCreateFailed"}}
			}
			if err := privateJSON(filepath.Join(root, "machines.json"), map[string]any{"items": machines}); err != nil {
				t.Fatal(err)
			}
			state := "Ready"
			if scenario == "error" {
				state = "Error"
			}
			if scenario == "unknown status" {
				state = "private-unmapped-status"
			}
			instances := []nativeInstance{{ID: "cp-instance", Status: state}, {ID: "worker-instance", Status: state}, {ID: "worker-instance-2", Status: state}}
			if scenario == "duplicate instance" {
				instances[1].ID = "cp-instance"
			}
			if scenario == "missing inventory" {
				instances = nil
			}
			if scenario != "failed observation" && scenario != "creation-failed-no-inventory" {
				if err := privateJSON(filepath.Join(root, "instances.json"), instances); err != nil {
					t.Fatal(err)
				}
			}
			owner := &tenant{name: "tenant", id: "cluster-id", session: ManagementSession{Work: root, Tools: tools, Runner: runner}}
			site := &nativeSiteAttempt{tenantOwner: owner, conformance: &nativeConformanceConfig{}, api: "http://127.0.0.1:18080"}
			if scenario == "console" || scenario == "unsafe console" || scenario == "libvirt" || scenario == "foreign-domain" {
				site.simulatorWork = filepath.Join(root, "simulator")
				for _, instance := range []string{"cp-instance", "worker-instance", "worker-instance-2"} {
					dir := filepath.Join(site.simulatorWork, "instances", instance)
					if scenario == "libvirt" || scenario == "foreign-domain" {
						dir = filepath.Join(site.simulatorWork, "var", "instances", instance)
					}
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					member := "console.log"
					if scenario == "libvirt" || scenario == "foreign-domain" {
						member = "disk.qcow2.ttyS0.log"
					}
					console := filepath.Join(dir, member)
					if scenario == "unsafe console" {
						if err := os.Symlink("/etc/passwd", console); err != nil {
							t.Fatal(err)
						}
					} else {
						writeTenantFixture(t, console, []byte("private console bytes"), 0600)
					}
				}
			}
			if scenario == "libvirt" || scenario == "foreign-domain" {
				writeTenantFixture(t, filepath.Join(tools, "virsh"), []byte(`#!/bin/sh
case "$3" in
 dumpxml)
  instance=${4#nico-vm-}
  disk="$SIM/var/instances/$instance/disk.qcow2"
  if test "$SCENARIO" = foreign-domain; then disk=/foreign/disk.qcow2; fi
  printf '<domain><name>%s</name><uuid>%s-uuid</uuid><devices><disk><source file="%s"/></disk></devices></domain>' "$4" "$instance" "$disk" ;;
 domuuid) printf '%s-uuid' "${4#nico-vm-}" ;;
 domstate) printf running ;;
 *) exit 9 ;;
esac
`), 0700)
				for _, instance := range []string{"cp-instance", "worker-instance", "worker-instance-2"} {
					writeTenantFixture(t, filepath.Join(site.simulatorWork, "var", "instances", instance, "disk.qcow2"), []byte("fixture disk"), 0600)
				}
				runner.Env = []string{"PATH=" + tools + ":/usr/bin:/bin", "FIXTURE=" + root, "SIM=" + site.simulatorWork, "SCENARIO=" + scenario, "NICO_VMS_VM_BACKEND=libvirt", "NICO_VMS_LIBVIRT_URI=qemu:///session"}
				owner.session.Runner = runner
				site.guard = &NetworkGuard{}
			}
			observer := nativeTenantObserver{}
			now := time.Now()
			if err := observer.update(t.Context(), site, runner, tools, "private-config", now); err != nil {
				t.Fatal(err)
			}
			if len(writer.events) != 1 || writer.events[0].Kind != TenantGuests || writer.events[0].ExpectedNodes != 3 {
				t.Fatal("native snapshot was not emitted", writer.events)
			}
			event := writer.events[0]
			for index, guest := range event.Guests {
				available := scenario == "ready" || scenario == "error" || scenario == "unknown status" || scenario == "duplicate instance" && index == 2 || scenario == "console" || scenario == "unsafe console" || scenario == "libvirt" || scenario == "foreign-domain"
				domain := TenantDomainUnknown
				if scenario == "libvirt" {
					domain = TenantDomainRunning
				}
				if (guest.ProvisioningFailure == TenantInstanceCreateFailed) != (scenario == "creation-failed-no-inventory" && index == 1) {
					t.Fatal("known provider failure hidden by unavailable inventory", guest)
				}
				if guest.Available != available || guest.Domain != domain || guest.ConsoleKnown != (scenario == "console" || scenario == "libvirt") {
					t.Fatal("provider query invented domain/console liveness", event)
				}
				if available && scenario == "ready" && guest.InstanceState != TenantInstanceReady || available && scenario == "error" && guest.InstanceState != TenantInstanceError || available && scenario == "unknown status" && guest.InstanceState != TenantInstanceUnknown {
					t.Fatal("source instance status mapped incorrectly", guest)
				}
			}
			raw, err := json.Marshal(event)
			if err != nil || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "cp-instance") || strings.Contains(string(raw), "worker-instance") {
				t.Fatal("private inventory data leaked to presenter", string(raw), err)
			}
			// A failed later observation clears the previous snapshot instead of
			// making old Ready status look current. Numbered logs remain unique.
			if err := os.WriteFile(filepath.Join(root, "instances.json"), []byte("invalid json"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := observer.update(t.Context(), site, runner, tools, "private-config", now.Add(31*time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(writer.events) != 2 || writer.events[1].Guests[0].Available {
				t.Fatal("stale Ready snapshot survived failed refresh", writer.events)
			}
			if scenario == "console" {
				raw, err := json.Marshal(instances)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "instances.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				console := filepath.Join(site.simulatorWork, "instances", "cp-instance", "console.log")
				if err := os.WriteFile(console, []byte("private console bytes have actually grown"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := observer.update(t.Context(), site, runner, tools, "private-config", now.Add(62*time.Second)); err != nil {
					t.Fatal(err)
				}
				if len(writer.events) != 3 || !writer.events[2].Guests[0].ConsoleChanged || writer.events[2].Guests[0].ConsoleBytes <= event.Guests[0].ConsoleBytes {
					t.Fatal("actual console growth was not observed", writer.events)
				}
			}
		})
	}
}

func TestNativeGuestObservationPreservesCancellationAndLostJoin(t *testing.T) {
	for _, unjoined := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		cause := errors.New("private-observation-failure")
		if unjoined {
			cause = errors.Join(cause, ErrUnjoined)
		} else {
			cancel()
		}
		err := nativeGuestObservationError(ctx, cause)
		cancel()
		if unjoined && !errors.Is(err, ErrUnjoined) || !unjoined && !errors.Is(err, context.Canceled) {
			t.Fatal("observer masked lifecycle failure", err)
		}
	}
}
