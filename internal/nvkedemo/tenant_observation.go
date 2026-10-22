// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
)

const tenantClusterLabel = "cluster.x-k8s.io/cluster-name"
const tenantControlLabel = "cluster.x-k8s.io/control-plane"

// These projections intentionally omit bootstrap bytes, condition messages and
// console text. The pinned NVKE/CAPNICo owners supply the role/instance binding.
type tenantObjectOwner struct {
	APIVersion, Kind, Name, UID string
	Controller                  *bool
}

type tenantMachineObservation struct {
	Kind     string
	Metadata struct {
		Name, Namespace, UID string
		OwnerReferences      []tenantObjectOwner
		Labels, Annotations  map[string]string
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

func emptyTenantGuests() [3]TenantGuestObservation {
	return [3]TenantGuestObservation{
		{Role: TenantGuestControlPlane, Domain: TenantDomainUnknown, ProviderReady: TenantConditionUnknown},
		{Role: TenantGuestWorker, Domain: TenantDomainUnknown, ProviderReady: TenantConditionUnknown},
	}
}

func tenantGuestBindingsForCount(rows []tenantMachineObservation, name, id string, expected int, native bool) ([3]tenantGuestBinding, bool) {
	var result [3]tenantGuestBinding
	if (expected != 2 && expected != 3) || len(rows) > 2*expected || name == "" || (!native && id == "") {
		return result, false
	}
	// Stable worker ordering describes the same CAPI Machine across polls.
	// Never mutate a caller's inventory or infer role from a provider name.
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b tenantMachineObservation) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	var seen [3]bool
	worker := 1
	clusterUID, namespace := "", ""
	for _, machine := range rows {
		if machine.Kind != "Machine" {
			continue
		}
		if machine.Metadata.Labels[tenantClusterLabel] != name || machine.Spec.ClusterName != name || machine.Metadata.Namespace == "" {
			return result, false
		}
		role := 0
		if _, control := machine.Metadata.Labels[tenantControlLabel]; !control {
			role = worker
			worker++
		}
		if role >= expected || seen[role] {
			return result, false
		}
		seen[role] = true
		host := machine.Metadata.Annotations["nke.nvidia.com/host-id"]
		if native {
			if machine.Metadata.UID == "" || (namespace != "" && namespace != machine.Metadata.Namespace) {
				return result, false
			}
			namespace = machine.Metadata.Namespace
			matches := 0
			for _, owner := range machine.Metadata.OwnerReferences {
				group, _, _ := strings.Cut(owner.APIVersion, "/")
				if group == "cluster.x-k8s.io" && owner.Kind == "Cluster" && owner.Name == name && owner.UID != "" {
					if clusterUID != "" && clusterUID != owner.UID {
						return result, false
					}
					clusterUID = owner.UID
					matches++
				}
			}
			if matches != 1 {
				return result, false
			}
		}
		ref := machine.Spec.InfrastructureRef
		group := ref.APIGroup
		if group == "" {
			group, _, _ = strings.Cut(ref.APIVersion, "/")
		}
		if (!native && host == "") || ref.Name == "" || ref.Kind != "NicoMachine" || group != "infrastructure.cluster.x-k8s.io" {
			return result, false
		}
		matches := 0
		for _, infra := range rows {
			if infra.Kind != "NicoMachine" || infra.Metadata.Name != ref.Name || infra.Metadata.Namespace != machine.Metadata.Namespace {
				continue
			}
			matches++
			_, control := infra.Metadata.Labels[tenantControlLabel]
			if infra.Metadata.Labels[tenantClusterLabel] != name {
				return result, false
			}
			if native {
				ownerMatches := 0
				for _, owner := range infra.Metadata.OwnerReferences {
					group, _, _ := strings.Cut(owner.APIVersion, "/")
					if group == "cluster.x-k8s.io" && owner.Kind == "Machine" && owner.Name == machine.Metadata.Name && owner.UID == machine.Metadata.UID && owner.Controller != nil && *owner.Controller {
						ownerMatches++
					}
				}
				if infra.Metadata.UID == "" || ownerMatches != 1 {
					return result, false
				}
			} else if control != (role == 0) || infra.Spec.Labels["nke.nvidia.com/node-id"] != host || infra.Spec.Labels["nke.nvidia.com/cluster-id"] != id {
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
	for i, binding := range result {
		if binding.instance == "" {
			continue
		}
		for j := 0; j < i; j++ {
			if result[j].instance == binding.instance {
				return [3]tenantGuestBinding{}, false
			}
		}
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
	return tenantConsoleMetadataForBackend(ctx, work, instance, "libvirt")
}

func tenantConsoleMetadataForBackend(ctx context.Context, work, instance, backend string) ([2]os.FileInfo, error) {
	var result [2]os.FileInfo
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	directories := []string{"instances", instance}
	members := []string{"disk.qcow2.ttyS0.log", "disk.qcow2.ttyS1.log"}
	switch backend {
	case "libvirt":
		directories = append([]string{"var"}, directories...)
	case "darwin-qemu":
		members = []string{"console.log"}
	default:
		return result, errors.New("unsupported guest console backend")
	}
	dir, info, err := securepath.OpenDirectory(work)
	if err != nil {
		return result, err
	}
	if !currentOwner(info) || info.Mode().Perm() != 0o700 {
		_ = dir.Close()
		return result, errors.New("guest workspace is not private and owned")
	}
	for _, name := range directories {
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
	for i, name := range members {
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

// Native Linux presentation uses the held domain and console observation.
// A CAPI binding or NICo readiness alone never proves a process.
func observeTenantDomain(ctx context.Context, runner Runner, work, instance string, console *tenantConsoleSample, guest *TenantGuestObservation, now time.Time) (bool, error) {
	owned, err := OwnedDomain(ctx, runner, "nico-vm-"+instance, work)
	if err != nil || owned == nil {
		return false, err
	}
	state, err := runner.Capture(ctx, []string{"virsh", "-c", "qemu:///session", "domstate", owned.UUID}, "guest-domain-state", 30*time.Second)
	if err != nil {
		return false, err
	}
	guest.Domain = presentedDomainState(state)
	if files, readErr := tenantConsoleMetadata(ctx, work, instance); readErr == nil {
		console.observe(now, files, guest)
	}
	return true, nil
}
