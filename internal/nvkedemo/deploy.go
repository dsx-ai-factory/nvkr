// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// AdmitDemoDeployment checks native capabilities and the fresh output destination
// before build, sudo prompts, artifact capture, storage, or guest creation.
func AdmitDemoDeployment(ctx context.Context, cfg Config) error {
	if _, err := selectedNativeTenantNodes(cfg.TenantWorkers, cfg.Conformance); err != nil {
		return err
	}
	if !cfg.Cleanup && runtimeRetention(ctx) == nil {
		if err := checkRetainedPeerCapability(ctx); err != nil {
			return err
		}
	}
	if _, err := nativeDeploymentNetwork(cfg.Network); err != nil {
		return err
	}
	if cfg.Conformance {
		if runtime.GOARCH != "arm64" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			return nvkrerr.New(nvkrerr.CodeEngineCapability, "local demo conformance requires native Mac or Linux ARM64")
		}
		if !configString(cfg.Repository) {
			return configUsage("conformance requires the repository containing its captured artifact Run")
		}
		if _, err := resolveExisting(cfg.Repository); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeUsage, err, "conformance repository")
		}
	}
	if _, err := nativeAccelerator(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator); err != nil {
		return err
	}
	if err := AdmitNativeDeployment(ctx, cfg.Accelerator); err != nil {
		return err
	}
	// Recheck at guest creation; this preflight does not reserve the path.
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeUsage, err, "deployment output requires a fresh destination with an existing parent")
	}
	capacity := nativeResourceAdmission{resources: cfg.Resources, tenantWorkers: cfg.TenantWorkers, conformance: cfg.Conformance}
	if err := capacity.check(ctx, filepath.Dir(out), false, cfg.Progress); err != nil {
		return err
	}
	_, _, err = requireBootstrapLima(ctx, Runner{Env: NativeEnvironment()}, runtime.GOOS)
	return err
}

func nativeDeploymentNetwork(network string) (string, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", err
	}
	if mode == "kind-calico" {
		return "", nvkrerr.New(nvkrerr.CodeEngineCapability, "native site requires the selected OVN disk and profiles")
	}
	return mode, nil
}

// AdmitNativeDeployment rejects an unusable guest accelerator before an
// expensive BUILD or any deployment workspace, process or VM is created.
func AdmitNativeDeployment(ctx context.Context, accelerator string) error {
	if err := requireContext(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "native deployment admission")
	}
	if _, err := nativeAccelerator(runtime.GOOS+"/"+runtime.GOARCH, accelerator); err != nil {
		return err
	}
	if err := admitNativeSeedISO(ctx, runtime.GOOS, NativeEnvironment()); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := admitNativeAddresses(ctx, NativeEnvironment()); err != nil {
			return err
		}
	}
	if runtime.GOOS == "linux" {
		device, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
		if err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native deployment requires usable KVM; software-emulated tenant fallback is unavailable")
		}
		if err := device.Close(); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "close native deployment KVM admission")
		}
	}
	return nil
}

func nativeAccelerator(platform, requested string) (string, error) {
	if requested == "" {
		switch platform {
		case "darwin/arm64":
			requested = "hvf"
		case "linux/arm64":
			requested = "kvm"
		}
	}
	return requested, admitNativeHost(platform, requested)
}

// DeployArtifacts is the BUILD-to-DEPLOY handoff used by both run and e2e.
// NVKR owns the disk and tool outputs; one native workflow owns all three VMs.
func DeployArtifacts(ctx context.Context, cfg Config) error {
	selected, err := nativeDeploymentConfig(ctx, cfg, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		var coded *nvkrerr.Error
		if errors.As(err, &coded) && coded.Code == nvkrerr.CodeEngineCapability {
			return err
		}
		code := nvkrerr.CodePreflightFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = nvkrerr.CodeTimeout
		}
		return nvkrerr.Wrap(code, err, "select native deployment from NVKR outputs")
	}
	if !cfg.Cleanup && runtimeRetention(ctx) == nil {
		return launchRetainedNative(ctx, cfg)
	}
	return RunNative(ctx, selected)
}

func nativeDeploymentConfig(ctx context.Context, cfg Config, platform string) (NativeConfig, error) {
	if err := requireContext(ctx); err != nil {
		return NativeConfig{}, err
	}
	if _, err := selectedNativeTenantNodes(cfg.TenantWorkers, cfg.Conformance); err != nil {
		return NativeConfig{}, err
	}
	if _, err := selectedNativeResources(cfg.Resources); err != nil {
		return NativeConfig{}, err
	}
	if _, err := nativeDeploymentNetwork(cfg.Network); err != nil {
		return NativeConfig{}, err
	}
	if cfg.Conformance && platform != "linux/arm64" && platform != "darwin/arm64" {
		return NativeConfig{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "local demo conformance requires native Mac or Linux ARM64")
	}
	conformance, err := selectNativeConformance(cfg)
	if err != nil {
		return NativeConfig{}, err
	}
	accelerator, err := nativeAccelerator(platform, cfg.Accelerator)
	if err != nil {
		return NativeConfig{}, err
	}
	if cfg.Runs.OS == "" {
		return NativeConfig{}, nvkrerr.New(nvkrerr.CodeUsage, "deployment requires a completed OS Run")
	}
	name, err := selectedDiskOutput(cfg.Network)
	if err != nil {
		return NativeConfig{}, err
	}
	root, err := filepath.Abs(cfg.Runs.OS)
	if err != nil {
		return NativeConfig{}, err
	}
	_, image, err := nativeSelectedRunPlan(ctx, root, name, "disk-image")
	if err != nil {
		return NativeConfig{}, err
	}
	if image.Disk == nil || image.Disk.Format != "qcow2" || image.Disk.Encryption != "none" || image.Disk.Outcome != "succeeded" || image.Disk.PayloadDigest != image.Digest || image.Disk.Size <= 0 {
		return NativeConfig{}, nvkrerr.New(nvkrerr.CodeVerifyFailed, "deployment requires the successful observed plain QCOW2 output")
	}
	disk := filepath.Join(root, "outputs", name+".qcow2")
	if err := verifyDiskBytes(ctx, disk, image); err != nil {
		return NativeConfig{}, err
	}
	runtimeRun := cfg.Runs.Runtime
	if runtimeRun == "" {
		runtimeRun = cfg.Runs.Components
	}
	return NativeConfig{
		Image: disk, Out: cfg.Out, Accelerator: accelerator,
		Network: cfg.Network, DemoExecutable: cfg.DemoExecutable,
		conformance: conformance, TenantWorkers: cfg.TenantWorkers, Resources: cfg.Resources,
		Cleanup: cfg.Cleanup, Walkthrough: cfg.Walkthrough, Progress: cfg.Progress, TenantTimeout: cfg.Timeout,
		Assets: NativeAssetConfig{
			OSRun: cfg.Runs.OS, ToolsRun: cfg.Runs.Tools, HostToolsRun: cfg.Runs.HostTools,
			BootstrapRun: cfg.Runs.Components, ControllerRun: cfg.Runs.Site,
			DeploymentRun: cfg.Runs.Profiles, ProvidersRun: cfg.Runs.Providers,
			RuntimeRun: runtimeRun,
		},
	}, nil
}
