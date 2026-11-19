// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

// NativeResources describes downstream guest sizing and additional host/QEMU
// headroom. It changes neither selected artifact bytes nor their build identity.
// Zero selects the existing shared defaults; a memory/headroom override must be
// complete. Zero VCPUs preserves the existing six-vCPU site and tenant shape.
type NativeResources struct {
	SiteMemoryGiB   int    `yaml:"siteMemoryGiB" json:"siteMemoryGiB"`
	TenantMemoryGiB int    `yaml:"tenantMemoryGiB" json:"tenantMemoryGiB"`
	HeadroomMiB     int    `yaml:"headroomMiB" json:"headroomMiB"`
	VCPUs           int    `yaml:"vcpus,omitempty" json:"vcpus,omitempty"`
	Profile         string `yaml:"profile,omitempty" json:"profile,omitempty"`
}

func selectedNativeResources(value NativeResources) (NativeResources, error) {
	if err := admitNativeResourceProfile(value.Profile, siteOVNVersion); err != nil {
		return NativeResources{}, err
	}
	if value == (NativeResources{Profile: value.Profile}) {
		value = NativeResources{SiteMemoryGiB: 12, TenantMemoryGiB: tenantDemoMemoryGiB, HeadroomMiB: 4096, Profile: value.Profile}
	}
	if value.VCPUs == 0 {
		value.VCPUs = tenantDemoVCPUs
	}
	// Bound descriptive arithmetic before converting to unsigned capacity units.
	if value.SiteMemoryGiB < 1 || value.SiteMemoryGiB > 1<<20 || value.TenantMemoryGiB < 1 || value.TenantMemoryGiB > 1<<20 || value.HeadroomMiB < 1 || value.HeadroomMiB > 1<<30 || value.VCPUs < 1 || value.VCPUs > 1<<20 {
		return NativeResources{}, configUsage("native resources require positive bounded siteMemoryGiB, tenantMemoryGiB, headroomMiB and vcpus")
	}
	return value, nil
}

// Small is an explicit functional-test candidate, not a physical minimum or
// performance qualification. Empty preserves the existing native configuration.
func admitNativeResourceProfile(profile, ovnVersion string) error {
	switch profile {
	case "":
		return nil
	case "small":
		if ovnVersion == siteOVNVersion {
			return nil
		}
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "small native resource profile requires NVIDIA OVN %s", siteOVNVersion)
	default:
		return configUsage(fmt.Sprintf("unknown native resource profile %q", profile))
	}
}

// An opt-in profile must be admitted before host, path or artifact work. Empty
// keeps each existing boundary's validation order and default behavior intact.
func admitNativeResourcesForNetwork(resources NativeResources, network string) error {
	if resources.Profile == "" {
		return nil
	}
	if _, err := selectedNativeResources(resources); err != nil {
		return err
	}
	mode, err := nativeDeploymentNetwork(network)
	if err != nil {
		return err
	}
	version, _, _, err := selectedOVNOutputs(mode)
	if err != nil {
		return err
	}
	return admitNativeResourceProfile(resources.Profile, version)
}

func nativeReservedMemory(profile string) string {
	if profile == "small" {
		return "256Mi"
	}
	return "1Gi"
}

func nativeSmallKubeletScript(operatorFile, unitFile string) string {
	// A map-valued flag replaces the whole map. Preserve source CPU/disk values.
	const flags = "--kube-reserved=cpu=1000m,memory=256Mi,ephemeral-storage=1Gi"
	const args = "\nKUBELET_EXTRA_ARGS=\"" + flags + "\"\n"
	// systemd owns EnvironmentFile parsing; no shell eval or approximate parser.
	// The transient child has independent bounds and is joined and collected.
	// https://www.freedesktop.org/software/systemd/man/systemd-run.html
	owner := func(file string) string {
		return "systemd-run --quiet --wait --pipe --collect --expand-environment=no --property=Type=oneshot --property=TimeoutStartSec=10s --property=TimeoutStopSec=5s " + file + " /bin/sh -ec "
	}
	return "( test -f " + operatorQuote(unitFile) + " && test ! -L " + operatorQuote(unitFile) + " || exit 1; " +
		"grep -Fx -- 'EnvironmentFile=-/etc/default/kubelet' " + operatorQuote(unitFile) + " >/dev/null; " +
		"grep -Fx -- 'ExecStart=/usr/bin/kubelet $KUBELET_KUBECONFIG_ARGS $KUBELET_CONFIG_ARGS $KUBELET_KUBEADM_ARGS $KUBELET_EXTRA_ARGS' " + operatorQuote(unitFile) + " >/dev/null; " +
		"test -f " + operatorQuote(operatorFile) + " && test ! -L " + operatorQuote(operatorFile) + " && test -O " + operatorQuote(operatorFile) + " || exit 1; " +
		"test \"$(wc -c < " + operatorQuote(operatorFile) + ")\" -le 65536; " +
		owner(operatorQuote("--property=EnvironmentFile="+operatorFile)) + operatorQuote(`test -z "${KUBELET_EXTRA_ARGS-}"`) + "; " +
		"candidate=$(mktemp " + operatorQuote(operatorFile+".nvkr-small.XXXXXX") + "); trap 'rm -f -- \"$candidate\"' EXIT; " +
		"cat " + operatorQuote(operatorFile) + " > \"$candidate\"; printf '%s' " + operatorQuote(args) + " >> \"$candidate\"; " +
		owner(`"--property=EnvironmentFile=$candidate"`) + operatorQuote(`test "$KUBELET_EXTRA_ARGS" = `+operatorQuote(flags)) + "; " +
		"mv -f -- \"$candidate\" " + operatorQuote(operatorFile) + " )"
}

func nativeKubeletProfilePreparation(ctx context.Context, profile string) (string, error) {
	if err := admitNativeResourceProfile(profile, siteOVNVersion); err != nil {
		return "", err
	}
	if profile != "small" {
		return "", nil
	}
	if _, err := remainingWork(ctx); err != nil {
		return "", err
	}
	deadline, _ := ctx.Deadline()
	// Two manager-owned oneshots each allow start/stop 10+5s. Unlike Type=exec,
	// a signal cannot count as successful completion of the shell predicate.
	// Refuse before spawning if the outer guest deadline cannot join both.
	return fmt.Sprintf("test $((%d - $(date +%%s))) -ge 60\nsystemctl stop kubelet.service\n", deadline.Unix()) + nativeSmallKubeletScript("/etc/default/kubelet", "/lib/systemd/system/kubelet.service.d/10-kubeadm.conf") + "\n", nil
}

type nativeResourceAdmission struct {
	resources     NativeResources
	tenantWorkers int
	conformance   bool
	observe       func(context.Context, string) (execution.ResourceCapacity, error)
}

func observeNativeResources(ctx context.Context, work string) (execution.ResourceCapacity, error) {
	if err := requireContext(ctx); err != nil {
		return execution.ResourceCapacity{}, err
	}
	directory, _, err := securepath.OpenDirectory(work)
	if err != nil {
		return execution.ResourceCapacity{}, err
	}
	defer directory.Close()
	facts, err := execution.ObserveLocalHost(ctx, directory)
	return facts.Resources, err
}

func (admission nativeResourceAdmission) check(ctx context.Context, work string, tenantOnly bool, output io.Writer) error {
	if err := requireContext(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "native guest capacity observation")
	}
	tenantNodes, err := selectedNativeTenantNodes(admission.tenantWorkers, admission.conformance)
	if err != nil {
		return err
	}
	resources, err := selectedNativeResources(admission.resources)
	if err != nil {
		return err
	}
	observe := admission.observe
	if observe == nil {
		observe = observeNativeResources
	}
	actual, err := observe(ctx, filepath.Clean(work))
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "observe native guest capacity")
	}
	tenantMiB := uint64(tenantNodes) * uint64(resources.TenantMemoryGiB) * 1024
	needed := tenantMiB
	if !tenantOnly {
		// The temporary 6-GiB bootstrap backend is joined before tenants start.
		// Admit the larger phase, never the sum of two non-overlapping phases.
		needed = uint64(resources.SiteMemoryGiB)*1024 + max(tenantMiB, 6*1024)
	}
	needed += uint64(resources.HeadroomMiB)
	// vCPUs are time-shared: admit the largest VM, not the sum of all vCPUs.
	largest := resources.VCPUs
	if !tenantOnly {
		largest = max(largest, 4) // The separately joined bootstrap backend uses four CPUs.
	}
	if actual.CPUMillis < uint64(largest)*1000 || actual.MemoryMiB < needed {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "native guests require %d available MiB including host/QEMU headroom and %d host CPUs; observed %d MiB and %d CPU milliunits; observation reserves nothing", needed, largest, actual.MemoryMiB, actual.CPUMillis)
	}
	if output != nil {
		_, err = fmt.Fprintf(output, "Native guest capacity: available memory %d/%d MiB; largest VM %d CPUs. Not reserved.\n", actual.MemoryMiB, needed, largest)
	}
	return err
}
