// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// admitNativeHost keeps backend and accelerator admission ahead of I/O. The
// Linux libvirt backend selects KVM when /dev/kvm exists; require usable KVM
// rather than silently promising an explicit TCG selection it cannot honor.
func admitNativeHost(platform, accelerator string) error {
	switch platform {
	case "darwin/arm64":
		if accelerator == "hvf" || accelerator == "tcg" {
			return nil
		}
	case "linux/arm64":
		if accelerator == "kvm" {
			return nil
		}
	}
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "native demo does not support host %q with accelerator %q", platform, accelerator)
}

// NICo creates each guest's NoCloud seed before launch. Its Darwin builder can
// fall back from cloud-localds to either ISO tool; Linux requires cloud-localds.
// Resolve only: admission must not execute a tool or create a probe ISO.
func admitNativeSeedISO(ctx context.Context, platform string, env []string) error {
	if err := requireContext(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "native seed ISO admission")
	}
	names := []string{"cloud-localds"}
	switch platform {
	case "darwin":
		names = append(names, "mkisofs", "genisoimage")
	case "linux":
	default:
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "native seed ISO does not support host %q", platform)
	}
	available := false
	for _, name := range names {
		if _, err := commandPath(name, env); err == nil {
			available = true
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "native seed ISO admission")
	}
	if !available {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "native guest seed ISO requires %s on PATH", strings.Join(names, " or "))
	}
	return nil
}

func admitNativeQEMUAdapter(platform, accelerator, executable string, site bool) error {
	if platform == "darwin/arm64" && (site || accelerator == "tcg") && (!filepath.IsAbs(executable) || filepath.Clean(executable) != executable) {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "native Mac site or TCG execution requires this demo's explicit adapter executable")
	}
	return nil
}

// prepareNativeLinux reuses the existing durable bridge, privilege admission,
// exact libvirt environment and cleanup owners. It does not install software,
// change sudo policy, adopt existing guests, or create a permanent site in Kind.
func prepareNativeLinux(ctx context.Context, runner *Runner, cfg NativeGuestConfig, simdir string) (_ *NetworkGuard, _ string, result error) {
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return nil, "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native Linux demo requires usable KVM")
	}
	if err := kvm.Close(); err != nil {
		return nil, "", err
	}
	for _, tool := range []string{"virsh", "qemu-img", "cloud-localds", "swtpm", "swtpm_setup"} {
		if _, err := commandPath(tool, runner.Env); err != nil {
			return nil, "", err
		}
	}
	binding, err := admitLibvirtEnvironment(ctx, *runner)
	if err != nil {
		return nil, "", err
	}
	runner.Env = SetEnv(runner.Env, binding)
	guard, err := admitNetworkGuard(ctx, *runner, "", cfg.DemoExecutable, true)
	if err != nil {
		return nil, "", err
	}
	noteRetainedNetworkStart(ctx)
	defer func() {
		if result == nil {
			return
		}
		if holdErr := waitRetainedCleanup(ctx, result); holdErr != nil {
			result = errors.Join(result, holdErr)
			guard.releaseHandles()
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if runtimeRetention(ctx) != nil {
			cleanupErr := guard.StopDNS(cleanup)
			recordRetainedCleanup(ctx, cleanupErr)
			result = errors.Join(result, cleanupErr)
			guard.releaseHandles()
			return
		}
		result = errors.Join(result, guard.Close(cleanup))
	}()
	defer recoverRetainedPanic(ctx, &result)
	if err := guard.Start(ctx); err != nil {
		return nil, "", err
	}
	if err := guard.allowNativeBootstrap(ctx, cfg.site != nil); err != nil {
		return nil, "", err
	}
	binary := filepath.Join(runner.Work, "nico-sim")
	if err := copyPrivate(ctx, filepath.Join(cfg.Tools, "nico-sim"), binary, 0700); err != nil {
		return nil, "", err
	}
	if err := guard.Grant(ctx, binary, networkCapabilities["nico-sim"]); err != nil {
		return nil, "", err
	}
	qemu, err := commandPath("qemu-img", runner.Env)
	if err != nil {
		return nil, "", err
	}
	cloud, err := commandPath("cloud-localds", runner.Env)
	if err != nil {
		return nil, "", err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{
		"NICO_SIM_DB_PATH":    filepath.Join(simdir, "var", "nico.db"),
		"NICO_VMS_VM_BACKEND": "libvirt", "NICO_VMS_LIBVIRT_URI": "qemu:///session",
		"NICO_VMS_LIBVIRT_NETWORK": "", "NICO_VMS_BRIDGE_NAME": BridgeName,
		"NICO_VMS_QEMU_IMG_PATH": qemu, "NICO_VMS_CLOUD_LOCALDS_PATH": cloud,
		"NICO_VMS_QEMU_ACCELERATOR": "", "NICO_VMS_DHCP_HOSTS_DIR": filepath.Join(guard.Work, "dhcp-hosts"),
		"NICO_VMS_DHCP_LEASE_FILE": filepath.Join(guard.Work, "leases"),
	})
	return guard, binary, nil
}

// These five exact optional grants extend the existing durable network journal.
// Ports 18081/18082 serve the private site handoff and selected disk. The
// SPIRE NodePort 30081 is published only by this attempt's bootstrap; Kind needs the
// pinned NVKE node-agent (15005) and Kubernetes API (6443) on the owned VM range.
func nativeBootstrapRules(owner, kind string) []networkRule {
	var rules []networkRule
	for _, port := range []string{"18081", "18082", "30081"} {
		rules = append(rules, networkRule{"filter", "INPUT", []string{"-m", "comment", "--comment", owner, "-i", BridgeName, "-s", GuestCIDR, "-d", GuestGateway + "/32", "-p", "tcp", "--dport", port, "-j", "ACCEPT"}})
	}
	if kind != "" {
		for _, port := range []string{"15005", "6443"} {
			rules = append(rules, networkRule{"filter", "FORWARD", []string{"-m", "comment", "--comment", owner, "-s", kind, "-d", GuestCIDR, "-p", "tcp", "--dport", port, "-j", "ACCEPT"}})
		}
	}
	return rules
}

func (g *NetworkGuard) allowNativeBootstrap(ctx context.Context, site bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cleaning || g.finished || !g.BridgeIntent {
		return errors.New("native bootstrap grants require an active owned bridge")
	}
	rules := nativeBootstrapRules(g.Owner, g.KindCIDR)
	if !site {
		rules = rules[1:2]
	} // standalone guest needs only its disk HTTP port18082
	for _, row := range rules {
		exists, err := g.ruleExists(ctx, row)
		if err != nil {
			return err
		}
		if exists {
			return errors.New("fresh native bootstrap grant already exists")
		}
		g.rules = append(g.rules, row)
		if err := g.checkpoint(ctx); err != nil {
			return err
		}
		if err := g.ruleCommand(ctx, row, "-I"); err != nil {
			return err
		}
		exists, err = g.ruleExists(ctx, row)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("native bootstrap grant did not survive readback")
		}
	}
	return nil
}

// nativeLinuxVMEvidence checks local VM bindings through libvirt's own
// XML/UUID observations and the existing private-domain owner. This diagnostic
// checks each requested guest against the same selected standalone QCOW bytes.
func nativeLinuxVMEvidence(ctx context.Context, runner Runner, work, digest string, expected int) ([]domainEvidence, error) {
	if expected != 1+nativeTenantNodeCount(false) && expected != 1+nativeTenantNodeCount(true) {
		return nil, errors.New("unsupported native VM topology")
	}
	runner.Work = ""
	names, err := DomainInventory(ctx, runner)
	if err != nil {
		return nil, err
	}
	if len(names) != expected {
		return nil, fmt.Errorf("require exactly %d real simulator-owned site and tenant libvirt domains", expected)
	}
	slices.Sort(names)
	var observed []domainEvidence
	for _, name := range names {
		binding, err := OwnedDomain(ctx, runner, name, work)
		if err != nil {
			return nil, err
		}
		if binding == nil {
			return nil, errors.New("native guest domain is outside this private simulator")
		}
		var domain struct {
			Type string     `xml:"type,attr"`
			TPM  []struct{} `xml:"devices>tpm"`
			OS   struct {
				Type struct {
					Arch string `xml:"arch,attr"`
				} `xml:"type"`
			} `xml:"os"`
		}
		if err := xml.Unmarshal([]byte(binding.XML), &domain); err != nil {
			return nil, err
		}
		if domain.Type != "kvm" || domain.OS.Type.Arch != "aarch64" || len(domain.TPM) != 1 {
			return nil, errors.New("native guest lacks its real ARM64 KVM/TPM configuration")
		}
		disk := filepath.Join(work, "var", "instances", strings.TrimPrefix(name, "nico-vm-"), "disk.qcow2")
		raw, err := runner.Capture(ctx, []string{"qemu-img", "info", "--force-share", "--output=json", disk}, "native-disk-observation", 30*time.Second)
		if err != nil {
			return nil, err
		}
		var info struct {
			Backing string `json:"full-backing-filename"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(info.Backing) {
			return nil, errors.New("native guest omits its absolute backing image")
		}
		actual, err := DigestFile(ctx, info.Backing)
		if err != nil {
			return nil, err
		}
		if actual != digest {
			return nil, errors.New("native guest backing differs from the selected disk")
		}
		observed = append(observed, domainEvidence{name, binding.UUID, actual, raw})
	}
	return observed, nil
}
