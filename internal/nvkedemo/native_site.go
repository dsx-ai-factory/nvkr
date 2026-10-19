// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

// NativeConfig selects local artifacts for one site and two tenant QCOW guests.
// Cleanup false keeps the foreground environment until its context is canceled.
type NativeConfig struct {
	TenantTimeout           time.Duration
	Image, Out, Accelerator string
	Network, DemoExecutable string
	conformance             *nativeConformanceConfig
	Assets                  NativeAssetConfig
	Cleanup, Walkthrough    bool
	Progress                io.Writer
}

type nativeSiteAttempt struct {
	tenantTimeout             time.Duration
	conformance               *nativeConformanceConfig
	assets                    NativeAssets
	handoff                   *nativeHandoff
	files                     string
	cleanup, walkthrough      bool
	sequence                  int
	bootstrapName             string
	bootstrapRunner           *Runner
	bootstrap                 *hostBootstrap
	network                   string
	bridge                    nativeBridgeConfig
	guard                     *NetworkGuard
	tenantOwner               *tenant
	diskDigest, simulatorWork string
	nodes                     []tenantNode
	workload                  json.RawMessage
}

// RunNative runs source-owned site bootstrap, deployment and tenant commands
// on the execution host, with one site and two tenant QCOW guests.
func RunNative(ctx context.Context, cfg NativeConfig) (result error) {
	defer func() {
		if result != nil {
			result = nvkrerr.Wrap(nvkrerr.CodePreflightFailed, result, "native NVKE demo")
		}
	}()
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := admitNativeHost(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator); err != nil {
		return err
	}
	if cfg.conformance != nil && runtime.GOARCH != "arm64" {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "local conformance requires native ARM64")
	}
	if err := admitBootstrapHost(runtime.GOOS, NativeEnvironment()); err != nil {
		return err
	}
	if cfg.TenantTimeout != 0 && (cfg.TenantTimeout < MinTenantTimeoutSeconds*time.Second || cfg.TenantTimeout > MaxTenantTimeoutSeconds*time.Second) {
		return nvkrerr.New(nvkrerr.CodeUsage, "native tenant budget must be 300..7200 seconds")
	}
	mode, err := nativeDeploymentNetwork(cfg.Network)
	if err != nil {
		return err
	}
	if cfg.Image == "" || cfg.Assets.RuntimeRun == "" {
		return errors.New("require a local QCOW, explicit accelerator and completed runtime-image Run")
	}
	if _, err := DigestFile(ctx, cfg.Image); err != nil {
		return err
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return err
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	cfg.Assets.Network = mode
	cfg.Assets.Out = filepath.Join(out, "assets")
	cfg.Assets.Progress = cfg.Progress
	preparation := beginProgress(cfg.Progress, "installer-preparation", PhaseStarted, PhaseFinished, PhaseFailed)
	assets, err := PrepareNativeAssets(ctx, cfg.Assets)
	preparation.finish(err)
	if err != nil {
		return err
	}
	attempt := &nativeSiteAttempt{assets: assets, cleanup: cfg.Cleanup, walkthrough: cfg.Walkthrough, network: mode, tenantTimeout: cfg.TenantTimeout, conformance: cfg.conformance}
	result = RunNativeGuest(ctx, NativeGuestConfig{
		Image: cfg.Image, Tools: assets.HostTools, Out: filepath.Join(out, "runtime"),
		Accelerator: cfg.Accelerator, Timeout: 30 * time.Minute, Progress: cfg.Progress, site: attempt, DemoExecutable: cfg.DemoExecutable,
	})
	if result == nil && cfg.Cleanup && cfg.Progress != nil {
		fmt.Fprintln(cfg.Progress, "PASS: native site and tenant checks, natural tenant deletion and joined owned cleanup completed.")
	}
	return result
}

func nativeSiteSeed(ctx context.Context, source, out string) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	items, ok := doc["instance_types"].([]any)
	if !ok {
		return "", errors.New("source seed lacks instance types")
	}
	var site, tenant map[string]any
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return "", errors.New("malformed instance type in source seed")
		}
		switch row["id"] {
		case "a1b2c3d4-0005-4000-8000-000000000004":
			if site != nil || row["name"] != "cpu6-mem32gb-disk60gb" || row["site_id"] != SiteID || row["gpu_count"] != 0 || row["cpu_count"] != 6 || row["memory_gib"] != 32 || row["disk_gib"] != 60 {
				return "", errors.New("unexpected source site instance type")
			}
			site = row
		case TenantInstanceType:
			if tenant != nil || row["name"] != "cpu4-mem6gb-disk60gb" || row["site_id"] != SiteID || row["gpu_count"] != 0 || row["cpu_count"] != 4 || row["memory_gib"] != 6 || row["disk_gib"] != 60 {
				return "", errors.New("unexpected source tenant instance type")
			}
			tenant = row
		}
	}
	if site == nil || tenant == nil {
		return "", errors.New("source seed omits selected site or tenant instance type")
	}
	// Change only runtime sizing after both selected source rows are admitted.
	// Source artifacts remain untouched, and tenant CNI requests remain intact.
	site["name"], site["cpu_count"], site["memory_gib"] = "native-site-cpu4-mem12gb-disk60gb", 4, 12
	tenant["name"], tenant["cpu_count"], tenant["memory_gib"] = fmt.Sprintf("cpu%d-mem%dgb-disk60gb", tenantDemoVCPUs, tenantDemoMemoryGiB), tenantDemoVCPUs, tenantDemoMemoryGiB
	raw, err = yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	path := filepath.Join(out, "native-seed.yaml")
	return path, os.WriteFile(path, raw, 0600)
}

func (s *nativeSiteAttempt) share(ctx context.Context, files string) error {
	s.files = files
	if err := bootstrapCA(ctx, ManagementSession{Work: filepath.Dir(files), Runner: Runner{Work: filepath.Dir(files), Env: NativeEnvironment()}}); err != nil {
		return err
	}
	for name, source := range s.sources() {
		if err := copyPrivate(ctx, source, filepath.Join(files, name), 0600); err != nil {
			return err
		}
	}
	return nil
}

func (s *nativeSiteAttempt) sources() map[string]string {
	result := map[string]string{"nerdctl": filepath.Join(s.assets.SiteTools, "nerdctl"), "spire-ca.crt": filepath.Join(filepath.Dir(s.files), "ca.crt")}
	for role, image := range s.assets.Images {
		if role != "provisioned-provider" {
			result[role+".tar"] = image.File
		}
	}
	return result
}

func (s *nativeSiteAttempt) startup(ctx context.Context, api, diskURL, out string) ([]byte, error) {
	port := "0"
	if s.guard != nil {
		port = "18081"
	}
	handoff, err := startNativeHandoffAt(ctx, s.bridge.gateway.String(), port, out)
	if err != nil {
		return nil, err
	}
	s.handoff = handoff
	version, _, _, err := selectedOVNOutputs(s.network)
	if err != nil {
		return nil, err
	}
	cfg := NativeSiteStartup{OVNVersion: version, Name: "nvke-native-site", Gateway: s.bridge.gateway.String(),
		ControllerImage: s.assets.Images["controller"].Reference, ProviderImage: s.assets.Images["provisioned-provider"].Reference,
		HandoffURL: handoff.URL, HandoffToken: handoff.Token, HandoffCertificate: handoff.Certificate,
		Deployment:    &NativeSiteDeployment{NicoURL: api, ImageURL: diskURL, ProfilesImage: s.assets.Images["profiles"].Reference},
		RuntimeImages: len(s.assets.RuntimeOutputs) != 0,
	}
	if err := s.releases(ctx, diskURL, api, out); err != nil {
		return nil, err
	}
	for name := range s.sources() {
		digest, err := DigestFile(ctx, filepath.Join(s.files, name))
		if err != nil {
			return nil, err
		}
		cfg.Assets = append(cfg.Assets, StartupAsset{Name: name, URL: api + "/files/" + name, Digest: digest})
	}
	data, err := SiteStartupUserData(ctx, cfg)
	return []byte(data), err
}

func (s *nativeSiteAttempt) close(ctx context.Context) error {
	var cleanup error
	if s.tenantOwner != nil {
		runner := s.tenantOwner.session.Runner
		if err := observeNativeAPI(ctx, runner, s.tenantOwner.cli()); errors.Is(err, ErrUnjoined) {
			return errors.Join(err, s.handoff.Stop(ctx))
		}
		cleanup = s.tenantOwner.restore(ctx)
		if errors.Is(cleanup, ErrUnjoined) {
			return errors.Join(cleanup, s.handoff.Stop(ctx))
		}
		if cleanup != nil {
			cleanup = retainNativeSiteFailure(ctx, runner, s.assets.HostTools, filepath.Join(s.tenantOwner.session.Work, "site.kubeconfig"), cleanup)
			if errors.Is(cleanup, ErrUnjoined) {
				return errors.Join(cleanup, s.handoff.Stop(ctx))
			}
		}
	}
	cleanup = errors.Join(cleanup, s.stopBootstrap(ctx))
	return errors.Join(cleanup, s.handoff.Stop(ctx))
}

func (s *nativeSiteAttempt) stopBootstrap(ctx context.Context) error {
	if s.bootstrapRunner != nil {
		s.sequence++
		phase := fmt.Sprintf("native-site-%03d-host-bootstrap-cleanup", s.sequence)
		if err := s.bootstrapRunner.Run(ctx, []string{filepath.Join(s.assets.HostTools, "kind"), "delete", "cluster", "--name", s.bootstrapName}, phase, 2*time.Minute); err != nil {
			// The runtime still must stop and join even if Kind cleanup fails.
			stopErr := s.bootstrap.close(ctx)
			if stopErr == nil && s.bootstrap != nil && s.bootstrap.root == "" && s.bootstrap.process != nil {
				// Removing the private backend also removes its Kind containers.
				s.bootstrapRunner = nil
			}
			return errors.Join(err, stopErr)
		}
		s.bootstrapRunner = nil
	}
	return s.bootstrap.close(ctx)
}

func (s *nativeSiteAttempt) capture(ctx context.Context, runner Runner, phase string, args ...string) ([]byte, error) {
	s.sequence++
	return runner.Capture(ctx, args, fmt.Sprintf("native-site-%03d-%s", s.sequence, phase), 2*time.Minute)
}

// setup is the single source-owned site bootstrap/deployment path. It cannot
// create a tenant, publish native-result.json or claim full demo completion.
func (s *nativeSiteAttempt) setup(ctx context.Context, runner Runner, row nativeInstance, tools, out string, server *OwnedProcess) (Runner, error) {
	ip, err := nativePrimaryAddress(row, s.bridge)
	if err != nil {
		return Runner{}, err
	}
	stage := beginProgress(runner.Progress, "site-05-install-site-controller", PhaseStarted, PhaseFinished, PhaseFailed)
	budget, cancel := context.WithTimeout(ctx, 2*time.Hour)
	err = waitNativeSiteHandoff(budget, s.handoff, server)
	cancel()
	if err == nil {
		err = s.bootstrapOnHost(ctx, runner, ip, tools, out)
	}
	stage.finish(err)
	if err != nil {
		return Runner{}, err
	}
	kubeconfig := filepath.Join(out, "site.kubeconfig")
	if err := admitNativeSiteContract(ctx, s.assets.Installer); err != nil {
		return Runner{}, err
	}
	if err := s.admitAPIClient(ctx, runner, tools, out, kubeconfig); err != nil {
		return Runner{}, err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"XDG_CONFIG_HOME": filepath.Join(out, "config")})
	cli := s.assets.CLI
	if _, err := s.capture(ctx, runner, "configure", cli, "config", "set-site", "--name", "nvke-native-site", "--url", "http://"+ip+":30080", "--kubeconfig", kubeconfig); err != nil {
		return Runner{}, err
	}
	check := beginProgress(runner.Progress, "check-05-install-site-controller", PhaseStarted, PhaseFinished, PhaseFailed)
	_, err = s.capture(ctx, runner, "providers", cli, "get", "providers")
	check.finish(err)
	if err != nil {
		return Runner{}, err
	}
	for _, kind := range []string{"machineimage", "clusterrelease", "nodepoolrelease"} {
		if _, err := s.capture(ctx, runner, "register-"+kind, filepath.Join(tools, "kubectl"), "--kubeconfig", kubeconfig, "apply", "--server-side", "-f", filepath.Join(out, kind+".yaml")); err != nil {
			return Runner{}, err
		}
	}
	return runner, nil
}

func (s *nativeSiteAttempt) ready(ctx context.Context, runner Runner, row nativeInstance, tools, out string, server *OwnedProcess) error {
	var err error
	runner, err = s.setup(ctx, runner, row, tools, out, server)
	if err != nil {
		return err
	}
	kubeconfig := filepath.Join(out, "site.kubeconfig")
	cli := s.assets.CLI
	if err := s.tenant(ctx, runner, cli, tools, out, kubeconfig); err != nil {
		return err
	}
	access := OperatorAccess{kubectl: filepath.Join(tools, "kubectl"), site: kubeconfig, tenant: filepath.Join(out, "tenant.kubeconfig"), siteQCOW: true}
	if err := s.presentStatus(ctx, runner, access, s.walkthrough); err != nil {
		return err
	}
	if s.conformance != nil {
		if err := s.runConformance(ctx, runner, tools, out); err != nil {
			return err
		}
	}
	var domains []domainEvidence
	if s.guard != nil {
		domains, err = nativeLinuxVMEvidence(ctx, runner, s.simulatorWork, s.diskDigest)
		if err != nil {
			return err
		}
	}
	if err := privateJSON(filepath.Join(out, "native-result.json"), struct {
		Scope, Network, ImageDigest string
		Nodes                       []tenantNode
		Domains                     []domainEvidence
		Workload                    json.RawMessage
	}{"unqualified-local-demo", s.network, s.diskDigest, s.nodes, domains, s.workload}); err != nil {
		return err
	}
	if runner.Progress != nil {
		fmt.Fprintf(runner.Progress, "Tenant checks passed: two Ready nodes and successful workload/DNS. Site kubeconfig: %s\n", kubeconfig)
	}
	if s.cleanup {
		return nil
	}
	if runner.Progress != nil {
		fmt.Fprintln(runner.Progress, "Demo remains available in this foreground session; Ctrl-C deletes only its owned guests and servers.")
	}
	refresh := time.NewTicker(time.Minute)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-server.done:
			_, err := server.Exited()
			return errors.Join(errors.New("native NICo exited during the retained demo; inspect the private native-nico log"), err)
		case <-refresh.C:
			if err := s.presentStatus(ctx, runner, access, false); err != nil {
				if ctx.Err() != nil && !errors.Is(err, ErrUnjoined) {
					return nil
				}
				return err
			}
		}
	}
}

// Health remains a timestamped read-only observation after tenant acceptance.
// An unhealthy response is displayed without tearing down the retained demo;
// output/admission failures, cancellation and unjoined children still propagate.
func presentNativeStatus(ctx context.Context, runner Runner, access OperatorAccess) error {
	if runner.Progress == nil {
		return nil
	}
	err := access.WriteLiveStatus(ctx, runner, runner.Progress)
	if errors.Is(err, errOperatorHealthUnconfirmed) && !errors.Is(err, ErrUnjoined) && ctx.Err() == nil {
		return nil
	}
	return err
}

// Observe the existing child owner without another goroutine or a second Wait.
// A lost runtime cannot be turned into a successful bootstrap callback.
func waitNativeSiteHandoff(ctx context.Context, handoff *nativeHandoff, server *OwnedProcess) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if exited, err := server.Exited(); exited {
		return errors.Join(errors.New("native NICo exited before site startup completed; inspect the private native-nico log"), err)
	}
	select {
	case <-server.done:
		_, err := server.Exited()
		return errors.Join(errors.New("native NICo exited before site startup completed; inspect the private native-nico log"), err)
	case err := <-handoff.result:
		if exited, exitErr := server.Exited(); exited {
			return errors.Join(err, exitErr, errors.New("native NICo exited during site startup"))
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *nativeSiteAttempt) releases(ctx context.Context, imageURL, api, out string) error {
	for _, kind := range []string{"machineimage", "clusterrelease", "nodepoolrelease"} {
		if err := requireContext(ctx); err != nil {
			return err
		}
		path := filepath.Join(s.assets.Installer, "controller", "test", "fixtures", "releases", "nvke-nico-vms-24.04-arm64-fde-"+kind+".yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return err
		}
		metadata, ok := doc["metadata"].(map[string]any)
		if !ok {
			return errors.New("source release omits metadata")
		}
		metadata["name"] = TenantProfile
		spec, ok := doc["spec"].(map[string]any)
		if !ok {
			return errors.New("source release omits spec")
		}
		if kind == "machineimage" {
			nico, ok := spec["nico"].(map[string]any)
			if !ok {
				return errors.New("source machine image omits NICo input")
			}
			script, ok := nico["ipxeScript"].(string)
			if !ok || strings.Count(script, "http://127.0.0.1/nvke-node.qcow2") != 1 {
				return errors.New("source image URL slot changed")
			}
			nico["ipxeScript"] = strings.Replace(script, "http://127.0.0.1/nvke-node.qcow2", imageURL, 1)
			ospec, ok := spec["operatingSystem"].(map[string]any)
			if !ok {
				return errors.New("source machine image omits OS description")
			}
			ospec["version"] = "26.04"
			// The source fixture's date is retained as diagnostic configuration;
			// this projection grants no new disk build or Release provenance.
		} else {
			ref, ok := spec["machineImageRef"].(map[string]any)
			if !ok {
				return errors.New("source release omits machine image binding")
			}
			ref["name"] = TenantProfile
			if err := nativeDemoPackagePolicy(spec); err != nil {
				return fmt.Errorf("native %s startup policy: %w", kind, err)
			}
			if workload, selected := s.assets.Images["workload"]; selected {
				digest, err := DigestFile(ctx, workload.File)
				if err != nil {
					return err
				}
				if err := nativeDemoWorkloadPolicy(spec, api+"/files/workload.tar", digest); err != nil {
					return err
				}
			}
			if err := nativeDemoOVNPackagePolicy(spec, s.network); err != nil {
				return fmt.Errorf("native %s network package policy: %w", kind, err)
			}
		}
		raw, err = yaml.Marshal(doc)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, kind+".yaml"), raw, 0600); err != nil {
			return err
		}
	}
	return nil
}

// Bind the reviewed cluster/nodepool startup block before projecting its
// workload handoff. NVKE remains the template owner; this compares exact source
// bytes and does not implement its template or cloud-config grammar.
func nativeDemoWorkloadPolicy(spec map[string]any, location, digest string) error {
	init, ok := spec["machineInit"].(map[string]any)
	if !ok {
		return errors.New("source release omits machine initialization")
	}
	data, ok := init["userDataTemplate"].(string)
	if !ok || strings.Count(data, "\nruncmd:\n") != 1 || strings.Count(data, "\nphone_home:\n") != 1 {
		return errors.New("source startup differs from the supported workload import handoff")
	}
	start, end := strings.Index(data, "\nruncmd:\n"), strings.Index(data, "\nphone_home:\n")
	if end <= start {
		return errors.New("source startup differs from the reviewed workload handoff")
	}
	// Exact runcmd bytes from the b615 and 69ef9924 ARM64 fixtures; only
	// their OVN package version differs. Unknown later service-start items
	// must not bypass this gate when cloud-init continues after a failure.
	switch plainSHA([]byte(data[start:end])) {
	case "a928fb81a53f1cd4cb24b1ff7db0160d8a6b68529b089270dc81a7a44a4fe6ea",
		"8f36aacbb4484ff1e15cb027c7a4e6c99bab5629f6b42e1d26bcdc80cb513846":
	default:
		return errors.New("source startup differs from the reviewed workload handoff")
	}
	const install = "  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-agent nvke-kubeadm-distro-{{ trimPrefix .KubernetesVersion \"v\" }} spire-agent\n"
	const restart = "  - sysctl --system\n  - systemctl daemon-reload\n  - systemctl restart spire-agent.service\n  - systemctl restart nvke-agent.service\n"
	const file = "/var/lib/nvke-demo-workload.tar"
	const state = "/run/nvke-demo-workload-packages.state"
	// The package's supported flag defers its postinst agent restart. Once
	// kubeadm-distro has installed containerd/ctr/crictl, one shell owns import,
	// local CRI admission and the original ordered service restarts.
	script := "set -euo pipefail; test \"$(cat " + state + ")\" = ready; sysctl --system; systemctl daemon-reload; curl --fail --silent --show-error --connect-timeout 10 --max-time 120 " + operatorQuote(location) + " -o " + file +
		"; printf '%s  %s\\n' " + operatorQuote(strings.TrimPrefix(digest, "sha256:")) + " " + file + " | sha256sum -c -; systemctl start containerd.service; " +
		"ctr --address /run/containerd/containerd.sock --namespace k8s.io --timeout 120s images import --platform linux/arm64 --label io.cri-containerd.image=managed " + file +
		"; crictl --runtime-endpoint unix:///run/containerd/containerd.sock --image-endpoint unix:///run/containerd/containerd.sock --timeout 10s inspecti --quiet " + operatorQuote(WorkloadImage) + " >/dev/null; " +
		"systemctl restart spire-agent.service; systemctl restart nvke-agent.service"
	command, err := json.Marshal([]string{"/bin/bash", "-ec", script})
	if err != nil {
		return err
	}
	// Cloud-init also continues after a failed package item, including a
	// partial install that left a usable runtime. Invalidate stale readiness
	// before apt, and permit the final handoff only after apt returns success.
	installScript := "set -euo pipefail\numask 077\nprintf '%s\\n' pending > " + state + "\n" +
		strings.TrimPrefix(strings.Replace(install, "DEBIAN_FRONTEND=", "NVKE_AGENT_SKIP_START=1 DEBIAN_FRONTEND=", 1), "  - ") +
		"printf '%s\\n' ready > " + state + "\n"
	// A literal YAML scalar preserves the NVKE-owned trimPrefix action's
	// quotes. JSON encoding this script would escape them before NVKE parses
	// its Go template and make the original action invalid.
	encodedInstall, err := yaml.Marshal([][]string{{"/bin/bash", "-ec", installScript}})
	if err != nil {
		return err
	}
	installItem := "  " + strings.ReplaceAll(strings.TrimSuffix(string(encodedInstall), "\n"), "\n", "\n  ") + "\n"
	projected := strings.Replace(data, install, installItem, 1)
	init["userDataTemplate"] = strings.Replace(projected, restart, "  - "+string(command)+"\n", 1)
	return nil
}

// Adapt only the pinned fixture's known cloud-config header. This is a source
// edit, not a Go-template or YAML parser; NVKE remains the template/schema owner.
// The local demo OS profile disables package refresh and upgrades because the
// image provisioner indexes its baked local repository before completing.
func nativeDemoPackagePolicy(spec map[string]any) error {
	init, ok := spec["machineInit"].(map[string]any)
	if !ok {
		return errors.New("source release omits machine initialization")
	}
	data, ok := init["userDataTemplate"].(string)
	const enabled, disabled = "\npackage_update: true\n", "\npackage_update: false\n"
	if !ok || !strings.HasPrefix(data, "#cloud-config\n") ||
		strings.Count(data, enabled)+strings.Count(data, disabled) != 1 ||
		strings.Count(data, "\npackage_upgrade: false\n") != 1 {
		return errors.New("source release package header differs from the supported demo fixture")
	}
	init["userDataTemplate"] = strings.Replace(data, enabled, disabled, 1)
	return nil
}

func (s *nativeSiteAttempt) tenant(ctx context.Context, runner Runner, cli, tools, out, siteConfig string) error {
	stage := beginProgress(runner.Progress, "tenant-provisioning", DeploymentStarted, DeploymentFinished, DeploymentFailed)
	var result error
	defer func() { stage.finish(result) }()
	result = s.runTenant(ctx, runner, cli, tools, out, siteConfig)
	return result
}

// NVKE owns request validation. Select its reviewed wire fields from the same
// source contract used for bootstrap and deployment on both native hosts.
func (s *nativeSiteAttempt) tenantRequest(ctx context.Context) (TenantRequest, error) {
	request, err := newTenantRequest("native-demo-tenant", s.network)
	if err != nil {
		return TenantRequest{}, err
	}
	if err := admitNativeSiteContract(ctx, s.assets.Installer); err != nil {
		return TenantRequest{}, err
	}
	request.VIP = ""
	request.ControlPlane.NICo.Release, request.ControlPlane.NICo.OS = request.ControlPlane.NICo.OS, ""
	for i := range request.NodePools {
		request.NodePools[i].NICo.Release, request.NodePools[i].NICo.OS = request.NodePools[i].NICo.OS, ""
	}
	// The source default pod range contains the Mac vmnet underlay. Keep both
	// tenant ranges separate from that underlay and the site Kubernetes ranges.
	request.Network = &TenantNetwork{PodCIDR: "10.245.0.0/16", ServiceCIDR: "10.97.0.0/16"}
	if !s.bridge.prefix.IsValid() {
		return TenantRequest{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "tenant requires an observed guest subnet")
	}
	for _, network := range []string{request.Network.PodCIDR, request.Network.ServiceCIDR} {
		if s.bridge.prefix.Overlaps(netip.MustParsePrefix(network)) {
			return TenantRequest{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "tenant Kubernetes range %s overlaps guest subnet %s", network, s.bridge.prefix)
		}
	}
	return request, nil
}

func (s *nativeSiteAttempt) runTenant(ctx context.Context, runner Runner, cli, tools, out, siteConfig string) error {
	request, err := s.tenantRequest(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(out, "tenant-request.json")
	if err := privateJSON(path, request); err != nil {
		return err
	}
	s.tenantOwner = &tenant{name: request.Name, network: s.network, cliPath: cli, session: ManagementSession{Work: out, Tools: tools, Runner: runner}}
	rows, err := s.tenantOwner.inventory(ctx)
	if err != nil {
		return err
	}
	if row, err := selectedTenant(rows, request.Name, ""); err != nil || row != nil {
		return errors.Join(err, errors.New("native tenant name already exists"))
	}
	s.tenantOwner.intent = true
	if _, err := s.capture(ctx, runner, "tenant-create", cli, "create", "cluster", "-f", path); err != nil {
		return err
	}
	row, err := s.tenantOwner.row(ctx)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("NVKE omitted the exact created native tenant")
	}
	s.tenantOwner.id = row.ID
	timeout := s.tenantTimeout
	if timeout == 0 {
		timeout = 45 * time.Minute
	}
	budget, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := budget.Deadline()
	progress := tenantProgress{writer: runner.Progress, started: time.Now(), deadline: deadline}
	tenantConfig := filepath.Join(out, "tenant.kubeconfig")
	kubectl := filepath.Join(tools, "kubectl")
	for {
		progress.update(time.Now(), "tenant-kubeconfig", nil)
		raw, err := nativeReadinessObservation(budget, runner.Progress, func(ctx context.Context) ([]byte, error) {
			return s.capture(ctx, runner, "tenant-config-secret", kubectl, "--kubeconfig", siteConfig, "-n", "nke-system", "get", "secret", request.Name+"-admin-kubeconfig", "--ignore-not-found", "-o", "json")
		})
		if err != nil {
			return err
		}
		published, err := tenantKubeconfigPublished(raw)
		if err != nil {
			return err
		}
		if published {
			if _, err := s.capture(budget, runner, "tenant-config", cli, "get", "kubeconfig", request.Name, "--output-file", tenantConfig); err != nil {
				return err
			}
			break
		}
		if err := pause(budget, 10*time.Second); err != nil {
			return err
		}
	}
	var observed []tenantNode
	progress.update(time.Now(), "tenant-api", nil)
	for {
		raw, err := s.capture(budget, runner, "tenant-nodes", kubectl, "--kubeconfig", tenantConfig, "--request-timeout=10s", "get", "nodes", "-o", "json")
		if err == nil {
			var nodes struct{ Items []tenantNode }
			if err := json.Unmarshal(raw, &nodes); err != nil {
				return err
			}
			observed = nodes.Items
			ready, err := readyNodes(nodes.Items)
			if err != nil {
				return err
			}
			progress.update(time.Now(), "tenant-nodes", observed)
			if ready {
				break
			}
		} else if CommandExitCode(err) != 1 {
			return err
		}
		if err := pause(budget, 10*time.Second); err != nil {
			return err
		}
	}
	capture := func(ctx context.Context, args []string, phase string, timeout time.Duration) ([]byte, error) {
		s.sequence++
		return runner.Capture(ctx, args, fmt.Sprintf("native-site-%03d-%s", s.sequence, phase), timeout)
	}
	if err := waitTenantDNS(budget, tenantConfig, kubectl, runner.Progress, capture); err != nil {
		return err
	}
	progress.update(time.Now(), "tenant-workload", observed)
	s.nodes = observed
	s.workload, err = runTenantWorkload(budget, out, tenantConfig, kubectl, capture)
	return err
}

// The source-owned controller chart admits API ingress from cluster pods.
// Add the documented operator rule for this demo's observed gateway only.
func (s *nativeSiteAttempt) admitAPIClient(ctx context.Context, runner Runner, tools, out, kubeconfig string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	gateway := s.bridge.gateway
	if !gateway.Is4() || !gateway.IsPrivate() || !s.bridge.prefix.Contains(gateway) {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "API client admission requires the observed private IPv4 demo gateway")
	}
	raw, err := s.capture(ctx, runner, "api-client-controller", filepath.Join(tools, "kubectl"), "--kubeconfig", kubeconfig,
		"-n", "nke-controller", "get", "deployments", "-l", "app.kubernetes.io/instance=nke-controller", "-o", "json")
	if err != nil {
		return err
	}
	var observed struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Selector json.RawMessage `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &observed); err != nil {
		return err
	}
	if len(observed.Items) != 1 || observed.Items[0].Metadata.Namespace != "nke-controller" {
		return errors.New("API client admission requires exactly one controller Deployment in its namespace")
	}
	selector := observed.Items[0].Spec.Selector
	var selected struct {
		MatchLabels map[string]string `json:"matchLabels"`
	}
	if err := json.Unmarshal(selector, &selected); err != nil {
		return err
	}
	if selected.MatchLabels["app.kubernetes.io/instance"] != "nke-controller" {
		return errors.New("API client admission requires the exact controller release selector")
	}
	// Preserve the server's complete selector. Kubernetes, through kubectl apply,
	// owns LabelSelector validation; this consumer does not parse its grammar.
	policy := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]string{"name": "nvke-demo-api-client", "namespace": "nke-controller"},
		"spec": map[string]any{"podSelector": selector, "policyTypes": []string{"Ingress"},
			"ingress": []any{map[string]any{"from": []any{map[string]any{"ipBlock": map[string]string{"cidr": netip.PrefixFrom(gateway, 32).String()}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": 8080}}}}}}
	path := filepath.Join(out, "nvke-api-client.json")
	if err := privateJSON(path, policy); err != nil {
		return err
	}
	_, err = s.capture(ctx, runner, "api-client-admission", filepath.Join(tools, "kubectl"), "--kubeconfig", kubeconfig, "apply", "--server-side", "-f", path)
	return err
}
