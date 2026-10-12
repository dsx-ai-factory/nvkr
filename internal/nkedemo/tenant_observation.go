// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
)

const tenantClusterLabel = "cluster.x-k8s.io/cluster-name"
const tenantControlLabel = "cluster.x-k8s.io/control-plane"

// These projections intentionally omit bootstrap bytes, condition messages and
// console text. The pinned NKE/CAPNICo owners supply the role/instance binding.
type tenantMachineObservation struct {
	Kind     string
	Metadata struct {
		Name, Namespace     string
		Labels, Annotations map[string]string
	}
	Spec struct {
		ClusterName       string
		Labels            map[string]string
		InfrastructureRef struct{ Name, Kind, APIGroup, APIVersion string }
	}
	Status struct {
		InstanceID string
		Ready      *bool
		Conditions []struct{ Type, Status, Reason string }
	}
}

type tenantGuestBinding struct {
	instance string
	ready    TenantConditionStatus
	failure  TenantProvisioningFailure
}
type tenantConsoleSample struct {
	files   [2]os.FileInfo
	changed time.Time
}
type tenantGuestObserver struct {
	last      time.Time
	console   [2]tenantConsoleSample
	instances [2]string
}

func emptyTenantGuests() [2]TenantGuestObservation {
	return [2]TenantGuestObservation{
		{Role: TenantGuestControlPlane, Domain: TenantDomainUnknown, ProviderReady: TenantConditionUnknown},
		{Role: TenantGuestWorker, Domain: TenantDomainUnknown, ProviderReady: TenantConditionUnknown},
	}
}

func tenantGuestBindings(rows []tenantMachineObservation, name, id string) ([2]tenantGuestBinding, bool) {
	var result [2]tenantGuestBinding
	if len(rows) > 4 || name == "" || id == "" {
		return result, false
	}
	var seen [2]bool
	for _, machine := range rows {
		if machine.Kind != "Machine" {
			continue
		}
		if machine.Metadata.Labels[tenantClusterLabel] != name || machine.Spec.ClusterName != name || machine.Metadata.Namespace == "" {
			return result, false
		}
		role := 1
		if _, control := machine.Metadata.Labels[tenantControlLabel]; control {
			role = 0
		}
		if seen[role] {
			return result, false
		}
		seen[role] = true
		host := machine.Metadata.Annotations["nke.nvidia.com/host-id"]
		ref := machine.Spec.InfrastructureRef
		group := ref.APIGroup
		if group == "" {
			group, _, _ = strings.Cut(ref.APIVersion, "/")
		}
		if host == "" || ref.Name == "" || ref.Kind != "NicoMachine" || group != "infrastructure.cluster.x-k8s.io" {
			return result, false
		}
		matches := 0
		for _, infra := range rows {
			if infra.Kind != "NicoMachine" || infra.Metadata.Name != ref.Name || infra.Metadata.Namespace != machine.Metadata.Namespace {
				continue
			}
			matches++
			_, control := infra.Metadata.Labels[tenantControlLabel]
			if infra.Metadata.Labels[tenantClusterLabel] != name || control != (role == 0) || infra.Spec.Labels["nke.nvidia.com/node-id"] != host || infra.Spec.Labels["nke.nvidia.com/cluster-id"] != id {
				return result, false
			}
			ready := TenantConditionUnknown
			if infra.Status.Ready != nil {
				ready = TenantConditionFalse
				if *infra.Status.Ready {
					ready = TenantConditionTrue
				}
			}
			var failure TenantProvisioningFailure
			conditionCount := 0
			if len(infra.Status.Conditions) > 32 {
				return result, false
			}
			for _, condition := range infra.Status.Conditions {
				if condition.Type != "MachineProvisioned" {
					continue
				}
				conditionCount++
				if condition.Status == "False" && condition.Reason == "InstanceCreateFailed" {
					failure = TenantInstanceCreateFailed
				}
			}
			if conditionCount > 1 {
				return result, false
			}
			result[role] = tenantGuestBinding{instance: infra.Status.InstanceID, ready: ready, failure: failure}
		}
		if matches != 1 {
			return result, false
		}
	}
	if result[0].instance != "" && result[0].instance == result[1].instance {
		return [2]tenantGuestBinding{}, false
	}
	return result, true
}

func presentedDomainState(raw []byte) TenantDomainState {
	switch strings.TrimSpace(string(raw)) {
	case "running":
		return TenantDomainRunning
	case "paused":
		return TenantDomainPaused
	case "shut off":
		return TenantDomainShutOff
	case "crashed":
		return TenantDomainCrashed
	default:
		return TenantDomainUnknown
	}
}

// Each path component is held while opening its child; no console bytes are
// read. Missing/unsafe files provide no liveness, and cannot block on a FIFO.
func tenantConsoleMetadata(ctx context.Context, work, instance string) ([2]os.FileInfo, error) {
	var result [2]os.FileInfo
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	dir, info, err := securepath.OpenDirectory(work)
	if err != nil {
		return result, err
	}
	if !currentOwner(info) || info.Mode().Perm() != 0o700 {
		_ = dir.Close()
		return result, errors.New("guest workspace is not private and owned")
	}
	for _, name := range []string{"var", "instances", instance} {
		next, childInfo, readErr := securepath.OpenChild(dir, name, true)
		_ = dir.Close()
		if readErr != nil {
			return result, readErr
		}
		if !currentOwner(childInfo) {
			_ = next.Close()
			return result, errors.New("guest directory is not owned")
		}
		dir = next
	}
	defer dir.Close()
	for i, name := range []string{"disk.qcow2.ttyS0.log", "disk.qcow2.ttyS1.log"} {
		if err = requireContext(ctx); err != nil {
			return result, err
		}
		file, fileInfo, readErr := securepath.OpenChild(dir, name, false)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return result, readErr
		}
		_ = file.Close()
		if !currentOwner(fileInfo) {
			return result, errors.New("guest console is not owned")
		}
		result[i] = fileInfo
	}
	return result, nil
}

func (sample *tenantConsoleSample) observe(now time.Time, files [2]os.FileInfo, event *TenantGuestObservation) {
	var grew, reset bool
	for i, file := range files {
		old := sample.files[i]
		if file == nil {
			if old != nil {
				reset = true
			}
			continue
		}
		event.ConsoleKnown = true
		event.ConsoleBytes += file.Size()
		if old == nil || !os.SameFile(old, file) || file.Size() < old.Size() {
			reset = true
		} else if file.Size() > old.Size() {
			grew = true
		}
	}
	if reset {
		sample.changed = time.Time{}
	} else if grew {
		sample.changed = now
	}
	sample.files = files
	if !sample.changed.IsZero() {
		event.ConsoleChanged = true
		event.ConsoleLastChange = max(time.Duration(0), now.Sub(sample.changed)).Truncate(time.Second)
	}
}

func (observer *tenantGuestObserver) update(ctx context.Context, t *tenant, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if _, reported := t.session.Runner.Progress.(ProgressReporter); !reported {
		return nil
	}
	if !observer.last.IsZero() && now.Sub(observer.last) < 30*time.Second {
		return nil
	}
	observer.last = now
	guests := emptyTenantGuests()
	limited, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	config := environmentValue(t.session.Runner.Env, "SITE_KUBECONFIG")
	if config == "" {
		reportProgress(t.session.Runner.Progress, ProgressEvent{Kind: TenantGuests, Guests: guests})
		return nil
	}
	raw, err := t.capture(limited, t.kubectl(config, "--request-timeout=10s", "get", "machines.cluster.x-k8s.io,nicomachines.infrastructure.cluster.x-k8s.io", "--all-namespaces", "--selector", tenantClusterLabel+"="+t.name, "-o", "json"), "guest-bindings", 30*time.Second)
	if err == nil {
		var list struct{ Items []tenantMachineObservation }
		if json.Unmarshal(raw, &list) == nil {
			if bindings, valid := tenantGuestBindings(list.Items, t.name, t.id); valid {
				runner := t.domains
				runner.Work = ""
				runner.Progress = nil
				work := filepath.Join(filepath.Dir(t.session.Work), "simulator")
				for i, binding := range bindings {
					guests[i].ProvisioningFailure = binding.failure
					if binding.instance == "" {
						continue
					}
					if observer.instances[i] != binding.instance {
						observer.console[i] = tenantConsoleSample{}
						observer.instances[i] = binding.instance
					}
					owned, readErr := OwnedDomain(limited, runner, "nico-vm-"+binding.instance, work)
					if errors.Is(readErr, ErrUnjoined) {
						return readErr
					}
					if readErr != nil {
						err = errors.Join(err, readErr)
						continue
					}
					if owned == nil {
						continue
					}
					state, readErr := runner.Capture(limited, []string{"virsh", "-c", "qemu:///session", "domstate", owned.UUID}, "guest-domain-state", 30*time.Second)
					if errors.Is(readErr, ErrUnjoined) {
						return readErr
					}
					if readErr != nil {
						err = errors.Join(err, readErr)
						continue
					}
					guests[i].Available, guests[i].Domain, guests[i].ProviderReady = true, presentedDomainState(state), binding.ready
					if files, readErr := tenantConsoleMetadata(limited, work, binding.instance); readErr == nil {
						observer.console[i].observe(now, files, &guests[i])
					}
				}
			}
		}
	}
	if errors.Is(err, ErrUnjoined) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	reportProgress(t.session.Runner.Progress, ProgressEvent{Kind: TenantGuests, Guests: guests})
	return nil
}
