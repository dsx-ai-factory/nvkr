// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

// The native observer shares the existing CAPI role/instance binding and the
// private simulator inventory. NICo Ready describes its instance, not a live
// QEMU process, Kubernetes readiness, console activity or successful workload.
type nativeTenantObserver struct {
	last      time.Time
	console   [3]tenantConsoleSample
	instances [3]string
}

func (observer *nativeTenantObserver) update(ctx context.Context, site *nativeSiteAttempt, runner Runner, tools, siteConfig string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if _, enabled := runner.Progress.(ProgressReporter); !enabled {
		return nil
	}
	if !observer.last.IsZero() && now.Sub(observer.last) < 30*time.Second {
		return nil
	}
	observer.last = now
	guests := emptyTenantGuests()
	expected := site.tenantNodeCount()
	if expected == 3 {
		guests[2] = TenantGuestObservation{Role: TenantGuestWorker, Domain: TenantDomainUnknown, ProviderReady: TenantConditionUnknown}
	}
	defer func() {
		reportProgress(runner.Progress, ProgressEvent{Kind: TenantGuests, Name: "native", ExpectedNodes: expected, Guests: guests})
	}()
	budget, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	owner := site.tenantOwner
	if owner == nil {
		return nil
	}
	raw, err := owner.capture(budget, owner.kubectl(siteConfig, "--request-timeout=10s", "get", "machines.cluster.x-k8s.io,nicomachines.infrastructure.cluster.x-k8s.io", "--all-namespaces", "--selector", tenantClusterLabel+"="+owner.name, "-o", "json"), "native-guest-bindings", 15*time.Second)
	if err != nil {
		return nativeGuestObservationError(ctx, err)
	}
	var machines struct{ Items []tenantMachineObservation }
	if json.Unmarshal(raw, &machines) != nil {
		return nil
	}
	bindings, valid := tenantGuestBindingsForCount(machines.Items, owner.name, owner.id, expected, true)
	if !valid {
		return nil
	}
	for index, binding := range bindings {
		guests[index].ProvisioningFailure = binding.failure
	}
	// Silence mechanical progress. Observation failures stay in private logs;
	// an unjoined child still propagates to the same lifecycle owner.
	raw, err = owner.capture(budget, []string{filepath.Join(tools, "nico-simctl"), "instance", "list", "--base-url", site.api}, "native-guest-inventory", 15*time.Second)
	if err != nil {
		return nativeGuestObservationError(ctx, err)
	}
	var instances []nativeInstance
	if json.Unmarshal(raw, &instances) != nil || len(instances) > expected+1 {
		return nil
	}
	for index, binding := range bindings {
		if binding.instance == "" {
			continue
		}
		matches := 0
		var instance nativeInstance
		for _, row := range instances {
			if row.ID == binding.instance {
				instance, matches = row, matches+1
			}
		}
		if matches != 1 {
			continue
		}
		guests[index].Available, guests[index].ProviderReady = true, binding.ready
		guests[index].InstanceState = TenantInstanceUnknown
		if observer.instances[index] != binding.instance {
			observer.console[index] = tenantConsoleSample{}
			observer.instances[index] = binding.instance
		}
		if site.guard != nil && environmentValue(runner.Env, "NICO_VMS_VM_BACKEND") == "libvirt" && environmentValue(runner.Env, "NICO_VMS_LIBVIRT_URI") == "qemu:///session" {
			domainRunner := runner
			domainRunner.Work, domainRunner.Progress = "", nil
			_, readErr := observeTenantDomain(budget, domainRunner, site.simulatorWork, binding.instance, &observer.console[index], &guests[index], now)
			if err := nativeGuestObservationError(ctx, readErr); err != nil {
				return err
			}
		} else if files, readErr := tenantConsoleMetadataForBackend(budget, site.simulatorWork, binding.instance, environmentValue(runner.Env, "NICO_VMS_VM_BACKEND")); readErr == nil {
			observer.console[index].observe(now, files, &guests[index])
		}
		switch instance.Status {
		case "Ready":
			guests[index].InstanceState = TenantInstanceReady
		case "Error":
			guests[index].InstanceState = TenantInstanceError
		}
	}
	return nil
}

func nativeGuestObservationError(ctx context.Context, err error) error {
	if errors.Is(err, ErrUnjoined) {
		return err
	}
	return ctx.Err()
}
